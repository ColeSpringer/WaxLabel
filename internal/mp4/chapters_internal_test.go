package mp4

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// TestCollectMvhdAgreesWithMovieTimingOf is the regression for the write/reparse twin divergence
// a valid-but-truncated mvhd would introduce: collectMvhd (write path, populates
// d.movieTimescale/d.movieDuration) and movieTimingOf (reparse, feeds the chapter last-end
// canonicalization) must read the same timing at the same per-field thresholds. A v0 mvhd with a
// present timescale+duration but cut off before next_track_ID (byte 96) previously left the write
// path's timing zero while a reparse read it - desyncing the last-end canonicalization and
// churning the file on an identical re-apply.
func TestCollectMvhdAgreesWithMovieTimingOf(t *testing.T) {
	payload := make([]byte, 50)                      // v0, 50 bytes: past the duration field (byte 20), before next_track_ID (byte 96)
	binary.BigEndian.PutUint32(payload[12:16], 1000) // timescale
	binary.BigEndian.PutUint32(payload[16:20], 9000) // duration
	moovBytes := renderAtom(atomName("moov"), renderAtom(atomName("mvhd"), payload))
	src := core.BytesSource(moovBytes)
	limit := int64(len(moovBytes))
	nodes, err := walkAtoms(src, 0, limit, bits.NewDepth(bits.DefaultLimits.MaxDepth), maxMetaChunk, true)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("walkAtoms: %d nodes, err %v", len(nodes), err)
	}
	moov := nodes[0]
	mvhd, ok := moov.find("mvhd")
	if !ok {
		t.Fatal("mvhd not found in the walked moov")
	}
	ts, dur := movieTimingOf(src, moov, limit)
	if ts != 1000 || dur != 9000 {
		t.Errorf("movieTimingOf = (%d, %d), want (1000, 9000) from the truncated mvhd", ts, dur)
	}
	var d doc
	collectMvhd(src, mvhd, &d, limit)
	if d.movieTimescale != ts || d.movieDuration != dur {
		t.Errorf("collectMvhd = (%d, %d) but movieTimingOf = (%d, %d): a truncated mvhd desyncs the write vs reparse timing",
			d.movieTimescale, d.movieDuration, ts, dur)
	}
}

// ffmpeg-compatible chpl parsing skips the Nero reserved field for any non-zero
// version, not just version 1. A version-2 atom should parse and render back to
// the same payload.
func TestDecodeChplV2SkipsReservedField(t *testing.T) {
	payload := []byte{
		2, 0, 0, 0, // version 2 + flags
		0, 0, 0, 0, // reserved 32-bit field (must be skipped for v2, as it is for v1)
		1,                        // chapter count
		0, 0, 0, 0, 0, 0, 3, 232, // start = 1000 chpl units (100 ns each)
		5, 'I', 'n', 't', 'r', 'o', // length-prefixed UTF-8 title
	}
	raw := append(make([]byte, 8), payload...) // 8-byte dummy atom header; payloadOff() = 8
	n := node{name: [4]byte{'c', 'h', 'p', 'l'}, offset: 0, headerLen: 8, size: int64(len(raw))}

	version, chapters, ok := decodeChpl(core.BytesSource(raw), n, int64(len(raw)))
	if !ok {
		t.Fatal("decodeChpl(v2) returned ok=false; the reserved field was not skipped for version 2")
	}
	if version != 2 {
		t.Errorf("version = %d, want 2", version)
	}
	if len(chapters) != 1 {
		t.Fatalf("chapters = %d, want 1", len(chapters))
	}
	if want := scaleToDuration(1000, chplStartUnit); chapters[0].Start != want {
		t.Errorf("chapter Start = %v, want %v (a mis-skipped reserved field shifts the start)", chapters[0].Start, want)
	}
	if chapters[0].Title != "Intro" {
		t.Errorf("chapter Title = %q, want %q", chapters[0].Title, "Intro")
	}

	// renderChpl is symmetric: a v2 atom re-emits the reserved field, so the
	// payload past the atom header matches the input byte for byte.
	if got := renderChpl(2, chapters); !bytes.Equal(got[8:], payload) {
		t.Errorf("renderChpl(2) payload = % x, want % x", got[8:], payload)
	}
}

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
	if d, _ := chapterDeltas(chs, 1000, 1000, 0); d[1] != 1000 {
		t.Errorf("last delta with unknown duration = %d, want 1000 (1s tail)", d[1])
	}
	// A real movie duration bounds the last chapter to the remaining span.
	if d, _ := chapterDeltas(chs, 1000, 1000, 9000); d[1] != 4000 {
		t.Errorf("last delta with duration 9000 = %d, want 4000", d[1])
	}
	// An out-of-order start cannot encode a negative span. Defense in depth behind
	// the editor's sort: clamp to one unit so every chapter still spans End > Start.
	if d, _ := chapterDeltas([]core.Chapter{{Start: 5 * time.Second}, {Start: time.Second}}, 1000, 1000, 0); d[0] != 1 {
		t.Errorf("backwards gap delta = %d, want 1 (clamped to the one-unit minimum)", d[0])
	}
	// Two chapters at the same start must still give the first one a nonzero duration.
	if d, _ := chapterDeltas([]core.Chapter{{Start: time.Second}, {Start: time.Second}}, 1000, 1000, 0); d[0] != 1 {
		t.Errorf("same-start delta = %d, want 1 (one-unit minimum, not a zero-length chapter)", d[0])
	}
}

