package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// id3v1Block builds a valid 128-byte ID3v1 tag whose title is set (padding zeroed, a numeric year
// and genre so the strict LooksLikeID3v1 gate accepts it).
func id3v1Block(title string) []byte {
	b := make([]byte, 128)
	copy(b[0:3], "TAG")
	copy(b[3:33], title)
	copy(b[93:97], "2020") // year
	b[127] = 255           // genre: unknown
	return b
}

// stackedID3v1MP3 returns an MP3 (from a fixture that carries no trailing legacy tag) with n
// contiguous ID3v1 tags appended, as a re-tagging tool that never removes the old tag leaves.
func stackedID3v1MP3(t *testing.T, n int) []byte {
	t.Helper()
	audio, err := os.ReadFile(td("notags.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	out := audio
	for i := 0; i < n; i++ {
		out = append(out, id3v1Block("Tag")...)
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

// TestLegacyStripRemovesStackedID3v1 checks that a single --legacy strip clears an entire stacked
// ID3v1 run (not just the last block), and that a normal edit preserves the whole run rather than
// truncating an inner block.
func TestLegacyStripRemovesStackedID3v1(t *testing.T) {
	t.Parallel()
	stacked := stackedID3v1MP3(t, 2)
	if got := trailingID3v1Count(stacked); got != 2 {
		t.Fatalf("fixture setup: trailing ID3v1 blocks = %d, want 2", got)
	}

	// The trailing-id3v1 warning reports the run length rather than reading singular.
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

	// A normal edit preserves the full run: the inner block is not truncated by a fixed-128 copy.
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

// flacBodyEmptyVorbis builds a minimal FLAC with an empty Vorbis comment (no canonical tags), so a
// legacy container prepended/appended to it is the only metadata.
func flacBodyEmptyVorbis() []byte {
	le := func(n int) []byte { return []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)} }
	block := func(code byte, last bool, body []byte) []byte {
		h := []byte{code, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
		if last {
			h[0] |= 0x80
		}
		return append(h, body...)
	}
	si := make([]byte, 34) // STREAMINFO: 44100 Hz, 2ch, 16-bit
	si[0], si[1], si[2], si[3] = 0x10, 0x00, 0x10, 0x00
	si[10], si[11] = 0x0A, 0xC4
	si[12] = 0x40 | (1 << 1)
	si[13] = 15 << 4
	vc := append(le(len("test")), "test"...)
	vc = append(vc, le(0)...) // empty comment list
	out := []byte("fLaC")
	out = append(out, block(0, false, si)...)
	out = append(out, block(4, false, vc)...)
	out = append(out, block(1, true, make([]byte, 4))...)
	return append(out, 0xFF, 0xF8) // a little audio
}

// legacyOnlyFLAC builds a FLAC whose only title lives in a trailing ID3v1 tag, so the canonical
// tags view is empty and dump must surface a legacy note rather than a bare "(none)".
func legacyOnlyFLAC() []byte {
	v1 := make([]byte, 128)
	copy(v1[0:3], "TAG")
	copy(v1[3:33], "Trailing Only Title")
	v1[127] = 255
	return append(flacBodyEmptyVorbis(), v1...)
}

// leadingTitlePictureFLAC builds a FLAC whose leading ID3v2 carries both a unique title (a
// legacy-only tag) and a cover (opaque non-tag content), so dump must print both notes.
func leadingTitlePictureFLAC() []byte {
	syncsafe := func(n int) []byte {
		return []byte{byte(n>>21) & 0x7f, byte(n>>14) & 0x7f, byte(n>>7) & 0x7f, byte(n) & 0x7f}
	}
	frame := func(id string, body []byte) []byte {
		out := append([]byte(id), byte(len(body)>>24), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)), 0, 0)
		return append(out, body...)
	}
	tbody := append([]byte{0}, "Lead Title"...) // Latin-1 encoding byte + text
	abody := []byte{0}                          // Latin-1 encoding byte
	abody = append(abody, "image/png"...)
	abody = append(abody, 0)                        // MIME terminator
	abody = append(abody, 3)                        // picture type: front cover
	abody = append(abody, 0)                        // empty description terminator
	abody = append(abody, "\x89PNG dummy cover"...) // image data (unvalidated by the decoder)
	body := append(frame("TIT2", tbody), frame("APIC", abody)...)
	id3 := append([]byte{'I', 'D', '3', 3, 0, 0}, syncsafe(len(body))...)
	id3 = append(id3, body...)
	return append(id3, flacBodyEmptyVorbis()...)
}

// TestDumpAndLintSurfaceLegacyOnly drives the real CLI: dump prints the legacy note and lint
// reports legacy-only-tags for a file whose only tag lives in a legacy container.
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

// TestDumpShowsBothLegacyNotes drives the CLI on a file that has both a legacy-only tag and opaque
// legacy content, confirming neither dump note shadows the other and both lint findings fire.
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
