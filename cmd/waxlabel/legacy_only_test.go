package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
