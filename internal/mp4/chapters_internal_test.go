package mp4

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
)

func TestSentinelToZero64(t *testing.T) {
	cases := []struct {
		v, sentinel, want uint64
	}{
		{0xFFFFFFFF, 0xFFFFFFFF, 0},                  // v0 "unknown duration"
		{123, 0xFFFFFFFF, 123},                       // a real v0 duration
		{0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF, 0},  // v1 "unknown duration"
		{0xFFFFFFFF, 0xFFFFFFFFFFFFFFFF, 0xFFFFFFFF}, // not the v1 sentinel
	}
	for _, c := range cases {
		if got := sentinelToZero64(c.v, c.sentinel); got != c.want {
			t.Errorf("sentinelToZero64(%#x, %#x) = %d, want %d", c.v, c.sentinel, got, c.want)
		}
	}
}

func TestChapterDeltasLastChapterBounded(t *testing.T) {
	chs := []core.Chapter{{Start: 0}, {Start: 5 * time.Second}}
	// An unknown movie duration (the sentinel maps to 0) must give the final
	// chapter a one-second tail, not a multi-week span - the regression a raw
	// 0xFFFFFFFF movieDuration would cause.
	if d := chapterDeltas(chs, 1000, 0); d[1] != 1000 {
		t.Errorf("last delta with unknown duration = %d, want 1000 (1s tail)", d[1])
	}
	// A real movie duration bounds the last chapter to the remaining span.
	if d := chapterDeltas(chs, 1000, 9000); d[1] != 4000 {
		t.Errorf("last delta with duration 9000 = %d, want 4000", d[1])
	}
	// An out-of-order start cannot encode a negative span. Defense in depth behind
	// the editor's sort: clamp to one unit so every chapter still spans End > Start.
	if d := chapterDeltas([]core.Chapter{{Start: 5 * time.Second}, {Start: time.Second}}, 1000, 0); d[0] != 1 {
		t.Errorf("backwards gap delta = %d, want 1 (clamped to the one-unit minimum)", d[0])
	}
	// Two chapters at the same start must still give the first one a nonzero duration.
	if d := chapterDeltas([]core.Chapter{{Start: time.Second}, {Start: time.Second}}, 1000, 0); d[0] != 1 {
		t.Errorf("same-start delta = %d, want 1 (one-unit minimum, not a zero-length chapter)", d[0])
	}
}

// Within a chapterEdts atom (renderAtom("edts", renderFullBox("elst", ...))) the
// layout is: edts size+name (8), elst size+name (8), version/flags (4), then
// entry_count (4) at byte 20, then 12-byte v0 entries from byte 24.
const (
	elstCountOff = 20
	elstEntry0   = 24
)

// TestChapterEdtsEmptyEdit pins the wire format of the chapter track's edit list.
// A non-zero first chapter is positioned by a leading empty edit
// (media_time -1), not zero-anchored. Asserting the raw bytes matters because a
// round-trip alone only proves WaxLabel's (lenient) reader agrees with its writer;
// iTunes and Apple Books read these exact fields. A zero-start list keeps the
// original single normal entry, byte for byte.
func TestChapterEdtsEmptyEdit(t *testing.T) {
	const firstStart, mediaDur = uint64(4000), uint64(9000)
	be := binary.BigEndian

	t.Run("non-zero start writes an empty edit", func(t *testing.T) {
		edts := chapterEdts(firstStart, mediaDur)
		if got := be.Uint32(edts[elstCountOff : elstCountOff+4]); got != 2 {
			t.Fatalf("entry_count = %d, want 2", got)
		}
		e0 := edts[elstEntry0:]
		if got := uint64(be.Uint32(e0[0:4])); got != firstStart {
			t.Errorf("empty-edit segment_duration = %d, want %d (the first chapter's start)", got, firstStart)
		}
		if got := be.Uint32(e0[4:8]); got != 0xFFFFFFFF {
			t.Errorf("empty-edit media_time = %#x, want 0xFFFFFFFF (-1)", got)
		}
		if got := be.Uint32(e0[8:12]); got != 0x00010000 {
			t.Errorf("empty-edit media_rate = %#x, want 0x00010000 (1.0)", got)
		}
		e1 := edts[elstEntry0+12:]
		if got := uint64(be.Uint32(e1[0:4])); got != mediaDur {
			t.Errorf("normal segment_duration = %d, want %d", got, mediaDur)
		}
		if got := be.Uint32(e1[4:8]); got != 0 {
			t.Errorf("normal media_time = %d, want 0", got)
		}
	})

	t.Run("zero start stays a single normal entry", func(t *testing.T) {
		edts := chapterEdts(0, mediaDur)
		if got := be.Uint32(edts[elstCountOff : elstCountOff+4]); got != 1 {
			t.Fatalf("entry_count = %d, want 1 (no empty edit)", got)
		}
		e0 := edts[elstEntry0:]
		if got := uint64(be.Uint32(e0[0:4])); got != mediaDur {
			t.Errorf("single-entry segment_duration = %d, want %d", got, mediaDur)
		}
		if got := be.Uint32(e0[4:8]); got != 0 {
			t.Errorf("single-entry media_time = %d, want 0", got)
		}
		// edts(8) + elst(8) + version/flags(4) + entry_count(4) + one 12-byte entry.
		if len(edts) != elstEntry0+12 {
			t.Errorf("zero-start edts length = %d, want %d (single entry)", len(edts), elstEntry0+12)
		}
	})
}

