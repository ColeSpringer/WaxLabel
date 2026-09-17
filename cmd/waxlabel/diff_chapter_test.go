package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// diffChaptersChanged runs "diff --json a b" and returns the chapters.changed verdict.
func diffChaptersChanged(t *testing.T, a, b string) bool {
	t.Helper()
	out, _, code := runCLI(t, "--json", "diff", a, b)
	if code > 1 {
		t.Fatalf("diff %s %s exit = %d (>1 is an error), out=%s", a, b, code, out)
	}
	var jd jsonDiff
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("invalid diff JSON: %v\n%s", err, out)
	}
	return jd.Chapters.Changed
}

// TestDiffChaptersReconstructableIdentical: diff ignores reconstructable end differences (Finding 7).
func TestDiffChaptersReconstructableIdentical(t *testing.T) {
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// M4B gapless interior ends reconstruct from next start; FLAC stores start+title only.
	t.Run("m4b copied to flac", func(t *testing.T) {
		dst := copyFixture(t, notagsFLAC)
		if _, _, code := runCLI(t, "copy", sampleM4B, dst); code != 0 {
			t.Fatalf("copy m4b->flac exit = %d, want 0", code)
		}
		if diffChaptersChanged(t, sampleM4B, dst) {
			t.Error("chapters.changed = true; reconstructable interior ends should read as identical")
		}
	})

	// Truncate path: notags.mp3 duration is 2037.551 ms; trailing end must floor to 2037 ms
	// before compare, not use raw float duration.
	t.Run("id3 vs flac, non-whole-ms duration", func(t *testing.T) {
		mp3 := copyFixture(t, notagsMP3)
		flac := copyFixture(t, notagsFLAC)
		for _, f := range []string{mp3, flac} {
			if _, _, code := runCLI(t, "set", f, "--add-chapter", "0=A", "--add-chapter", "0.5=B", "--add-chapter", "1=C"); code != 0 {
				t.Fatalf("set chapters on %s exit = %d, want 0", f, code)
			}
		}
		if diffChaptersChanged(t, mp3, flac) {
			t.Error("chapters.changed = true; a trailing end filled to the ms-floored duration should read as run-to-EOF (Truncate path)")
		}
	})
}

// chapteredDoc writes explicit chapter ends the CLI --add-chapter grammar cannot express.
func chapteredDoc(t *testing.T, fixture string, chs ...wl.Chapter) *wl.Document {
	t.Helper()
	src, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read %s: %v", fixture, err)
	}
	doc, err := wl.Parse(context.Background(), wl.BytesSource(src))
	if err != nil {
		t.Fatalf("parse %s: %v", fixture, err)
	}
	plan, err := doc.Edit().SetChapters(chs...).Prepare()
	if err != nil {
		t.Fatalf("prepare chapters on %s: %v", fixture, err)
	}
	var buf bytes.Buffer
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(src))); err != nil {
		t.Fatalf("execute chapters on %s: %v", fixture, err)
	}
	re, err := wl.Parse(context.Background(), wl.BytesSource(buf.Bytes()))
	if err != nil {
		t.Fatalf("reparse %s: %v", fixture, err)
	}
	return re
}

// TestDiffChaptersGenuineEndStillDiffers: non-reconstructable ends still differ (MP3 CHAP).
func TestDiffChaptersGenuineEndStillDiffers(t *testing.T) {
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// Gapped interior end cannot be inferred from next start.
	t.Run("interior gapped end", func(t *testing.T) {
		gapped := chapteredDoc(t, notagsMP3,
			wl.Chapter{Start: 0, End: 200 * time.Millisecond, Title: "A"}, // ends at 200ms, next starts at 500ms
			wl.Chapter{Start: 500 * time.Millisecond, Title: "B"},
		)
		open := chapteredDoc(t, notagsMP3,
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 500 * time.Millisecond, Title: "B"},
		)
		if !computeDiff(gapped, open).chapsDiffer {
			t.Error("a gapped interior end should still differ from an open one")
		}
	})

	// Trailing end before EOF differs from run-to-EOF.
	t.Run("trailing end before EOF", func(t *testing.T) {
		early := chapteredDoc(t, notagsMP3,
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 500 * time.Millisecond, End: 800 * time.Millisecond, Title: "B"}, // ends at 800ms, EOF ~2037ms
		)
		toEOF := chapteredDoc(t, notagsMP3,
			wl.Chapter{Start: 0, Title: "A"},
			wl.Chapter{Start: 500 * time.Millisecond, Title: "B"}, // open -> filled to the duration on write
		)
		if !computeDiff(early, toEOF).chapsDiffer {
			t.Error("a trailing end before EOF should still differ from one that runs to EOF")
		}
	})
}
