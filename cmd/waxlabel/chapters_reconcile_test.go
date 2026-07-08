package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestChapterOverlapReconciledWarningSurface is a CLI regression guard: inserting a start-only
// marker inside an already-ended chapter reconciles the stale overlap and surfaces the accurate
// chapter-overlap-reconciled note - and, for MP4, replaces the previously-spurious
// chapter-metadata-dropped warning. No duplicate-chapter or chapter-past-duration is invented.
func TestChapterOverlapReconciledWarningSurface(t *testing.T) {
	t.Parallel()
	chaptersMKA := filepath.Join("..", "..", "testdata", "chapters.mka")

	cases := []struct {
		name   string
		file   string
		marker string // an insert time strictly inside the first ended chapter
	}{
		{"mp4", sampleM4B, "1.5=Marker"},        // Opening Credits 0-3s -> overlaps a 1.5s marker
		{"matroska", chaptersMKA, "0.1=Marker"}, // Intro 0-0.2s -> overlaps a 0.1s marker
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, _, code := runCLI(t, "plan", copyFixture(t, c.file), "--add-chapter", c.marker)
			if code != 0 {
				t.Fatalf("plan --add-chapter exit = %d, want 0:\n%s", code, out)
			}
			if !strings.Contains(out, "chapter-overlap-reconciled") {
				t.Errorf("missing the chapter-overlap-reconciled note:\n%s", out)
			}
			for _, spurious := range []string{"chapter-metadata-dropped", "duplicate-chapter", "chapter-past-duration"} {
				if strings.Contains(out, spurious) {
					t.Errorf("unexpected %q warning after reconciling an inserted overlap:\n%s", spurious, out)
				}
			}
		})
	}
}
