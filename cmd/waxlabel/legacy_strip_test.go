package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeLegacyOnlyMP3: ID3v2 has TITLE/ARTIST; ID3v1 trailer holds legacy-only fields.
func writeLegacyOnlyMP3(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacyonly.mp3")
	if _, _, code := runCLI(t, "set", td("notags.mp3"), "--set", "TITLE=T2", "--set", "ARTIST=A2", "-o", path); code != 0 {
		t.Fatalf("building the fixture failed with exit %d", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, id3v1Block("", "Legacy Album", "legacy comment", 17)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSetLegacyStripWarnsAndStrictRefuses: --legacy strip warns dropped legacy-only keys; --strict refuses.
func TestSetLegacyStripWarnsAndStrictRefuses(t *testing.T) {
	t.Parallel()
	path := writeLegacyOnlyMP3(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out, _, code := runCLI(t, "set", path, "--set", "TITLE=New", "--legacy", "strip")
	if code != 0 {
		t.Fatalf("set --legacy strip exit = %d, want 0", code)
	}
	if !strings.Contains(out, "legacy-strip-dropped") {
		t.Errorf("--legacy strip destroyed legacy-only values silently:\n%s", out)
	}
	for _, key := range []string{"ALBUM", "RECORDINGDATE", "COMMENT", "GENRE"} {
		if !strings.Contains(out, key) {
			t.Errorf("the warning does not name %s:\n%s", key, out)
		}
	}

	// --strict refuses at exit 2 and leaves the file byte-identical.
	strictPath := writeLegacyOnlyMP3(t)
	_, errOut, code := runCLI(t, "set", strictPath, "--set", "TITLE=New", "--legacy", "strip", "--strict")
	if code != 2 {
		t.Errorf("--strict exit = %d, want 2:\n%s", code, errOut)
	}
	after, err := os.ReadFile(strictPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("--strict refused but the file changed")
	}
}

// TestSetPresetMinimalWarnsLikeLegacyStrip: --preset minimal must warn like --legacy strip.
func TestSetPresetMinimalWarnsLikeLegacyStrip(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "set", writeLegacyOnlyMP3(t), "--set", "TITLE=New", "--preset", "minimal")
	if code != 0 {
		t.Fatalf("set --preset minimal exit = %d, want 0", code)
	}
	if !strings.Contains(out, "legacy-strip-dropped") {
		t.Errorf("--preset minimal is a quiet route around the strip warning:\n%s", out)
	}
}

// TestCopyLegacyStripWarns: copy --legacy strip warns for destination legacy-only loss.
func TestCopyLegacyStripWarns(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "src.flac")
	if _, _, code := runCLI(t, "set", td("notags.flac"), "--set", "TITLE=Src Title", "-o", src); code != 0 {
		t.Fatalf("building the source failed with exit %d", code)
	}
	out, _, code := runCLI(t, "copy", src, writeLegacyOnlyMP3(t), "--legacy", "strip")
	if code != 0 {
		t.Fatalf("copy --legacy strip exit = %d, want 0", code)
	}
	if !strings.Contains(out, "legacy-strip-dropped") {
		t.Errorf("copy --legacy strip destroyed the destination's legacy-only values silently:\n%s", out)
	}
}

// TestLegacyStripUnmappedWAVItems: unmapped LIST/INFO items drop on strip; --strict refuses.
func TestLegacyStripUnmappedWAVItems(t *testing.T) {
	t.Parallel()
	path := writeInfoOnlyWAV(t, "unmapped.wav",
		[2]string{"INAM", "Song"}, [2]string{"IKEY", "Alice"}, [2]string{"ISBJ", "Subj"})
	out, _, code := runCLI(t, "set", path, "--set", "TITLE=New", "--legacy", "strip")
	if code != 0 {
		t.Fatalf("set --legacy strip exit = %d, want 0", code)
	}
	for _, want := range []string{"legacy-strip-dropped", "IKEY", "ISBJ"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	strictPath := writeInfoOnlyWAV(t, "unmapped-strict.wav",
		[2]string{"INAM", "Song"}, [2]string{"IKEY", "Alice"})
	if _, _, code := runCLI(t, "set", strictPath, "--set", "TITLE=New", "--legacy", "strip", "--strict"); code != 2 {
		t.Errorf("--strict exit = %d, want 2", code)
	}
	if got := wavChunkKinds(t, strictPath); !slices.Contains(got, "LIST/INFO") {
		t.Errorf("--strict refused but the LIST chunk is gone: %v", got)
	}
}

// TestCopyStrictEscalatesLegacyStripDrop: copy --strict refuses when strip would drop destination legacy data.
func TestCopyStrictEscalatesLegacyStripDrop(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "src.flac")
	if _, _, code := runCLI(t, "set", td("notags.flac"), "--set", "TITLE=Src Title", "-o", src); code != 0 {
		t.Fatalf("building the source failed with exit %d", code)
	}
	dst := writeLegacyOnlyMP3(t)
	before, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runCLI(t, "copy", src, dst, "--legacy", "strip", "--strict")
	if code != 2 {
		t.Fatalf("copy --legacy strip --strict exit = %d, want 2:\n%s\n%s", code, out, errOut)
	}
	after, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("copy --strict refused but wrote anyway")
	}
}
