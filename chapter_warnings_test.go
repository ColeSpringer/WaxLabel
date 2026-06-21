package waxlabel

import (
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
)

// chap is a keyed-field Chapter constructor for the tests (Chapter requires keyed
// fields).
func chap(start time.Duration) core.Chapter { return core.Chapter{Start: start} }

// TestAppendChapterWarnings exercises the chapter sanity-warning logic directly,
// including the cases the public API cannot easily fixture: the duration-0 guard
// (no chapter-capable zero-duration fixture exists), the duplicate-start run dedup,
// and the scoping to genuinely-new chapters (so a pre-existing chapter merged into
// the list is not flagged). The end-to-end wiring is covered by the public
// TestChapterWarningsSurface.
func TestAppendChapterWarnings(t *testing.T) {
	const dur = 10 * time.Second

	// A newly-added chapter beyond the file end is flagged; one within it is not.
	// (No base chapters here, so every chapter is "new".)
	ws := appendChapterWarnings(nil, []core.Chapter{chap(time.Second), chap(dur + time.Hour)}, nil, dur)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 1 {
		t.Errorf("past-duration warnings = %d, want 1; got %v", n, ws)
	}

	// All chapters within the duration: no past-duration warning.
	ws = appendChapterWarnings(nil, []core.Chapter{chap(time.Second), chap(5 * time.Second)}, nil, dur)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 0 {
		t.Errorf("in-bounds chapters should not warn, got %d: %v", n, ws)
	}

	// Duration 0 (a truncated/header-only file): the past-duration check is skipped
	// entirely, so no chapter is spuriously flagged as beyond 0:00.
	ws = appendChapterWarnings(nil, []core.Chapter{chap(time.Second), chap(time.Hour)}, nil, 0)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 0 {
		t.Errorf("duration-0 should suppress past-duration warnings, got %d: %v", n, ws)
	}

	// A run of equal starts (all new) is reported once, not once per adjacent pair.
	ws = appendChapterWarnings(nil, []core.Chapter{
		chap(time.Second), chap(time.Second), chap(time.Second), chap(2 * time.Second),
	}, nil, dur)
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 1 {
		t.Errorf("a run of equal starts should warn once, got %d: %v", n, ws)
	}

	// Two distinct collisions are reported once each.
	ws = appendChapterWarnings(nil, []core.Chapter{
		chap(time.Second), chap(time.Second), chap(2 * time.Second), chap(2 * time.Second),
	}, nil, dur)
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 2 {
		t.Errorf("two distinct collisions should warn twice, got %d: %v", n, ws)
	}

	// Scoping: a pre-existing (base) chapter that is past the duration or shares a
	// start with another pre-existing one is NOT flagged when the edit only adds an
	// unrelated, valid chapter. This is the --add-chapter case: base chapters merge
	// into the list but are not the user's authored input.
	base := []core.Chapter{chap(dur + time.Hour), chap(2 * time.Second), chap(2 * time.Second)}
	merged := append(append([]core.Chapter{}, base...), chap(time.Second)) // + one valid new chapter
	ws = appendChapterWarnings(nil, merged, base, dur)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 0 {
		t.Errorf("a pre-existing past-duration chapter should not be flagged, got %d: %v", n, ws)
	}
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 0 {
		t.Errorf("a pre-existing duplicate-start pair should not be flagged, got %d: %v", n, ws)
	}

	// But a collision the new chapter itself causes IS flagged, even against a
	// pre-existing chapter. The new chapter is distinct by content (a different
	// title), so it is genuinely "new" rather than an identical no-op duplicate.
	base = []core.Chapter{{Start: 3 * time.Second, Title: "Old"}}
	merged = []core.Chapter{{Start: 3 * time.Second, Title: "Old"}, {Start: 3 * time.Second, Title: "New"}}
	ws = appendChapterWarnings(nil, merged, base, dur)
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 1 {
		t.Errorf("a new chapter colliding with a pre-existing start should warn once, got %d: %v", n, ws)
	}
}

func countWarn(ws []core.Warning, code core.WarningCode) int {
	n := 0
	for _, w := range ws {
		if w.Code == code {
			n++
		}
	}
	return n
}
