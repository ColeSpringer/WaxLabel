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

// TestDiffChaptersReconstructableIdentical checks the Finding 7 fix end-to-end: diff no longer
// reports chapters as differing when the only difference is an end a codec would itself
// reconstruct - matching how copy grades such an end as reconstructable rather than lossy.
func TestDiffChaptersReconstructableIdentical(t *testing.T) {
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// The report's motivating case: copy an M4B's chapters into a FLAC, then diff the two. The
	// M4B carries gapless interior ends (each end == the next start) and an open trailing end;
	// FLAC (CHAPTERxxx) stores start+title only, so it reads all ends open. Those interior ends
	// are reconstructable, so the chapters must diff as identical.
	t.Run("m4b copied to flac", func(t *testing.T) {
		dst := copyFixture(t, notagsFLAC)
		if _, _, code := runCLI(t, "copy", sampleM4B, dst); code != 0 {
			t.Fatalf("copy m4b->flac exit = %d, want 0", code)
		}
		if diffChaptersChanged(t, sampleM4B, dst) {
			t.Error("chapters.changed = true; reconstructable interior ends should read as identical")
		}
	})

	// The Truncate-path case: author the same chapters on an ID3 file (MP3) and a FLAC. The
	// MP3's trailing open chapter is filled to the media duration floored to ms, while FLAC
	// stores no end. notags.mp3's duration is 2037.551020 ms - deliberately NOT a whole
	// millisecond - so normalizing the trailing end must truncate the duration to ms
	// (2037 ms) before the >= comparison; a naive ">= duration" would leave 2037 ms < 2037.551
	// ms unnormalized and wrongly report the chapters as differing. A whole-ms fixture would
	// pass even with that bug, hiding the regression.
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

// chapteredDoc authors chs on fixture, writes, and reparses, returning the resulting document.
// It is used to build documents carrying explicit chapter ends the CLI's start=title
// --add-chapter grammar cannot express (a gapped interior end, an early-ending trailing end).
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

// TestDiffChaptersGenuineEndStillDiffers checks Finding 7 did not blind diff to real end
// differences: an end a codec cannot reconstruct (a gapped interior end, or a trailing end
// that stops before EOF) must still make the chapters differ, matching copy's "lossy" grade.
// Both cases use MP3, whose ID3 CHAP store preserves explicit ends.
func TestDiffChaptersGenuineEndStillDiffers(t *testing.T) {
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// An interior chapter that ends before the next starts (a real gap, not gapless) cannot be
	// inferred from the next start, so it differs from the same chapters left open.
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

	// A trailing chapter that ends before EOF carries information a run-to-EOF end does not, so
	// it differs from the same chapters whose trailing end runs to the media duration.
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
