package waxlabel_test

import (
	"bytes"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// regression guard: the QuickTime reader recovers the last chapter's end from the stts running
// total. An explicit end that lands below the movie duration must survive the round trip, and the
// in-memory result must equal a fresh reparse.
func TestMP4ChapterLastEndRecoveredBelowMovieDuration(t *testing.T) {
	src := readFixture(t, sampleM4B) // movie duration 9 s
	res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 4 * time.Second, End: 6 * time.Second, Title: "B"},
		)
	})
	if !equalChapterLists(res.Chapters(), re.Chapters()) {
		t.Errorf("result %+v != reparse %+v", res.Chapters(), re.Chapters())
	}
	chs := re.Chapters()
	if len(chs) != 2 {
		t.Fatalf("got %d chapters, want 2", len(chs))
	}
	if chs[1].End != 6*time.Second {
		t.Errorf("last chapter End = %v, want 6s (recovered from the QuickTime stts total)", chs[1].End)
	}
	if chs[0].End != 4*time.Second {
		t.Errorf("interior End = %v, want 4s (filled from the next start)", chs[0].End)
	}
}

// direction the last-chapter end takes: an open last chapter is written as a span to the movie
// duration and read back as that end, not canonicalized to 0.
func TestMP4ChapterOpenLastMaterializesMovieDuration(t *testing.T) {
	src := readFixture(t, sampleM4B) // movie duration 9 s
	res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 4 * time.Second, Title: "B"}, // open: spans to the 9 s movie end
		)
	})
	if !equalChapterLists(res.Chapters(), re.Chapters()) {
		t.Errorf("result %+v != reparse %+v", res.Chapters(), re.Chapters())
	}
	chs := re.Chapters()
	if len(chs) != 2 {
		t.Fatalf("got %d chapters, want 2", len(chs))
	}
	if chs[1].End != 9*time.Second {
		t.Errorf("open last chapter End = %v, want 9s (the movie duration it spans to)", chs[1].End)
	}
}

// reversal's payoff, and the report's own repro: an authored last-chapter end that lands exactly on
// the movie duration must read back as that value, not as null.
func TestMP4ChapterConcreteEndAtMovieDurationRoundTrips(t *testing.T) {
	src := readFixture(t, sampleMP4) // movie duration 1000 ms
	res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, End: 500 * time.Millisecond, Title: "A"},
			wl.Chapter{Start: 500 * time.Millisecond, End: 1000 * time.Millisecond, Title: "B"},
		)
	})
	if !equalChapterLists(res.Chapters(), re.Chapters()) {
		t.Errorf("result %+v != reparse %+v", res.Chapters(), re.Chapters())
	}
	chs := re.Chapters()
	if len(chs) != 2 {
		t.Fatalf("got %d chapters, want 2", len(chs))
	}
	if chs[1].End != 1000*time.Millisecond {
		t.Errorf("last chapter End = %v, want 1s (authored at the movie duration, reported verbatim)", chs[1].End)
	}
}

// open half of the asymmetry: a last chapter starting past the movie duration gets a synthetic
// placeholder tail on write that isPlaceholderTail recognizes, so it reads back open rather than as
// an end our own writer invented.
func TestMP4ChapterOpenLastPastMovieDurationReadsOpen(t *testing.T) {
	src := readFixture(t, sampleM4B) // movie duration 9 s
	res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 9500 * time.Millisecond, Title: "Epilogue"}, // past the 9 s movie end
		)
	})
	if !equalChapterLists(res.Chapters(), re.Chapters()) {
		t.Errorf("result %+v != reparse %+v (read and write predictor must agree)", res.Chapters(), re.Chapters())
	}
	chs := re.Chapters()
	if len(chs) != 2 {
		t.Fatalf("got %d chapters, want 2", len(chs))
	}
	if chs[1].End != 0 {
		t.Errorf("past-duration open last chapter End = %v, want 0 (no fabricated placeholder tail)", chs[1].End)
	}
	if chs[1].Start != 9500*time.Millisecond {
		t.Errorf("past-duration last chapter Start = %v, want 9.5s (exact)", chs[1].Start)
	}
}

// placeholder predicate where a Duration comparison would stop firing. At the 90 kHz chapter
// timescale the gap between adjacent units is 11111 ns except at unit counts 4 mod 9, which round
// up to 11112 while scaleToDuration(1) stays 11111.
func TestMP4ChapterPlaceholderTailOnUnitGrid(t *testing.T) {
	src := readFixture(t, sampleM4B) // movie duration 9 s, chapter media timescale 90000
	const pastDuration = 9000044444 * time.Nanosecond
	res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: pastDuration, Title: "Epilogue"},
		)
	})
	if !equalChapterLists(res.Chapters(), re.Chapters()) {
		t.Errorf("result %+v != reparse %+v (read and write predictor must agree on the unit grid)", res.Chapters(), re.Chapters())
	}
	chs := re.Chapters()
	if len(chs) != 2 {
		t.Fatalf("got %d chapters, want 2", len(chs))
	}
	if chs[1].End != 0 {
		t.Errorf("past-duration open last chapter End = %v, want 0 (one-unit placeholder tail, recognized on the unit grid)", chs[1].End)
	}
}

