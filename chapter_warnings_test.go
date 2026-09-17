package waxlabel

import (
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
)

// chap builds a Chapter with only Start set.
func chap(start time.Duration) core.Chapter { return core.Chapter{Start: start} }

// TestAppendChapterWarnings covers duration-0, duplicate-start dedup, and
// scoping to new chapters (public API fixtures miss these).
func TestAppendChapterWarnings(t *testing.T) {
	const dur = 10 * time.Second

	// New chapter past EOF flagged; in-bounds not.
	ws := appendChapterWarnings(nil, []core.Chapter{chap(time.Second), chap(dur + time.Hour)}, nil, dur)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 1 {
		t.Errorf("past-duration warnings = %d, want 1; got %v", n, ws)
	}

	// All in-bounds: no past-duration warning.
	ws = appendChapterWarnings(nil, []core.Chapter{chap(time.Second), chap(5 * time.Second)}, nil, dur)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 0 {
		t.Errorf("in-bounds chapters should not warn, got %d: %v", n, ws)
	}

	// Duration 0: skip past-duration check.
	ws = appendChapterWarnings(nil, []core.Chapter{chap(time.Second), chap(time.Hour)}, nil, 0)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 0 {
		t.Errorf("duration-0 should suppress past-duration warnings, got %d: %v", n, ws)
	}

	// Equal-start run warns once, not per adjacent pair.
	ws = appendChapterWarnings(nil, []core.Chapter{
		chap(time.Second), chap(time.Second), chap(time.Second), chap(2 * time.Second),
	}, nil, dur)
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 1 {
		t.Errorf("a run of equal starts should warn once, got %d: %v", n, ws)
	}

	// Two distinct collisions warn once each.
	ws = appendChapterWarnings(nil, []core.Chapter{
		chap(time.Second), chap(time.Second), chap(2 * time.Second), chap(2 * time.Second),
	}, nil, dur)
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 2 {
		t.Errorf("two distinct collisions should warn twice, got %d: %v", n, ws)
	}

	// Pre-existing past-duration / duplicate starts not flagged on --add-chapter.
	base := []core.Chapter{chap(dur + time.Hour), chap(2 * time.Second), chap(2 * time.Second)}
	merged := append(append([]core.Chapter{}, base...), chap(time.Second)) // + one valid new chapter
	ws = appendChapterWarnings(nil, merged, base, dur)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 0 {
		t.Errorf("a pre-existing past-duration chapter should not be flagged, got %d: %v", n, ws)
	}
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 0 {
		t.Errorf("a pre-existing duplicate-start pair should not be flagged, got %d: %v", n, ws)
	}

	// New chapter colliding with pre-existing start is flagged.
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
