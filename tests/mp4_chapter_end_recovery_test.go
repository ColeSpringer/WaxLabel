package waxlabel_test

import (
	"bytes"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// TestMP4ChapterLastEndRecoveredBelowMovieDuration is the M4 regression: the QuickTime
// reader recovers the last chapter's end from the stts running total. An explicit end that
// lands below the movie duration must survive the round trip (only an end that reaches the
// movie duration is canonicalized back to open), and the in-memory result must equal a fresh
// reparse.
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

// TestMP4ChapterOpenLastCanonicalizedAtMovieDuration is the companion to the single-chapter
// discriminator: a multi-chapter list whose open last chapter spans to the movie duration
// reads that end back as open (End 0), so the encoder's "tail to end-of-movie" bytes do not
// resurrect a spurious near-EOF end.
func TestMP4ChapterOpenLastCanonicalizedAtMovieDuration(t *testing.T) {
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
	if chs := re.Chapters(); len(chs) != 2 || chs[1].End != 0 {
		t.Errorf("open last chapter End = %v, want 0 (canonicalized at the movie duration)", chs[len(chs)-1].End)
	}
}

// TestMP4ChapterExactChplStartsOverDriftedQT is the M1 regression: coincident and
// sub-millisecond-apart starts drift in the QuickTime stts (a duplicate start borrows a unit
// that only repays from later slack), but the uint64 Nero chpl keeps them exact. When the two
// sources agree, the read must take the chpl's exact starts, not the drifted QuickTime ones.
func TestMP4ChapterExactChplStartsOverDriftedQT(t *testing.T) {
	src := readFixture(t, sampleM4B)
	// Three coincident starts at 3 s, then two 1 ms apart - the report's 10/10/10/.001/.002
	// shape, kept inside the fixture's 9 s duration.
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

// TestMP4ChapterCopyWithEndConverges is the M5 regression: a chapter list carrying a
// last-chapter end now round-trips (M4), so re-applying it is a true no-op with byte-identical
// output - three re-copies in a row report "no changes" and never churn the file.
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

// TestMP4ChapterGapPastClampPrefersChpl is the regression for a chapter gap past ~13.25 h: the
// 90 kHz QuickTime stts delta clamps (WarnChapterStartOverflow), corrupting the QuickTime starts,
// but the exact uint64 Nero chpl survives. mergeChapters detects the saturated QuickTime track and
// reads back the exact chpl start rather than the clamped QuickTime value, and the in-memory
// result matches a fresh reparse. (A >13.25 h single-chapter gap is pathological, not a real
// audiobook, but it must not silently drop the exact chapter start.)
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

// TestMP4ChapterCoincidentOpenLastMirrors covers the M4/M1 corner the plan flags as a known
// cosmetic edge: coincident starts with an open last chapter. Whatever last end the reader
// recovers, the in-memory result must equal a fresh reparse (so re-apply idempotency holds),
// and the coincident starts still read back exact from the chpl.
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
