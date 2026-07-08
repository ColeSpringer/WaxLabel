package core

import (
	"testing"
	"time"
)

// TestReconcileChapterOverlaps covers the helper: an edit-introduced overlap truncates the
// preceding chapter's stale end to the next start, while a file's own pre-existing on-disk
// overlap (both sides in base) is left verbatim.
func TestReconcileChapterOverlaps(t *testing.T) {
	ms := time.Millisecond
	ch := func(start, end time.Duration, title string) Chapter {
		return Chapter{Start: start * ms, End: end * ms, Title: title}
	}

	t.Run("inserted marker truncates the preceding stale end", func(t *testing.T) {
		base := []Chapter{ch(0, 500, "A"), ch(500, 1500, "B"), ch(1500, 2000, "C")}
		// Start-sorted list after inserting a start-only marker at 700 between B and C.
		chs := []Chapter{ch(0, 500, "A"), ch(500, 1500, "B"), {Start: 700 * ms, Title: "M"}, ch(1500, 2000, "C")}
		if !ReconcileChapterOverlaps(chs, base) {
			t.Fatal("expected a reconciliation (B's end overlaps the marker)")
		}
		if chs[1].End != 700*ms {
			t.Errorf("B.End = %v, want 700ms (truncated to the marker start)", chs[1].End)
		}
		if chs[0] != ch(0, 500, "A") || chs[3] != ch(1500, 2000, "C") {
			t.Errorf("unrelated chapters changed: %+v", chs)
		}
		if chs[2].End != 0 {
			t.Errorf("the start-only marker gained an end: %+v", chs[2])
		}
	})

	t.Run("contiguous chapters are a no-op", func(t *testing.T) {
		base := []Chapter{ch(0, 500, "A"), ch(500, 1000, "B")}
		chs := []Chapter{ch(0, 500, "A"), ch(500, 1000, "B")} // End == next.Start, not past it
		if ReconcileChapterOverlaps(chs, base) {
			t.Error("contiguous chapters must not reconcile")
		}
	})

	t.Run("pre-existing on-disk overlap (both in base) is preserved", func(t *testing.T) {
		// Both chapters overlap and both are in base (an external tool wrote them): leave verbatim.
		base := []Chapter{ch(0, 1000, "A"), ch(500, 1500, "B")}
		chs := []Chapter{ch(0, 1000, "A"), ch(500, 1500, "B")}
		if ReconcileChapterOverlaps(chs, base) {
			t.Error("a pre-existing overlap with both sides in base must not be reconciled")
		}
		if chs[0].End != 1000*ms {
			t.Errorf("A.End = %v, want 1000ms preserved verbatim", chs[0].End)
		}
	})

	t.Run("retitling a chapter does not reconcile a pre-existing overlap", func(t *testing.T) {
		// A pre-existing overlap where only B's TITLE changed - no timing changed - must stay
		// verbatim. Keying on the whole struct would treat retitled B as "new" and wrongly
		// shorten A's end; keying on the timing values (both End 1000 and Start 500 are on base)
		// leaves it alone.
		base := []Chapter{ch(0, 1000, "A"), ch(500, 1500, "B")}
		chs := []Chapter{ch(0, 1000, "A"), ch(500, 1500, "B-retitled")}
		if ReconcileChapterOverlaps(chs, base) {
			t.Error("a title-only edit must not reconcile a pre-existing overlap")
		}
		if chs[0].End != 1000*ms {
			t.Errorf("A.End = %v, want 1000ms (unchanged by a neighbor's retitle)", chs[0].End)
		}
	})

	t.Run("unsorted input never drives End below Start", func(t *testing.T) {
		// A caller that forgets to sort must not get End<Start corruption: the next start (200)
		// is below chs[0].Start (500), so the truncation is skipped.
		chs := []Chapter{ch(500, 1000, "A"), ch(200, 300, "B")}
		ReconcileChapterOverlaps(chs, nil)
		if chs[0].End < chs[0].Start {
			t.Errorf("unsorted reconcile produced End<Start: %+v", chs[0])
		}
	})

	t.Run("edited base end overshooting a neighbor reads as new and reconciles", func(t *testing.T) {
		base := []Chapter{ch(0, 500, "A"), ch(500, 1000, "B")}
		// A library caller lengthened A's end past B's start; the edited A differs from base.
		chs := []Chapter{ch(0, 800, "A"), ch(500, 1000, "B")}
		if !ReconcileChapterOverlaps(chs, base) {
			t.Fatal("an edited end that overshoots the next start should reconcile")
		}
		if chs[0].End != 500*ms {
			t.Errorf("A.End = %v, want 500ms (truncated to B.Start)", chs[0].End)
		}
	})

	t.Run("short list is a safe no-op", func(t *testing.T) {
		chs := []Chapter{ch(0, 500, "A")}
		if ReconcileChapterOverlaps(chs, nil) {
			t.Error("a single chapter cannot overlap")
		}
	})
}