// regression guard: coincident and sub-millisecond-apart starts drift in the QuickTime stts (a
// duplicate start borrows a unit that only repays from later slack), but the uint64 Nero chpl keeps
// them exact. When the two sources agree, the read must take the chpl's exact starts, not the
// drifted QuickTime ones.
func TestMP4ChapterExactChplStartsOverDriftedQT(t *testing.T) {
	src := readFixture(t, sampleM4B)
	// Three coincident starts at 3 s, then two 1 ms apart; the report's 10/10/10/.001/.002 shape, kept
	// inside the fixture's 9 s duration.
	res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 3 * time.Second, Title: "A"},
			wl.Chapter{Start: 3 * time.Second, Title: "B"},
			wl.Chapter{Start: 3 * time.Second, Title: "C"},
			wl.Chapter{Start: 3*time.Second + 1*time.Millisecond, Title: "D"},
			wl.Chapter{Start: 3*time.Second + 2*time.Millisecond, Title: "E"},
		)
	})
	if !equalChapterLists(res.Chapters(), re.Chapters()) {
		t.Errorf("result %+v != reparse %+v", res.Chapters(), re.Chapters())
	}
	chs := re.Chapters()
	if len(chs) != 5 {
		t.Fatalf("got %d chapters, want 5", len(chs))
	}
	want := []time.Duration{
		3 * time.Second,
		3 * time.Second, // exact, not the drifted ~3.0000111 s the QuickTime stts would read
		3 * time.Second,
		3*time.Second + 1*time.Millisecond,
		3*time.Second + 2*time.Millisecond,
	}
	for i, w := range want {
		if chs[i].Start != w {
			t.Errorf("chapter %d start = %v, want %v (exact chpl start, not drifted QuickTime)", i, chs[i].Start, w)
		}
	}
	if chapterWarn(re, wl.WarnChapterSourceConflict) {
		t.Error("agreeing chpl and QuickTime tables (sub-ms drift is within tolerance) must not conflict")
	}
}

// regression guard: a chapter list carrying a last-chapter end now round-trips, so re-applying it
// is a true no-op with byte-identical output; three re-copies in a row report "no changes" and
// never churn the file.
func TestMP4ChapterCopyWithEndConverges(t *testing.T) {
	src := readFixture(t, sampleM4B)
	first, err := mustParseBytes(t, src).Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "A"},
		wl.Chapter{Start: 4 * time.Second, End: 6 * time.Second, Title: "B"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, first)

	cur := out
	for i := 1; i <= 3; i++ {
		doc := mustParseBytes(t, cur)
		// A re-copy re-applies the read-back list (which now carries the recovered end).
		plan, err := doc.Edit().SetChapters(doc.Chapters()...).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if !plan.IsNoOp() {
			t.Errorf("re-copy %d should be a no-op; operations: %v", i, plan.Report().Operations)
		}
		next := applyToBytes(t, cur, plan)
		if !bytes.Equal(next, out) {
			t.Errorf("re-copy %d changed the bytes; a chapters-with-end copy must converge", i)
		}
		cur = next
	}
}

// regression for a chapter gap past ~13.25 h: the 90 kHz QuickTime stts delta clamps
// (WarnChapterStartOverflow), corrupting the QuickTime starts, but the exact uint64 Nero chpl
// survives.
func TestMP4ChapterGapPastClampPrefersChpl(t *testing.T) {
	src := readFixture(t, sampleM4B)
	res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 14 * time.Hour, Title: "B"}, // 14 h > 13.25 h clamps the QuickTime delta
		)
	})
	if !equalChapterLists(res.Chapters(), re.Chapters()) {
		t.Errorf("result %+v != reparse %+v (twins must stay in lockstep)", res.Chapters(), re.Chapters())
	}
	chs := re.Chapters()
	if got := chs[1].Start; got != 14*time.Hour {
		t.Errorf("second chapter start = %v, want 14h (exact chpl preferred over the clamped QuickTime start)", got)
	}
	// The saturated QuickTime last end is garbage (< the chpl start); it must be left open, not
	// pinned to a value before the start (an invalid End < Start interval).
	if got := chs[1].End; got != 0 {
		t.Errorf("second chapter end = %v, want 0 (open); a clamped QuickTime end must not pin End < Start", got)
	}
	if got := chs[0].Start; got != 0 {
		t.Errorf("first chapter start = %v, want 0", got)
	}
}