// TestBuildChapterTrakEditGating checks the empty edit is written only when the
// movie timescale is known. With it, a non-zero first chapter yields a 2-entry
// elst; without it (timescale 0, the malformed-mvhd fallback) the track stays
// zero-anchored - the reader cannot resolve the movie timescale to honor an edit,
// so writing one would only desync the result from a reparse.
func TestBuildChapterTrakEditGating(t *testing.T) {
	chs := []core.Chapter{{Start: 4 * time.Second, Title: "A"}, {Start: 9 * time.Second, Title: "B"}}
	if c := trakElstEntryCount(t, mustBuildTrak(2, 1000, 1000, chs)); c != 2 {
		t.Errorf("with movie timescale: elst entry_count = %d, want 2 (empty edit)", c)
	}
	if c := trakElstEntryCount(t, mustBuildTrak(2, 1000, 0, chs)); c != 1 {
		t.Errorf("without movie timescale: elst entry_count = %d, want 1 (no empty edit)", c)
	}
}

func mustBuildTrak(trackID, mts, movieTimescale uint32, chs []core.Chapter) []byte {
	trak, _ := buildChapterTrak(trackID, mts, movieTimescale, 0, chs, false)
	return trak
}

// trakElstEntryCount reads the entry_count of the single elst the chapter trak
// carries (entry_count sits 8 bytes past the "elst" tag: name 4 + version/flags 4).
func trakElstEntryCount(t *testing.T, trak []byte) uint32 {
	t.Helper()
	i := bytes.Index(trak, []byte("elst"))
	if i < 0 || i+12 > len(trak) {
		t.Fatalf("no elst found in chapter trak")
	}
	return binary.BigEndian.Uint32(trak[i+8 : i+12])
}

// TestAddClampSaturates pins the overflow guard that keeps a chapter Start from
// wrapping negative when a hostile elst drives the empty-edit offset to ~MaxInt64
// (scaleToDuration clamps each operand, but the sum needs its own clamp).
func TestAddClampSaturates(t *testing.T) {
	const maxDur = time.Duration(1<<63 - 1) // math.MaxInt64
	if got := addClamp(maxDur, time.Second); got != maxDur {
		t.Errorf("addClamp(MaxInt64, 1s) = %d, want MaxInt64 (saturated, not wrapped negative)", got)
	}
	if got := addClamp(3*time.Second, 4*time.Second); got != 7*time.Second {
		t.Errorf("addClamp(3s, 4s) = %v, want 7s", got)
	}
	if got := addClamp(0, 0); got != 0 {
		t.Errorf("addClamp(0, 0) = %v, want 0", got)
	}
}

// TestEmptyEditOffset is the read side of that contract: an elst whose first entry is an empty
// edit (media_time -1) yields its segment_duration as a Duration scaled by the
// movie timescale; a normal first entry (or zero entries) yields no offset. It is
// the inverse of chapterEdts.
func TestEmptyEditOffset(t *testing.T) {
	be := binary.BigEndian
	elst := func(count uint32, entries ...byte) []byte {
		p := make([]byte, 8) // version/flags + entry_count
		be.PutUint32(p[4:8], count)
		return append(p, entries...)
	}
	entry := func(segDur uint32, mediaTime int32) []byte {
		e := make([]byte, 12)
		be.PutUint32(e[0:4], segDur)
		be.PutUint32(e[4:8], uint32(mediaTime))
		be.PutUint32(e[8:12], 0x00010000)
		return e
	}
	// An empty edit of 4000 units at timescale 1000 -> 4s.
	p := elst(2, append(entry(4000, -1), entry(9000, 0)...)...)
	if got := emptyEditOffset(p, 1000); got != 4*time.Second {
		t.Errorf("empty-edit offset = %v, want 4s", got)
	}
	// A normal first entry -> no offset (a foreign track without an empty edit).
	if got := emptyEditOffset(elst(1, entry(9000, 0)...), 1000); got != 0 {
		t.Errorf("normal-edit offset = %v, want 0", got)
	}
	// Zero entries -> no offset.
	if got := emptyEditOffset(elst(0), 1000); got != 0 {
		t.Errorf("empty-elst offset = %v, want 0", got)
	}
}
