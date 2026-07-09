package waxlabel

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/tag"
)

func planHasWarn(p *Plan, code WarningCode) bool {
	for _, w := range p.Report().Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// assertNonOverlapping checks that, in start-order, no chapter's explicit end runs past the
// next chapter's start.
func assertNonOverlapping(t *testing.T, chs []Chapter) {
	t.Helper()
	sorted := slices.Clone(chs)
	slices.SortStableFunc(sorted, func(a, b Chapter) int {
		return int(a.Start - b.Start)
	})
	for i := 0; i+1 < len(sorted); i++ {
		if sorted[i].End > 0 && sorted[i].End > sorted[i+1].Start {
			t.Errorf("chapter %d (end %v) overlaps chapter %d (start %v): %+v",
				i, sorted[i].End, i+1, sorted[i+1].Start, sorted)
		}
	}
}

// TestChapterOverlapReconciledID3EndToEnd is the ID3 end-to-end regression: inserting a
// start-only marker between already-ended ID3 CHAP chapters truncates the preceding stale end
// (so the written frames are non-overlapping, not silently overlapping), surfaces the reconcile
// note, and invents no duplicate-chapter/chapter-past-duration.
func TestChapterOverlapReconciledID3EndToEnd(t *testing.T) {
	ms := time.Millisecond
	ctx := context.Background()

	// Author ended chapters [0-500][500-1500][1500-2000] into an MP3 (notags.mp3 is 2.04s,
	// so every chapter is within the duration).
	base0, err := ParseFile(ctx, "testdata/notags.mp3")
	if err != nil {
		t.Fatalf("parse notags.mp3: %v", err)
	}
	ended := []Chapter{
		{Start: 0, End: 500 * ms, Title: "A"},
		{Start: 500 * ms, End: 1500 * ms, Title: "B"},
		{Start: 1500 * ms, End: 2000 * ms, Title: "C"},
	}
	endedPath := filepath.Join(t.TempDir(), "ended.mp3")
	plan0, err := base0.Edit().SetChapters(ended...).Prepare()
	if err != nil {
		t.Fatalf("prepare ended chapters: %v", err)
	}
	if _, _, err := plan0.Execute(ctx, SaveAsFile(endedPath)); err != nil {
		t.Fatalf("write ended chapters: %v", err)
	}

	// Insert a start-only marker at 700 between B and C.
	base, err := ParseFile(ctx, endedPath)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if n := len(base.Chapters()); n != 3 {
		t.Fatalf("setup: %d chapters, want 3", n)
	}
	merged := append(slices.Clone(base.Chapters()), Chapter{Start: 700 * ms, Title: "M"})
	plan, err := base.Edit().SetChapters(merged...).Prepare()
	if err != nil {
		t.Fatalf("prepare insert: %v", err)
	}

	if !planHasWarn(plan, WarnChapterOverlapReconciled) {
		t.Errorf("missing chapter-overlap-reconciled note; warnings=%v", plan.Report().Warnings)
	}
	for _, code := range []WarningCode{WarnDuplicateChapter, WarnChapterPastDuration} {
		if planHasWarn(plan, code) {
			t.Errorf("unexpected %v warning after an inserted, in-bounds marker; warnings=%v", code, plan.Report().Warnings)
		}
	}

	outPath := filepath.Join(t.TempDir(), "out.mp3")
	if _, _, err := plan.Execute(ctx, SaveAsFile(outPath)); err != nil {
		t.Fatalf("write inserted chapters: %v", err)
	}
	got, err := ParseFile(ctx, outPath)
	if err != nil {
		t.Fatalf("re-parse output: %v", err)
	}
	assertNonOverlapping(t, got.Chapters())
	// The B chapter (start 500) now ends at the marker start (700), not its stale 1500.
	var b *Chapter
	for i := range got.Chapters() {
		if c := got.Chapters()[i]; c.Start == 500*ms {
			b = &c
		}
	}
	if b == nil || b.End != 700*ms {
		t.Errorf("B chapter = %+v, want End 700ms (truncated to the marker start)", b)
	}
}

// TestCoincidentStartChapterEndPreservedRoundTrip covers the reconcile-then-serialize path for two
// chapters that share a start where the earlier one carries a real authored end past it. The
// reconcile must not collapse that end onto the shared start (the strict next > Start guard), and
// the writer must then serialize the surviving end rather than an invalid interval (the End > Start
// guard). It runs on the two container families with an explicit per-chapter end field, ID3 (mp3)
// and Matroska (mka). MP4 is deliberately excluded: it stores only start+title for interior
// chapters and derives their ends from the next start, dropping any interior end (with a
// chapter-metadata-dropped warning) whichever way the reconcile grades it, so it cannot represent
// this property. notags.mka is 1s long, so every time here is comfortably in bounds.
func TestCoincidentStartChapterEndPreservedRoundTrip(t *testing.T) {
	ms := time.Millisecond
	ctx := context.Background()

	authored := []Chapter{
		{Start: 0, Title: "Pre"},
		{Start: 500 * ms, End: 600 * ms, Title: "A"}, // authored end past the shared 500ms start
		{Start: 500 * ms, Title: "B"},                // coincident start with A
	}

	for _, tc := range []struct{ name, src, ext string }{
		{"id3", "testdata/notags.mp3", ".mp3"},
		{"matroska", "testdata/notags.mka", ".mka"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			base, err := ParseFile(ctx, tc.src)
			if err != nil {
				t.Fatalf("parse %s: %v", tc.src, err)
			}
			plan, err := base.Edit().SetChapters(authored...).Prepare()
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			out := filepath.Join(t.TempDir(), "out"+tc.ext)
			if _, _, err := plan.Execute(ctx, SaveAsFile(out)); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := ParseFile(ctx, out)
			if err != nil {
				t.Fatalf("re-parse: %v", err)
			}
			var a *Chapter
			for i := range got.Chapters() {
				if c := got.Chapters()[i]; c.Title == "A" {
					a = &c
				}
			}
			if a == nil {
				t.Fatalf("chapter A not found after round-trip; chapters=%+v", got.Chapters())
			}
			// The old next >= Start guard collapsed A onto the shared start; the fix keeps End=600ms.
			if a.End != 600*ms {
				t.Errorf("chapter A End = %v, want 600ms preserved end to end", a.End)
			}
		})
	}
}