// regression guard: a first chapter starting past the u32 movie-timescale ceiling (here 5,000,000 s
// at a 1 ms movie timescale, ~57.9 days > the ~49.7 day field) carries its start in the leading
// empty edit, whose u32 segment_duration clamps to MaxUint32.
func TestMP4ChapterOversizedStartPrefersChpl(t *testing.T) {
	src := readFixture(t, sampleM4B) // movie timescale 1000 (1 ms)
	const farStart = 5_000_000 * time.Second
	res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(wl.Chapter{Start: farStart, Title: "Far"})
	})
	if !equalChapterLists(res.Chapters(), re.Chapters()) {
		t.Errorf("result %+v != reparse %+v (read and write predictor must agree)", res.Chapters(), re.Chapters())
	}
	chs := re.Chapters()
	if len(chs) != 1 {
		t.Fatalf("got %d chapters, want 1", len(chs))
	}
	if chs[0].Start != farStart {
		t.Errorf("oversized start = %v, want %v (exact chpl, not the clamped QuickTime edit)", chs[0].Start, farStart)
	}
	if chapterWarn(re, wl.WarnChapterSourceConflict) || chapterWarn(res, wl.WarnChapterSourceConflict) {
		t.Errorf("a clamped-edit saturation is a lossy-representation artifact, not a source conflict; res=%v re=%v",
			res.Warnings(), re.Warnings())
	}
}

// corner where coincident starts borrow stts units that an open last chapter's own slack repays, so
// its recovered end lands a few units short of the movie duration.
func TestMP4ChapterCoincidentOpenLastMirrors(t *testing.T) {
	src := readFixture(t, sampleM4B)
	res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 2 * time.Second, Title: "A"},
			wl.Chapter{Start: 2 * time.Second, Title: "B"}, // coincident, and the open last chapter
		)
	})
	if !equalChapterLists(res.Chapters(), re.Chapters()) {
		t.Errorf("result %+v != reparse %+v (mirror must hold even in the coincident-open corner)", res.Chapters(), re.Chapters())
	}
	if chs := re.Chapters(); len(chs) != 2 || chs[0].Start != 2*time.Second || chs[1].Start != 2*time.Second {
		t.Errorf("coincident starts not read exact from the chpl: %+v", re.Chapters())
	}
}

// shared mirror-invariant guard the whole read/predictor divergence class depends on: for each
// chapter edit, the in-memory Result.Chapters and the chapter-source-conflict flag must equal a
// fresh parse of the written bytes.
func TestMP4ChapterWriteReparseInvariant(t *testing.T) {
	src := readFixture(t, sampleM4B)
	sec := func(s int) time.Duration { return time.Duration(s) * time.Second }
	cases := []struct {
		name string
		chs  []wl.Chapter
	}{
		{"plain", []wl.Chapter{{Start: 0, Title: "A"}, {Start: sec(3), Title: "B"}}},
		{"last-end-below-duration", []wl.Chapter{{Start: 0, Title: "A"}, {Start: sec(4), End: sec(6), Title: "B"}}},
		{"open-last-within-duration", []wl.Chapter{{Start: 0, Title: "A"}, {Start: sec(4), Title: "B"}}},
		{"open-last-past-duration", []wl.Chapter{{Start: 0, Title: "A"}, {Start: 9500 * time.Millisecond, Title: "Epilogue"}}},
		// The last start lands on a media unit whose Duration rounds up (4 mod 9 at mts 90000),
		// where a Duration-grid placeholder test and the unit-grid one disagree.
		{"open-last-past-duration-unit-grid", []wl.Chapter{{Start: 0, Title: "A"}, {Start: 9000044444 * time.Nanosecond, Title: "Epilogue"}}},
		{"coincident-open-last", []wl.Chapter{{Start: sec(2), Title: "A"}, {Start: sec(2), Title: "B"}}},
		{"gap-past-stts-clamp", []wl.Chapter{{Start: 0, Title: "A"}, {Start: 14 * time.Hour, Title: "B"}}}, // stts saturation
		{"oversized-start", []wl.Chapter{{Start: 5_000_000 * time.Second, Title: "Far"}}},                  // edit-list saturation
		{"oversized-start-with-open-last", []wl.Chapter{{Start: 5_000_000 * time.Second, Title: "Far"}, {Start: 5_000_001 * time.Second, Title: "Farther"}}},
		// firstStart lands exactly on the u32 movie-timescale ceiling (4294967295 ms at the 1 ms
		// movie timescale): the edit's segment_duration stores MaxUint32 without clamping, so the
		// read and write-predictor saturated flags must agree at that exact boundary (predictor >=).
		{"firststart-exactly-maxuint32", []wl.Chapter{{Start: 4_294_967_295 * time.Millisecond, Title: "Edge"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor { return e.SetChapters(c.chs...) })
			if !equalChapterLists(res.Chapters(), re.Chapters()) {
				t.Errorf("Result.Chapters %+v != reparse %+v (read/predictor drift)", res.Chapters(), re.Chapters())
			}
			if got, want := chapterWarn(res, wl.WarnChapterSourceConflict), chapterWarn(re, wl.WarnChapterSourceConflict); got != want {
				t.Errorf("conflict-flag mismatch: result=%v reparse=%v", got, want)
			}
		})
	}
}