// TestChapterDeltasRepaysCollisionDebt checks that the one-unit duration used for a
// duplicate start is subtracted from later slack, so later distinct starts reconstruct
// exactly. [1s, 1s, 5s, 8s] reconstructs to 1s / 1.001s / 5s / 8s.
func TestChapterDeltasRepaysCollisionDebt(t *testing.T) {
	chs := []core.Chapter{
		{Start: 1 * time.Second},
		{Start: 1 * time.Second},
		{Start: 5 * time.Second},
		{Start: 8 * time.Second},
	}
	deltas, saturated := chapterDeltas(chs, 1000, 1000, 10000)
	if saturated {
		t.Error("no delta should saturate for second-scale starts")
	}
	// The leading empty edit anchors chapter 0 at its start; each later absolute start
	// is that plus the running sum of preceding deltas, exactly as the reader sums them.
	const firstStart uint64 = 1000
	want := []uint64{1000, 1001, 5000, 8000}
	var sum uint64
	for i, d := range deltas {
		if abs := firstStart + sum; abs != want[i] {
			t.Errorf("chapter %d reconstructed start = %d, want %d (deltas %v)", i, abs, want[i], deltas)
		}
		sum += uint64(d)
	}
}

// TestChapterDeltasSaturationFlag checks that a chapter start past the 32-bit stts
// field (~49.7 days at the movie timescale) sets the saturation flag surfaced as
// WarnChapterStartOverflow, while an ordinary list does not.
func TestChapterDeltasSaturationFlag(t *testing.T) {
	if _, sat := chapterDeltas([]core.Chapter{{Start: 0}, {Start: 5 * time.Second}}, 1000, 1000, 0); sat {
		t.Error("ordinary second-scale chapters must not flag saturation")
	}
	huge := []core.Chapter{{Start: 0}, {Start: 50 * 24 * time.Hour}} // ~50 days > MaxUint32 ms units
	if _, sat := chapterDeltas(huge, 1000, 1000, 0); !sat {
		t.Error("a >49.7-day chapter gap must flag saturation")
	}
}

// TestBuildChapterTrakLeadingOffsetSaturates checks that a first chapter past the
// 32-bit edit-list segment_duration (~49.7 days) flags saturation even when every
// inter-chapter delta is small. chapterDeltas sees only gaps, so buildChapterTrak
// must also account for the leading empty edit.
func TestBuildChapterTrakLeadingOffsetSaturates(t *testing.T) {
	day := 24 * time.Hour
	// First chapter at 60 days (> MaxUint32 ms units), the next only 5s later.
	chs := []core.Chapter{{Start: 60 * day}, {Start: 60*day + 5*time.Second}}
	if _, satDeltas := chapterDeltas(chs, 1000, 1000, 0); satDeltas {
		t.Fatal("setup: the small inter-chapter gap should not saturate the deltas")
	}
	if _, _, sat := buildChapterTrak(2, 1000, 1000, 0, chs, false); !sat {
		t.Error("a first chapter past ~49.7 days must flag saturation (leading empty edit clamped)")
	}
	if _, _, sat := buildChapterTrak(2, 1000, 1000, 0, []core.Chapter{{Start: 0}, {Start: 5 * time.Second}}, false); sat {
		t.Error("a normal chapter list must not flag saturation")
	}
}