// TestChapterPreExistingOverlapNotReconciledOnTagEdit is the control: a file whose chapters
// already overlap on disk (both sides pre-existing) keeps them byte-identical through an
// unrelated tag edit - reconciliation is scoped to edit-introduced overlaps and gated on a
// chapter edit, so a tag-only edit never touches them.
func TestChapterPreExistingOverlapNotReconciledOnTagEdit(t *testing.T) {
	ms := time.Millisecond
	ctx := context.Background()

	// Fixture a pre-existing on-disk overlap via a carried (faithful-transfer) write, which
	// bypasses reconciliation - the only way to land overlapping chapters on disk.
	base0, err := ParseFile(ctx, "testdata/notags.mp3")
	if err != nil {
		t.Fatalf("parse notags.mp3: %v", err)
	}
	overlapping := []Chapter{
		{Start: 0, End: 1000 * ms, Title: "A"},
		{Start: 500 * ms, End: 1500 * ms, Title: "B"}, // A's end (1000) overshoots B's start (500)
	}
	ed := base0.Edit()
	ed.carried = true
	ed.SetChapters(overlapping...)
	plan0, err := ed.Prepare()
	if err != nil {
		t.Fatalf("prepare carried overlap: %v", err)
	}
	if planHasWarn(plan0, WarnChapterOverlapReconciled) {
		t.Fatal("a carried write must not reconcile (it preserves the source faithfully)")
	}
	overlapPath := filepath.Join(t.TempDir(), "overlap.mp3")
	if _, _, err := plan0.Execute(ctx, SaveAsFile(overlapPath)); err != nil {
		t.Fatalf("write overlapping chapters: %v", err)
	}

	base, err := ParseFile(ctx, overlapPath)
	if err != nil {
		t.Fatalf("re-parse overlap: %v", err)
	}
	before := slices.Clone(base.Chapters())
	// Sanity: the overlap really is on disk.
	assertOverlapping(t, before)

	// An unrelated tag edit must leave the chapters byte-identical.
	plan, err := base.Edit().Set(tag.Title, "Unrelated").Prepare()
	if err != nil {
		t.Fatalf("prepare tag edit: %v", err)
	}
	if planHasWarn(plan, WarnChapterOverlapReconciled) {
		t.Errorf("a tag-only edit must not reconcile chapters; warnings=%v", plan.Report().Warnings)
	}
	tagEditPath := filepath.Join(t.TempDir(), "tagedit.mp3")
	if _, _, err := plan.Execute(ctx, SaveAsFile(tagEditPath)); err != nil {
		t.Fatalf("write tag edit: %v", err)
	}
	after, err := ParseFile(ctx, tagEditPath)
	if err != nil {
		t.Fatalf("re-parse tag edit: %v", err)
	}
	if !slices.Equal(before, after.Chapters()) {
		t.Errorf("tag edit changed the chapters:\n before %+v\n after  %+v", before, after.Chapters())
	}
}

// assertOverlapping is the setup sanity check for the control: at least one chapter's end
// overshoots the next start, confirming the fixture actually has an on-disk overlap.
func assertOverlapping(t *testing.T, chs []Chapter) {
	t.Helper()
	sorted := slices.Clone(chs)
	slices.SortStableFunc(sorted, func(a, b Chapter) int { return int(a.Start - b.Start) })
	for i := 0; i+1 < len(sorted); i++ {
		if sorted[i].End > 0 && sorted[i].End > sorted[i+1].Start {
			return
		}
	}
	t.Fatalf("setup: expected an on-disk overlap, got %+v", sorted)
}
