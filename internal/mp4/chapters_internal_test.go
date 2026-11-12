package mp4

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// TestCollectMvhdAgreesWithMovieTimingOf: collectMvhd (write path, populates
// d.movieTimescale/d.movieDuration) and movieTimingOf (reparse, feeds the chapter
// last-end canonicalization) must read the same timing from a valid-but-truncated mvhd
// at the same per-field thresholds.
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
	ts, dur, err := movieTimingOf(src, moov, limit)
	if err != nil || ts != 1000 || dur != 9000 {
		t.Errorf("movieTimingOf = (%d, %d, %v), want (1000, 9000) from the truncated mvhd", ts, dur, err)
	}
	var d doc
	if err := collectMvhd(src, mvhd, &d, limit); err != nil {
		t.Fatal(err)
	}
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

	version, chapters, ok, err := decodeChpl(core.BytesSource(raw), n, &titleReader{page: &windows1252}, int64(len(raw)))
	if err != nil || !ok {
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
	// An unknown movie duration (the sentinel maps to 0) must give the final chapter a
	// one-unit tail, not the multi-week span a raw 0xFFFFFFFF movieDuration would cause.
	if d, _, _ := chapterDeltas(chs, 1000, 1000, 0); d[1] != 1 {
		t.Errorf("last delta with unknown duration = %d, want 1 (one-unit tail)", d[1])
	}
	// A real movie duration bounds the last chapter to the remaining span.
	if d, _, _ := chapterDeltas(chs, 1000, 1000, 9000); d[1] != 4000 {
		t.Errorf("last delta with duration 9000 = %d, want 4000", d[1])
	}
	// An out-of-order start cannot encode a negative span. Defense in depth behind
	// the editor's sort: clamp to one unit so every chapter still spans End > Start.
	if d, _, _ := chapterDeltas([]core.Chapter{{Start: 5 * time.Second}, {Start: time.Second}}, 1000, 1000, 0); d[0] != 1 {
		t.Errorf("backwards gap delta = %d, want 1 (clamped to the one-unit minimum)", d[0])
	}
	// Two chapters at the same start must still give the first one a nonzero duration.
	if d, _, _ := chapterDeltas([]core.Chapter{{Start: time.Second}, {Start: time.Second}}, 1000, 1000, 0); d[0] != 1 {
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
	deltas, saturated, _ := chapterDeltas(chs, 1000, 1000, 10000)
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
	if _, sat, _ := chapterDeltas([]core.Chapter{{Start: 0}, {Start: 5 * time.Second}}, 1000, 1000, 0); sat {
		t.Error("ordinary second-scale chapters must not flag saturation")
	}
	huge := []core.Chapter{{Start: 0}, {Start: 50 * 24 * time.Hour}} // ~50 days > MaxUint32 ms units
	if _, sat, _ := chapterDeltas(huge, 1000, 1000, 0); !sat {
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
	if _, satDeltas, _ := chapterDeltas(chs, 1000, 1000, 0); satDeltas {
		t.Fatal("setup: the small inter-chapter gap should not saturate the deltas")
	}
	if _, _, sat := buildChapterTrak(2, 1000, 1000, 0, chs, false); !sat {
		t.Error("a first chapter past ~49.7 days must flag saturation (leading empty edit clamped)")
	}
	if _, _, sat := buildChapterTrak(2, 1000, 1000, 0, []core.Chapter{{Start: 0}, {Start: 5 * time.Second}}, false); sat {
		t.Error("a normal chapter list must not flag saturation")
	}
}

// TestBuildChapterTrakCumulativeSpanSaturates checks that a chapter list whose
// cumulative span exceeds the 90 kHz mdhd/tkhd/elst 32-bit duration field flags
// saturation even when every individual inter-chapter gap fits (chapterDeltas alone
// reports none).
func TestBuildChapterTrakCumulativeSpanSaturates(t *testing.T) {
	const mts = chapterMediaTimescale // 90 kHz: MaxUint32 units is ~13.25 h
	chs := []core.Chapter{
		{Start: 0},
		{Start: 13 * time.Hour},
		{Start: 13*time.Hour + 30*time.Minute, End: 13*time.Hour + 30*time.Minute + time.Second},
	}
	// Each gap (13 h, then 30 min) is under the ~13.25 h per-field ceiling, so no per-gap stts
	// delta clamps; the flag must come from the cumulative span.
	if _, satDeltas, _ := chapterDeltas(chs, mts, mts, 0); satDeltas {
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

// trakTimes reads back the durations a chapter trak was built with: the stts deltas and the
// mdhd duration in media units, the tkhd duration and the elst segments in movie units.
func trakTimes(t *testing.T, trak []byte) (deltas []uint32, mdhd, tkhd uint32, elst []uint32) {
	t.Helper()
	be := binary.BigEndian
	at := func(name string) []byte {
		i := bytes.Index(trak, []byte(name))
		if i < 0 {
			t.Fatalf("no %s in the chapter trak", name)
		}
		return trak[i+4:]
	}
	stts := at("stts")
	for i := range int(be.Uint32(stts[4:])) {
		deltas = append(deltas, be.Uint32(stts[8+8*i+4:]))
	}
	segs := at("elst")
	for i := range int(be.Uint32(segs[4:])) {
		elst = append(elst, be.Uint32(segs[8+12*i:]))
	}
	return deltas, be.Uint32(at("mdhd")[16:]), be.Uint32(at("tkhd")[20:]), elst
}

// The chapter media runs on to where the track ends on the movie timeline, so a reader that
// takes the media duration for the last chapter's end, as ffmpeg does, has it right when the
// first chapter starts late. The edit list still plays only the span the chapters cover.
func TestBuildChapterTrakMediaReachesTheTrackEnd(t *testing.T) {
	const mts, movieTS, movieDur = 90000, 1000, 9000
	sec := func(n uint32) uint32 { return n * mts }
	for _, c := range []struct {
		name   string
		chs    []core.Chapter
		deltas []uint32
		mdhd   uint32
		tkhd   uint32
		elst   []uint32
	}{
		{"late first chapter, open last", []core.Chapter{{Start: 2 * time.Second}, {Start: 5 * time.Second}},
			[]uint32{sec(3), sec(6)}, sec(9), 9000, []uint32{2000, 7000}},
		{"late first chapter, last ends early", []core.Chapter{{Start: 2 * time.Second}, {Start: 5 * time.Second, End: 8 * time.Second}},
			[]uint32{sec(3), sec(5)}, sec(8), 8000, []uint32{2000, 6000}},
		{"a lone late chapter", []core.Chapter{{Start: 4 * time.Second}},
			[]uint32{sec(9)}, sec(9), 9000, []uint32{4000, 5000}},
		{"first chapter at zero", []core.Chapter{{Start: 0}, {Start: 5 * time.Second}},
			[]uint32{sec(5), sec(4)}, sec(9), 9000, []uint32{9000}},
		// The edit that plays the media is never written with no length.
		{"a late chapter shorter than a movie unit", []core.Chapter{{Start: 4 * time.Second, End: 4*time.Second + 100*time.Microsecond}},
			[]uint32{sec(4) + 9}, sec(4) + 9, 4001, []uint32{4000, 1}},
		// A first start that rounds to one movie unit, beside a span the same rounding shortens,
		// would read back as media ending with its edit: the media stays as long as the chapters.
		{"a first start the rounding hides", []core.Chapter{{Start: time.Millisecond}, {Start: 5 * time.Second, End: 8*time.Second + 500*time.Microsecond}},
			[]uint32{sec(5) - 90, sec(3) + 45}, sec(8) - 45, 8001, []uint32{1, 8000}},
		// Past the movie's end a span of one second reads as an open chapter's, so an end that
		// the first start would stretch to exactly that stays where it is.
		{"an end the longer media would lose", []core.Chapter{{Start: 250 * time.Millisecond}, {Start: 9 * time.Second, End: 9*time.Second + 750*time.Millisecond}},
			[]uint32{sec(9) - 22500, 67500}, sec(9) + 45000, 9750, []uint32{250, 9500}},
		// A last chapter at the movie's end has no span to extend: its one-unit tail stays, so
		// a read still takes it for open.
		{"last chapter at the movie end", []core.Chapter{{Start: 4 * time.Second}, {Start: 9 * time.Second}},
			[]uint32{sec(5), 1}, sec(5) + 1, 9000, []uint32{4000, 5000}},
	} {
		trak, _, saturated := buildChapterTrak(2, mts, movieTS, movieDur, c.chs, false)
		deltas, mdhd, tkhd, elst := trakTimes(t, trak)
		if saturated || !slices.Equal(deltas, c.deltas) || mdhd != c.mdhd || tkhd != c.tkhd || !slices.Equal(elst, c.elst) {
			t.Errorf("%s: stts %v, mdhd %d, tkhd %d, elst %v (saturated %v); want %v, %d, %d, %v",
				c.name, deltas, mdhd, tkhd, elst, saturated, c.deltas, c.mdhd, c.tkhd, c.elst)
		}
	}
}

// The media runs on only where every field holds it: past the 32-bit media duration, or beside an
// edit segment that clamps, it stays as long as the chapters and nothing new saturates.
func TestBuildChapterTrakMediaStaysWhereAFieldWouldClamp(t *testing.T) {
	const mts = 90000
	hours := func(h float64) time.Duration { return time.Duration(h * float64(time.Hour)) }
	for _, c := range []struct {
		name      string
		movieTS   uint32
		movieDur  time.Duration
		chs       []core.Chapter
		mdhd      time.Duration // the media duration written
		saturated bool
	}{
		{"media duration past 32 bits", 1000, hours(13) + 20*time.Minute, []core.Chapter{{Start: 10 * time.Minute}, {Start: hours(6)}},
			hours(13) + 10*time.Minute, false},
		{"an edit segment that clamps", 1_000_000, hours(2), []core.Chapter{{Start: time.Minute}, {Start: hours(1.5)}},
			hours(2) - time.Minute, true},
		{"a book that fits", 1000, hours(13), []core.Chapter{{Start: 10 * time.Minute}, {Start: hours(6)}}, hours(13), false},
	} {
		movieDur := uint64(c.movieDur.Seconds() * float64(c.movieTS))
		trak, _, saturated := buildChapterTrak(2, mts, c.movieTS, movieDur, c.chs, false)
		if _, mdhd, _, _ := trakTimes(t, trak); saturated != c.saturated || uint64(mdhd) != durationToUnits(c.mdhd, mts) {
			t.Errorf("%s: media %d units, saturated %v; want %d, %v", c.name, mdhd, saturated, durationToUnits(c.mdhd, mts), c.saturated)
		}
	}
}

// TestBuildChapterTrakEditGating checks the empty edit is written only when the movie
// timescale is known.
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

// TestEditListSpans is the read side of that contract: an elst whose first entry is an empty
// edit (media_time -1) yields its segment_duration as the lead, and the edits that play media
// sum to the span played, both in movie units.
func TestEditListSpans(t *testing.T) {
	be := binary.BigEndian
	elst := func(version byte, count uint32, entries ...[]byte) []byte {
		p := make([]byte, 8) // version/flags + entry_count
		p[0] = version
		be.PutUint32(p[4:8], count)
		return append(p, slices.Concat(entries...)...)
	}
	entry := func(segDur uint32, mediaTime int32) []byte {
		e := make([]byte, 12)
		be.PutUint32(e[0:4], segDur)
		be.PutUint32(e[4:8], uint32(mediaTime))
		be.PutUint32(e[8:12], 0x00010000)
		return e
	}
	entry64 := func(segDur uint64, mediaTime int64) []byte {
		e := make([]byte, 20)
		be.PutUint64(e[0:8], segDur)
		be.PutUint64(e[8:16], uint64(mediaTime))
		be.PutUint32(e[16:20], 0x00010000)
		return e
	}
	for _, c := range []struct {
		name         string
		p            []byte
		lead, played uint64
		clamped      bool
	}{
		{"an empty edit, then the media", elst(0, 2, entry(4000, -1), entry(9000, 0)), 4000, 9000, false},
		{"the media alone", elst(0, 1, entry(9000, 0)), 0, 9000, false},
		{"two edits that play", elst(0, 3, entry(4000, -1), entry(3000, 0), entry(2000, 5000)), 4000, 5000, false},
		{"a later empty edit is no lead", elst(0, 2, entry(3000, 0), entry(4000, -1)), 0, 3000, false},
		{"no entries", elst(0, 0), 0, 0, false},
		// A segment_duration read back as exactly MaxUint32 is a clamped lead: a first chapter past
		// the 32-bit ceiling, which the merge then takes from the chpl.
		{"a clamped lead", elst(0, 1, entry(math.MaxUint32, -1)), math.MaxUint32, 0, true},
		{"64-bit entries", elst(1, 2, entry64(1<<33, -1), entry64(9000, 0)), 1 << 33, 9000, false},
		{"a count past the entries", elst(0, 9, entry(4000, -1), entry(9000, 0)), 4000, 9000, false},
		{"a cut-off entry", elst(0, 2, entry(4000, -1), entry(9000, 0)[:7]), 4000, 0, false},
		{"an unknown version", elst(2, 1, entry(9000, 0)), 0, 0, false},
		{"spans that overflow", elst(1, 2, entry64(math.MaxUint64, 0), entry64(5, 0)), 0, math.MaxUint64, false},
		// A 32-bit span of exactly MaxUint32 was clamped when written, so how long it plays is unknown.
		{"a clamped span", elst(0, 2, entry(4000, -1), entry(math.MaxUint32, 0)), 4000, 0, false},
		{"a clamped span among others", elst(0, 2, entry(math.MaxUint32, 0), entry(9000, 0)), 0, 0, false},
		{"64-bit fields hold that value whole", elst(1, 2, entry64(math.MaxUint32, -1), entry64(math.MaxUint32, 0)), math.MaxUint32, math.MaxUint32, false},
	} {
		if lead, played, clamped := editListSpans(c.p); lead != c.lead || played != c.played || clamped != c.clamped {
			t.Errorf("%s: lead %d, played %d, clamped %v; want %d, %d, %v", c.name, lead, played, clamped, c.lead, c.played, c.clamped)
		}
	}
}

// The last chapter ends where the track's media does on the movie timeline: with the edits when
// the media runs past them, at the media's own duration, the finer of the two, when that reaches
// the track's end, and never at or before the chapter's own start.
func TestTrackEnd(t *testing.T) {
	const s, ms, us = time.Second, time.Millisecond, time.Microsecond
	slack := gridSlack(1000, 90000)
	if want := ms + 11111*time.Nanosecond; slack != want {
		t.Fatalf("slack %v, want one unit of each timescale, %v", slack, want)
	}
	for _, c := range []struct {
		name      string
		mediaEnd  time.Duration
		lastStart time.Duration
		ed        trackEdits
		want      time.Duration
	}{
		{"no edit list", 9 * s, 5 * s, trackEdits{}, 9 * s},
		{"media the edit plays whole", 7 * s, 5 * s, trackEdits{offset: 2 * s, played: 7 * s}, 9 * s},
		{"an edit rounded short of the media", 7*s + 400*us, 5 * s, trackEdits{offset: 2 * s, played: 7 * s}, 9*s + 400*us},
		{"media that reaches the track's end", 9 * s, 5 * s, trackEdits{offset: 2 * s, played: 7 * s}, 9 * s},
		{"the track's end on the finer grid", 8*s + 400*us, 5 * s, trackEdits{offset: 2 * s, played: 6 * s}, 8*s + 400*us},
		{"media an edit cuts short", 20 * s, 5 * s, trackEdits{offset: 2 * s, played: 7 * s}, 9 * s},
		{"media an edit cuts short, no lead", 20 * s, 5 * s, trackEdits{played: 7 * s}, 7 * s},
		{"media short of the edit", 4 * s, 3 * s, trackEdits{offset: 2 * s, played: 7 * s}, 6 * s},
		{"media short of the edit, no lead", 4 * s, 3 * s, trackEdits{played: 7 * s}, 4 * s},
		// A lead no longer than the rounding: the media ends nearer the track's end or the edit's.
		{"a lead of one unit, media to the track's end", 8 * s, 5 * s, trackEdits{offset: ms, played: 7999 * ms}, 8 * s},
		{"a lead of one unit, media the edit plays whole", 7999 * ms, 5 * s, trackEdits{offset: ms, played: 7999 * ms}, 8 * s},
		// Edits that end before the last chapter starts place no end for it: the media's own stands.
		{"edits that end before the last start", 10 * s, 8 * s, trackEdits{played: 5 * s}, 10 * s},
		{"edits that end at the last start", 10 * s, 7 * s, trackEdits{offset: 2 * s, played: 5 * s}, 12 * s},
		{"media duration at the last start", 4*s + ms, 4*s + ms, trackEdits{offset: 4*s + ms, played: ms}, 8*s + 2*ms},
		{"a sum past the clamp", math.MaxInt64, 5 * s, trackEdits{offset: s}, math.MaxInt64},
	} {
		if got := trackEnd(c.mediaEnd, c.lastStart, c.ed, slack); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSpliceBytesCoincidentOffsetOrdering pins the tie-break for two reps sharing a
// start (a combined tag+chapter edit where a chpl insert falls exactly at meta.end()):
// a zero-width insert is applied before a same-offset replace regardless of input order.
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

// TestIsPlaceholderTail covers both synthetic tails: the one media unit chapterDeltas
// writes now, and the whole second earlier releases wrote.
func TestIsPlaceholderTail(t *testing.T) {
	const mts, movieTS, movieDur = 90000, 1000, 9000 // 9 s movie, 90 kHz chapter track
	past := 10 * time.Second
	within := 5 * time.Second
	cases := []struct {
		name  string
		start time.Duration
		delta uint64
		want  bool
	}{
		{"one unit past the duration", past, 1, true},
		{"one second past the duration", past, mts, true},
		{"a real end past the duration", past, 2 * mts, false},
		{"one unit within the duration", within, 1, false},
		{"exactly at the duration", 9 * time.Second, 1, true},
	}
	for _, c := range cases {
		if got := isPlaceholderTail(c.start, c.delta, mts, movieTS, movieDur); got != c.want {
			t.Errorf("%s: isPlaceholderTail = %v, want %v", c.name, got, c.want)
		}
	}
	// Unknown movie timing disables the test entirely.
	if isPlaceholderTail(past, 1, mts, 0, 0) {
		t.Error("an unknown movie duration must not recognize a placeholder")
	}
}

// readLog records the offset and length of every read it serves.
type readLog struct {
	core.ReaderAtSized
	reads [][2]int64
}

func (r *readLog) ReadAt(p []byte, off int64) (int, error) {
	r.reads = append(r.reads, [2]int64{off, int64(len(p))})
	return r.ReaderAtSized.ReadAt(p, off)
}

// A chapter sample's title is read once, after its 2-byte length, and only the title counts
// against the limit: the length is not read again with it, and the style atoms after it are not
// read at all.
func TestReadTextSampleReadsTitleAlone(t *testing.T) {
	sample := append([]byte{0, 5}, "Intro"...)
	sample = append(sample, make([]byte, 100)...) // style atoms after the title
	src := &readLog{ReaderAtSized: core.BytesSource(sample)}
	title, err := readTextSample(src, 0, int64(len(sample)), &titleReader{page: &macRoman}, 5)
	if err != nil || title != "Intro" {
		t.Fatalf("readTextSample = %q, %v, want Intro", title, err)
	}
	if want := [][2]int64{{0, 2}, {2, 5}}; !slices.Equal(src.reads, want) {
		t.Errorf("reads (offset, length) = %v, want %v", src.reads, want)
	}
}

// shortAt serves every read but one that starts at off, which comes back short.
type shortAt struct {
	core.ReaderAtSized
	off int64
}

func (r shortAt) ReadAt(p []byte, off int64) (int, error) {
	if off == r.off {
		return 0, io.ErrUnexpectedEOF
	}
	return r.ReaderAtSized.ReadAt(p, off)
}

// A trak whose tref cannot be read is left out of the references: a rewrite from no payload
// would drop what else the tref holds.
func TestFindChapterTrackSkipsATrefItCannotRead(t *testing.T) {
	hdlr := func(kind string) []byte {
		return renderAtom(atomName("hdlr"), slices.Concat(make([]byte, 8), []byte(kind), make([]byte, 12)))
	}
	tkhd := func(id uint32) []byte {
		return renderAtom(atomName("tkhd"), slices.Concat(make([]byte, 12), be32u(id), make([]byte, 4)))
	}
	tref := renderAtom(atomName("tref"), slices.Concat(renderAtom(atomName("hint"), be32(7)), renderAtom(atomName("chap"), be32(2))))
	moov := renderAtom(atomName("moov"), slices.Concat(
		renderAtom(atomName("trak"), slices.Concat(tkhd(1), tref, renderAtom(atomName("mdia"), hdlr("soun")))),
		renderAtom(atomName("trak"), slices.Concat(tkhd(2), renderAtom(atomName("mdia"), hdlr("text"))))))
	whole := core.BytesSource(moov)
	nodes, err := walkAtoms(whole, 0, int64(len(moov)), bits.NewDepth(bits.DefaultLimits.MaxDepth), 1<<20, true)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("walk: %d nodes, %v", len(nodes), err)
	}
	if tr, err := readTracks(whole, nodes[0], 1<<20); err != nil || tr.text != 1 || len(tr.refs) != 1 {
		t.Fatalf("read whole: chapter track %d, %d refs, %v; want track 1 and the sound trak's ref", tr.text, len(tr.refs), err)
	}
	payload := int64(bytes.Index(moov, []byte("tref")) + 4)
	if tr, err := readTracks(shortAt{whole, payload}, nodes[0], 1<<20); err != nil || tr.text != -1 || len(tr.refs) != 0 {
		t.Errorf("tref unread: chapter track %d, %d refs, %v; want none of either", tr.text, len(tr.refs), err)
	}
}

// A sample's UTF-16 title is decoded, and the encoding atom after it is no part of it.
func TestReadTextSampleUTF16(t *testing.T) {
	for name, title := range map[string][]byte{
		"big-endian":    {0xFE, 0xFF, 0, 'H', 0, 'i'},
		"little-endian": {0xFF, 0xFE, 'H', 0, 'i', 0},
	} {
		sample := slices.Concat([]byte{0, byte(len(title))}, title, encdBox)
		got, err := readTextSample(core.BytesSource(sample), 0, int64(len(sample)), &titleReader{page: &macRoman}, 1<<20)
		if err != nil || got != "Hi" {
			t.Errorf("%s: readTextSample = %q, %v, want Hi", name, got, err)
		}
	}
}

// Every id of every "chap" entry is read, and an entry that names none still counts as one.
func TestChapIDs(t *testing.T) {
	chap := func(ids ...uint32) []byte {
		var list []byte
		for _, id := range ids {
			list = append(list, be32u(id)...)
		}
		return renderAtom(atomName("chap"), list)
	}
	hint := renderAtom(atomName("hint"), be32(7))
	if ids, found := chapIDs(slices.Concat(chap(2, 3), hint, chap(), chap(5), []byte{0, 0})); !found || !slices.Equal(ids, []uint32{2, 3, 5}) {
		t.Errorf("ids %v (found %v), want 2, 3 and 5", ids, found)
	}
	if ids, found := chapIDs(slices.Concat(hint, chap())); !found || len(ids) != 0 {
		t.Errorf("an empty entry: %d ids, found %v; want none, found", len(ids), found)
	}
	if ids, found := chapIDs(hint); found || len(ids) != 0 {
		t.Errorf("no entry: %d ids, found %v", len(ids), found)
	}
}

// A rewritten tref keeps its other references where they stood, puts the one "chap" entry where
// the first was or else behind the last whole reference, and keeps bytes that form no reference.
func TestTrefWithChap(t *testing.T) {
	chap := func(ids ...uint32) []byte {
		var list []byte
		for _, id := range ids {
			list = append(list, be32u(id)...)
		}
		return renderAtom(atomName("chap"), list)
	}
	hint, sync, tail := renderAtom(atomName("hint"), be32(7)), renderAtom(atomName("sync"), be32(8)), []byte{0, 0, 0, 0}
	for _, c := range []struct {
		name    string
		payload []byte
		ids     []uint32
		want    []byte
	}{
		{"unchanged", slices.Concat(hint, chap(2, 3), sync), []uint32{2, 3}, slices.Concat(hint, chap(2, 3), sync)},
		{"list shortened in place", slices.Concat(hint, chap(2, 3), sync), []uint32{3}, slices.Concat(hint, chap(3), sync)},
		{"entries merged into the first", slices.Concat(chap(99), hint, chap(2)), []uint32{2}, slices.Concat(chap(2), hint)},
		{"entry removed", slices.Concat(hint, chap(2)), nil, hint},
		{"only entry removed", chap(2), nil, nil},
		{"entry added", hint, []uint32{4}, slices.Concat(hint, chap(4))},
		{"entry added to none", nil, []uint32{4}, chap(4)},
		{"entry added ahead of stray bytes", slices.Concat(hint, tail), []uint32{4}, slices.Concat(hint, chap(4), tail)},
		{"stray bytes kept", slices.Concat(chap(2), tail), nil, tail},
	} {
		if got := trefWithChap(c.payload, c.ids); !bytes.Equal(got, c.want) {
			t.Errorf("%s: % x, want % x", c.name, got, c.want)
		}
	}
}

// An mdat is chapter-only when the samples cover it end to end: none that overlap, leave a gap
// or run past its end make it so.
func TestChapterOnlyMdats(t *testing.T) {
	mdats := [][2]int64{{100, 8}, {200, 8}, {300, 0}}
	for _, c := range []struct {
		name    string
		samples []sampleOffset
		want    []int64
	}{
		{"tiled, out of order", []sampleOffset{{104, 4}, {100, 4}}, []int64{100}},
		{"both filled", []sampleOffset{{100, 8}, {200, 3}, {203, 5}}, []int64{100, 200}},
		{"overlapping halves", []sampleOffset{{100, 4}, {100, 4}}, nil},
		{"overlap that still covers", []sampleOffset{{100, 6}, {102, 6}}, []int64{100}},
		{"gap in the middle", []sampleOffset{{100, 4}, {106, 2}}, nil},
		{"gap at the start", []sampleOffset{{102, 6}}, nil},
		{"past the end", []sampleOffset{{100, 4}, {104, 9}}, nil},
		{"filled, and one past the end", []sampleOffset{{100, 8}, {104, 9}}, nil},
		{"none", nil, nil},
	} {
		if got := chapterOnlyMdats(c.samples, mdats); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