// TestBuildChapterTrakCumulativeSpanSaturates checks that a chapter list whose cumulative
// span exceeds the 90 kHz mdhd/tkhd/elst 32-bit duration field flags saturation even when
// every individual inter-chapter gap fits (chapterDeltas alone reports none). Without folding
// the cumulative totalDur / totalDurMovie / firstStart+totalDurMovie spans into the flag, a
// real >13.25 h audiobook would write a clamped, un-warned chapter-track duration. Mirrors the
// report's repro (0:00 / 13:00:00 / 13:30:00) at the default 90 kHz chapter media timescale.
func TestBuildChapterTrakCumulativeSpanSaturates(t *testing.T) {
	const mts = chapterMediaTimescale // 90 kHz: MaxUint32 units is ~13.25 h
	chs := []core.Chapter{
		{Start: 0},
		{Start: 13 * time.Hour},
		{Start: 13*time.Hour + 30*time.Minute, End: 13*time.Hour + 30*time.Minute + time.Second},
	}
	// Each gap (13 h, then 30 min) is under the ~13.25 h per-field ceiling, so no per-gap stts
	// delta clamps - the flag must come from the cumulative span, not a single delta.
	if _, satDeltas := chapterDeltas(chs, mts, mts, 0); satDeltas {
		t.Fatal("setup: no single inter-chapter gap should saturate the stts deltas")
	}
	// Their sum (~13.5 h of 90 kHz units) overflows the mdhd/tkhd/elst 32-bit field.
	if _, _, sat := buildChapterTrak(2, mts, mts, 0, chs, false); !sat {
		t.Error("a >13.25 h cumulative chapter span must flag saturation (mdhd/tkhd/elst clamped)")
	}
	// An ordinary sub-13.25 h list at the same timescale must not flag.
	short := []core.Chapter{{Start: 0}, {Start: time.Hour}}
	if _, _, sat := buildChapterTrak(2, mts, mts, 0, short, false); sat {
		t.Error("an ordinary sub-13.25 h chapter list must not flag saturation")
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
	trak, _, _ := buildChapterTrak(trackID, mts, movieTimescale, 0, chs, false)
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
	// An empty edit of 4000 units at timescale 1000 -> 4s, not saturated.
	p := elst(2, append(entry(4000, -1), entry(9000, 0)...)...)
	if got, sat := emptyEditOffset(p, 1000); got != 4*time.Second || sat {
		t.Errorf("empty-edit offset = %v/sat=%v, want 4s/false", got, sat)
	}
	// A normal first entry -> no offset (a foreign track without an empty edit).
	if got, sat := emptyEditOffset(elst(1, entry(9000, 0)...), 1000); got != 0 || sat {
		t.Errorf("normal-edit offset = %v/sat=%v, want 0/false", got, sat)
	}
	// Zero entries -> no offset.
	if got, sat := emptyEditOffset(elst(0), 1000); got != 0 || sat {
		t.Errorf("empty-elst offset = %v/sat=%v, want 0/false", got, sat)
	}
	// A segment_duration read back as exactly MaxUint32 is a clamped leading offset (a
	// first chapter past the u32 movie-timescale ceiling), so saturated must be set - that is
	// how mergeChapters learns to take the exact chpl start over the clamped QuickTime one.
	if got, sat := emptyEditOffset(elst(1, entry(math.MaxUint32, -1)...), 1000); !sat {
		t.Errorf("MaxUint32 segment_duration offset = %v/sat=%v, want saturated=true", got, sat)
	}
}

// TestSpliceBytesCoincidentOffsetOrdering pins the tie-break for two reps sharing a start (a
// combined tag+chapter edit where a chpl insert lands exactly at meta.end()): a zero-width insert
// must be applied before a same-offset replace, deterministically and regardless of input order.
// Without the tie-break sort.Slice ordered by luck, and emitting the replace first advances pos
// past the insert's start, tripping the disjoint-range guard.
func TestSpliceBytesCoincidentOffsetOrdering(t *testing.T) {
	src := []byte("AABBCC") // replace the "BB" pair at offset 2 with "XX", insert "II" at offset 2
	insert := byteRep{start: 2, oldLen: 0, repl: []byte("II")}
	replace := byteRep{start: 2, oldLen: 2, repl: []byte("XX")}
	want := "AAIIXXCC" // insert's bytes precede the replacement's at the shared offset

	for _, order := range [][]byteRep{{insert, replace}, {replace, insert}} {
		reps := append([]byteRep(nil), order...) // sort.Slice mutates in place; give each run its own copy
		got, err := spliceBytes(src, reps)
		if err != nil {
			t.Fatalf("spliceBytes(order %+v): %v", order, err)
		}
		if string(got) != want {
			t.Errorf("spliceBytes = %q, want %q (insert must precede a same-offset replace, order-independent)", got, want)
		}
	}
}
