package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeLegacyOnlyMP3 writes an MP3 whose ID3v2 holds TITLE and ARTIST while its ID3v1
// trailer is the only home for ALBUM, RECORDINGDATE, COMMENT and GENRE. It is built by
// running the CLI itself so the ID3v2 side is a real tag rather than a hand-rolled one.
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

// TestSetLegacyStripWarnsAndStrictRefuses is the CLI half of the frozen contract: a
// --legacy strip that destroys legacy-only values says which, and --strict turns that into
// a refusal that writes nothing.
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

// TestSetPresetMinimalWarnsLikeLegacyStrip: --preset minimal resolves to LegacyStrip, so it
// must not be a quiet route around the warning.
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

// TestCopyLegacyStripWarns: copy has its own --legacy flag, and the destination's own data is
// what disappears, so the carry suppression that silences source-authored warnings must not
// silence this one.
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

// TestLegacyStripUnmappedWAVItems is the WAV arm of the same flag: consolidating LIST/INFO
// into an id3 chunk cannot carry an item with no canonical key, so the drop is reported and
// --strict refuses it.
func TestLegacyStripUnmappedWAVItems(t *testing.T) {
	t.Parallel()
	path := writeInfoOnlyWAV(t, "unmapped.wav",
		[2]string{"INAM", "Song"}, [2]string{"IENG", "Alice"}, [2]string{"ISBJ", "Subj"})
	out, _, code := runCLI(t, "set", path, "--set", "TITLE=New", "--legacy", "strip")
	if code != 0 {
		t.Fatalf("set --legacy strip exit = %d, want 0", code)
	}
	for _, want := range []string{"legacy-strip-dropped", "IENG", "ISBJ"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	strictPath := writeInfoOnlyWAV(t, "unmapped-strict.wav",
		[2]string{"INAM", "Song"}, [2]string{"IENG", "Alice"})
	if _, _, code := runCLI(t, "set", strictPath, "--set", "TITLE=New", "--legacy", "strip", "--strict"); code != 2 {
		t.Errorf("--strict exit = %d, want 2", code)
	}
	if got := wavChunkKinds(t, strictPath); !slices.Contains(got, "LIST/INFO") {
		t.Errorf("--strict refused but the LIST chunk is gone: %v", got)
	}
}

// TestCopyStrictEscalatesLegacyStripDrop pins the half of copy --strict the report cannot
// see: the projection is a clean carry, but the destination's own legacy container dies
// under the write policy the user asked for. The warning is emitted outside the carried
// gate precisely so a copy cannot be a quiet route around it.
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
