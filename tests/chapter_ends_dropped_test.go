package waxlabel_test

import (
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// TestChapterEndsDroppedWarning checks the Matroska/WebM warning for replacing ended
// chapters with open-ended ones. MP4 is exempt because its ends are inferred, a bare
// clear is a deletion rather than a rewrite, and faithful transfer is suppressed.
func TestChapterEndsDroppedWarning(t *testing.T) {
	mkaWithEnds := readFixture(t, chaptersMKA) // 3 chapters, each with an explicit end

	// 1. Matroska open-ended rewrite warns.
	rewrite := prepareWith(t, mkaWithEnds, func(e *wl.Editor) {
		e.SetChapters(
			wl.Chapter{Start: 0, Title: "Alpha"},
			wl.Chapter{Start: 100 * time.Millisecond, Title: "Beta"},
		)
	})
	if !planWarns(t, rewrite, wl.WarnChapterEndsDropped) {
		t.Errorf("Matroska open-ended chapter rewrite should warn chapter-ends-dropped; got %v", rewrite.Report().Warnings)
	}

	// 2. A bare clear is a full deletion, not an open-ended rewrite.
	cleared := prepareWith(t, mkaWithEnds, func(e *wl.Editor) { e.ClearChapters() })
	if planWarns(t, cleared, wl.WarnChapterEndsDropped) {
		t.Errorf("a bare --clear-chapters must not warn chapter-ends-dropped; got %v", cleared.Report().Warnings)
	}

	// 3. MP4 infers each end from the next chapter's start, so it is exempt.
	m4b := readFixture(t, sampleM4B)
	mp4rewrite := prepareWith(t, m4b, func(e *wl.Editor) {
		e.SetChapters(
			wl.Chapter{Start: 0, Title: "Alpha"},
			wl.Chapter{Start: 1 * time.Second, Title: "Beta"},
		)
	})
	if planWarns(t, mp4rewrite, wl.WarnChapterEndsDropped) {
		t.Errorf("MP4 chapter rewrite must not warn chapter-ends-dropped (ends are inferred); got %v", mp4rewrite.Report().Warnings)
	}

	// 4. Faithful transfer is suppressed by the carried flag. The user did not author
	// these chapters in the edit path, so this warning should not appear.
	endless := buildMatroskaCh("matroska", "Src", mkEl(idChapters, mkEdition(true, nil,
		mkAtom(1, 0, 0, "Alpha"),
		mkAtom(2, uint64(100*time.Millisecond), 0, "Beta"),
	)), nil)
	src := mustParseBytes(t, endless)
	if len(src.Chapters()) != 2 {
		t.Fatalf("synth source should have 2 chapters, got %d", len(src.Chapters()))
	}
	dst := mustParseBytes(t, mkaWithEnds)
	plan, _, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if planWarns(t, plan, wl.WarnChapterEndsDropped) {
		t.Errorf("a faithful copy carrying end-less chapters must not warn chapter-ends-dropped; got %v", plan.Report().Warnings)
	}
}
