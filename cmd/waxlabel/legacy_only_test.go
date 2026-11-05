package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// id3v1Block builds a valid 128-byte ID3v1 tag. Parameterized fields share one offset table.
func id3v1Block(title, album, comment string, genre byte) []byte {
	return testfile.ID3v1(title, "", album, "2020", comment, genre)
}

// stackedID3v1MP3 appends n contiguous ID3v1 tags to notags.mp3.
func stackedID3v1MP3(t *testing.T, n int) []byte {
	t.Helper()
	audio, err := os.ReadFile(td("notags.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	out := audio
	for range n {
		out = append(out, id3v1Block("Tag", "", "", 255)...)
	}
	return out
}

// trailingID3v1Count counts contiguous 128-byte "TAG" blocks at the end of b.
func trailingID3v1Count(b []byte) int {
	n := 0
	for len(b) >= 128 && bytes.Equal(b[len(b)-128:len(b)-128+3], []byte("TAG")) {
		n++
		b = b[:len(b)-128]
	}
	return n
}

// TestLegacyStripRemovesStackedID3v1: --legacy strip clears full stacked run; normal edit preserves it.
func TestLegacyStripRemovesStackedID3v1(t *testing.T) {
	t.Parallel()
	stacked := stackedID3v1MP3(t, 2)
	if got := trailingID3v1Count(stacked); got != 2 {
		t.Fatalf("fixture setup: trailing ID3v1 blocks = %d, want 2", got)
	}

	// Dump reports stacked run length, not singular.
	raw := filepath.Join(t.TempDir(), "raw.mp3")
	if err := os.WriteFile(raw, stacked, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := runCLI(t, "dump", raw); !strings.Contains(out, "2 stacked legacy ID3v1") {
		t.Errorf("dump should report the stacked ID3v1 run length, not a singular message; got:\n%s", out)
	}

	// One --legacy strip removes both blocks.
	strip := filepath.Join(t.TempDir(), "strip.mp3")
	if err := os.WriteFile(strip, stacked, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errb, code := runCLI(t, "set", strip, "--legacy", "strip", "--set", "TITLE=X"); code != 0 {
		t.Fatalf("legacy strip exit = %d\n%s", code, errb)
	}
	stripped, err := os.ReadFile(strip)
	if err != nil {
		t.Fatal(err)
	}
	if got := trailingID3v1Count(stripped); got != 0 {
		t.Errorf("after one --legacy strip, trailing ID3v1 blocks = %d, want 0", got)
	}

	// Normal edit preserves full run (no inner-block truncation).
	norm := filepath.Join(t.TempDir(), "norm.mp3")
	if err := os.WriteFile(norm, stacked, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errb, code := runCLI(t, "set", norm, "--set", "TITLE=Y"); code != 0 {
		t.Fatalf("normal edit exit = %d\n%s", code, errb)
	}
	edited, err := os.ReadFile(norm)
	if err != nil {
		t.Fatal(err)
	}
	if got := trailingID3v1Count(edited); got != 2 {
		t.Errorf("after a normal edit, preserved trailing ID3v1 blocks = %d, want 2 (no inner-block truncation)", got)
	}
}

// legacyOnlyFLAC: title only in trailing ID3v1; canonical tags empty.
func legacyOnlyFLAC() []byte {
	return slices.Concat(testfile.TaggedFLAC("test"), testfile.ID3v1("Trailing Only Title", "", "", "", "", 255))
}

// leadingTitlePictureFLAC: leading ID3v2 has legacy-only title plus cover.
func leadingTitlePictureFLAC() []byte {
	id3 := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Lead Title"),
		testfile.FrontCover("image/png", []byte("\x89PNG dummy cover"))) // cover bytes the decoder does not validate
	return slices.Concat(id3, testfile.TaggedFLAC("test"))
}

// TestDumpAndLintSurfaceLegacyOnly: dump legacy note and lint legacy-only-tags for legacy-only tag.
func TestDumpAndLintSurfaceLegacyOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.flac")
	if err := os.WriteFile(path, legacyOnlyFLAC(), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, code := runCLI(t, "dump", path)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if !strings.Contains(out, "only in a legacy container") {
		t.Errorf("dump missing the legacy note:\n%s", out)
	}

	lintOut, _, _ := runCLI(t, "lint", path)
	if !strings.Contains(lintOut, "legacy-only-tags") {
		t.Errorf("lint missing legacy-only-tags finding:\n%s", lintOut)
	}
}

// TestDumpShowsBothLegacyNotes: legacy-only tag and opaque legacy content both surface.
func TestDumpShowsBothLegacyNotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "both.flac")
	if err := os.WriteFile(path, leadingTitlePictureFLAC(), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, code := runCLI(t, "dump", path)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if !strings.Contains(out, "only in a legacy container") {
		t.Errorf("dump missing the legacy-only-tags note:\n%s", out)
	}
	if !strings.Contains(out, "non-tag metadata not shown") {
		t.Errorf("dump missing the opaque-content note (shadowed?):\n%s", out)
	}

	lintOut, _, _ := runCLI(t, "lint", path)
	for _, code := range []string{"legacy-only-tags", "legacy-opaque-content"} {
		if !strings.Contains(lintOut, code) {
			t.Errorf("lint missing %s finding:\n%s", code, lintOut)
		}
	}
}
