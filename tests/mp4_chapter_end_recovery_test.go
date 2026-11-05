package waxlabel_test

import (
	"bytes"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// The QuickTime reader recovers the last chapter's end from the stts running total. An explicit
// end below the movie duration must survive the round trip.
func TestMP4ChapterLastEndRecoveredBelowMovieDuration(t *testing.T) {
	src := readFixture(t, sampleM4B) // movie duration 9 s
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 4 * time.Second, End: 6 * time.Second, Title: "B"},
		)
	})
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

// An open last chapter is written as a span to the movie duration and read back as that end,
// not canonicalized to 0.
func TestMP4ChapterOpenLastMaterializesMovieDuration(t *testing.T) {
	src := readFixture(t, sampleM4B) // movie duration 9 s
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 4 * time.Second, Title: "B"}, // open: spans to the 9 s movie end
		)
	})
	chs := re.Chapters()
	if len(chs) != 2 {
		t.Fatalf("got %d chapters, want 2", len(chs))
	}
	if chs[1].End != 9*time.Second {
		t.Errorf("open last chapter End = %v, want 9s (the movie duration it spans to)", chs[1].End)
	}
}

// An authored last-chapter end that lands exactly on the movie duration must read back as that
// value, not as null.
func TestMP4ChapterConcreteEndAtMovieDurationRoundTrips(t *testing.T) {
	src := readFixture(t, sampleMP4) // movie duration 1000 ms
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, End: 500 * time.Millisecond, Title: "A"},
			wl.Chapter{Start: 500 * time.Millisecond, End: 1000 * time.Millisecond, Title: "B"},
		)
	})
	chs := re.Chapters()
	if len(chs) != 2 {
		t.Fatalf("got %d chapters, want 2", len(chs))
	}
	if chs[1].End != 1000*time.Millisecond {
		t.Errorf("last chapter End = %v, want 1s (authored at the movie duration, reported verbatim)", chs[1].End)
	}
}

// A last chapter starting past the movie duration gets a synthetic placeholder tail on write
// that isPlaceholderTail recognizes, so it reads back open rather than as a writer-invented end.
func TestMP4ChapterOpenLastPastMovieDurationReadsOpen(t *testing.T) {
	src := readFixture(t, sampleM4B) // movie duration 9 s
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 9500 * time.Millisecond, Title: "Epilogue"}, // past the 9 s movie end
		)
	})
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
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: pastDuration, Title: "Epilogue"},
		)
	})
	chs := re.Chapters()
	if len(chs) != 2 {
		t.Fatalf("got %d chapters, want 2", len(chs))
	}
	if chs[1].End != 0 {
		t.Errorf("past-duration open last chapter End = %v, want 0 (one-unit placeholder tail, recognized on the unit grid)", chs[1].End)
	}
}

// Coincident and sub-millisecond-apart starts drift in the QuickTime stts (a duplicate start
// borrows a unit that only repays from later slack), but the uint64 Nero chpl keeps them exact.
// When the two sources agree, the read must take the chpl's exact starts.
func TestMP4ChapterExactChplStartsOverDriftedQT(t *testing.T) {
	src := readFixture(t, sampleM4B)
	// Three coincident starts at 3 s, then two 1 ms apart, kept inside the fixture's 9 s duration.
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 3 * time.Second, Title: "A"},
			wl.Chapter{Start: 3 * time.Second, Title: "B"},
			wl.Chapter{Start: 3 * time.Second, Title: "C"},
			wl.Chapter{Start: 3*time.Second + 1*time.Millisecond, Title: "D"},
			wl.Chapter{Start: 3*time.Second + 2*time.Millisecond, Title: "E"},
		)
	})
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

// A chapter list carrying a last-chapter end round-trips, so re-applying it is a no-op with
// byte-identical output; three re-copies in a row report "no changes".
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
		// A re-copy re-applies the read-back list, which carries the recovered end.
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

// A chapter gap past ~13.25 h clamps the 90 kHz QuickTime stts delta (WarnChapterStartOverflow),
// corrupting the QuickTime starts, but the exact uint64 Nero chpl survives.
func TestMP4ChapterGapPastClampPrefersChpl(t *testing.T) {
	src := readFixture(t, sampleM4B)
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 14 * time.Hour, Title: "B"}, // 14 h > 13.25 h clamps the QuickTime delta
		)
	})
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

// A first chapter starting past the u32 movie-timescale ceiling (here 5,000,000 s at a 1 ms
// movie timescale, ~57.9 days > the ~49.7 day field) carries its start in the leading empty
// edit, whose u32 segment_duration clamps to MaxUint32.
func TestMP4ChapterOversizedStartPrefersChpl(t *testing.T) {
	src := readFixture(t, sampleM4B) // movie timescale 1000 (1 ms)
	const farStart = 5_000_000 * time.Second
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(wl.Chapter{Start: farStart, Title: "Far"})
	})
	chs := re.Chapters()
	if len(chs) != 1 {
		t.Fatalf("got %d chapters, want 1", len(chs))
	}
	if chs[0].Start != farStart {
		t.Errorf("oversized start = %v, want %v (exact chpl, not the clamped QuickTime edit)", chs[0].Start, farStart)
	}
	if chapterWarn(re, wl.WarnChapterSourceConflict) {
		t.Errorf("a clamped-edit saturation is a lossy-representation artifact, not a source conflict; warnings=%v", re.Warnings())
	}
}

// Coincident starts borrow stts units that an open last chapter's own slack repays, so its
// recovered end is a few units short of the movie duration.
func TestMP4ChapterCoincidentOpenLastMirrors(t *testing.T) {
	src := readFixture(t, sampleM4B)
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(
			wl.Chapter{Start: 2 * time.Second, Title: "A"},
			wl.Chapter{Start: 2 * time.Second, Title: "B"}, // coincident, and the open last chapter
		)
	})
	if chs := re.Chapters(); len(chs) != 2 || chs[0].Start != 2*time.Second || chs[1].Start != 2*time.Second {
		t.Errorf("coincident starts not read exact from the chpl: %+v", re.Chapters())
	}
}
