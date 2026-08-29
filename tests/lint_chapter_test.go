package waxlabel_test

import (
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/vorbis"
)

// TestLintChaptersGatedOnKnownDuration: the past-duration rule needs a duration to compare
// against. A file whose duration reads 0 - a header-only or truncated stream - would
// otherwise have every chapter flagged as beyond 0:00, so the linter gates on a known
// non-zero duration exactly as the editor does. The duplicate rule needs no duration and
// still applies, which is what keeps the gate from silencing the whole helper.
func TestLintChaptersGatedOnKnownDuration(t *testing.T) {
	// A comment block plus chapter comments and no audio frames: the STREAMINFO carries no
	// sample count, so Duration() is 0 while the chapters are real.
	data := flacWithCommentBlock([]vorbis.Comment{
		{Name: "CHAPTER001", Value: "00:00:05.000"},
		{Name: "CHAPTER001NAME", Value: "A"},
		{Name: "CHAPTER002", Value: "00:00:05.000"},
		{Name: "CHAPTER002NAME", Value: "B"},
	})
	doc := mustParseBytes(t, data)
	if got := doc.Properties().Duration(); got != 0 {
		t.Fatalf("setup: want an unknown (0) duration to exercise the gate, got %v", got)
	}
	if got := len(doc.Chapters()); got != 2 {
		t.Fatalf("setup: want 2 chapters, got %d", got)
	}

	var codes []string
	for _, f := range doc.Lint() {
		codes = append(codes, f.Code)
		if f.Code == "chapter-past-duration" {
			t.Errorf("an unknown duration must not flag a chapter as past the end: %v", f)
		}
	}
	// The duplicate rule is independent of duration, so it still fires: without this the
	// test would pass even if lintChapters returned nothing at all.
	if !slices.Contains(codes, "duplicate-chapter") {
		t.Errorf("two chapters at the same start should lint as duplicate-chapter, got %v", codes)
	}
}

// TestLintChaptersPastKnownDuration is the positive half: with a real duration, a chapter
// beyond it is reported. Together with the gate test above, removing the duration guard
// changes one of the two outcomes.
func TestLintChaptersPastKnownDuration(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	plan, err := mustParseBytes(t, src).Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "A"},
		wl.Chapter{Start: time.Hour, Title: "Late"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	doc := mustParseBytes(t, applyToBytes(t, src, plan))
	if doc.Properties().Duration() <= 0 {
		t.Fatal("setup: the fixture should report a real duration")
	}
	var codes []string
	for _, f := range doc.Lint() {
		codes = append(codes, f.Code)
	}
	if !slices.Contains(codes, "chapter-past-duration") {
		t.Errorf("a chapter an hour into a one-second file should be reported, got %v", codes)
	}
}
