package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// spliceInfoAfterHeader inserts a LIST/INFO chunk right after the 12-byte RIFF/WAVE header
// of a WAV file and fixes the RIFF size, the shape a hand-built conflict has.
func spliceInfoAfterHeader(t *testing.T, path string, pairs ...[2]string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	list := riffInfoList(pairs...)
	out := slices.Concat(data[:12], list, data[12:])
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSetKeepsConflictingInfoUnderStrict: an id3 chunk and a LIST/INFO chunk that disagree on
// the title; editing ALBUM under --strict succeeds and leaves the INFO title alone.
func TestSetKeepsConflictingInfoUnderStrict(t *testing.T) {
	t.Parallel()
	f := filepath.Join(t.TempDir(), "a.wav")
	src, err := os.ReadFile(td("notags.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, src, 0o644); err != nil {
		t.Fatal(err)
	}
	// COMPOSER has no INFO identifier, so this set creates the id3 chunk.
	if _, errb, code := runCLI(t, "set", f, "--set", "TITLE=Id3 Title", "--set", "COMPOSER=X", "-q"); code != 0 {
		t.Fatalf("seed set exit %d: %s", code, errb)
	}
	spliceInfoAfterHeader(t, f, [2]string{"INAM", "Riff Title"})

	if _, errb, code := runCLI(t, "set", f, "--set", "ALBUM=Z", "--strict"); code != 0 {
		t.Fatalf("set --strict exit %d, want 0: %s", code, errb)
	}
	out, _, _ := runCLI(t, "dump", "--native", f)
	if !strings.Contains(out, "Riff Title") {
		t.Errorf("INFO title lost:\n%s", out)
	}
	if !strings.Contains(out, "conflict") {
		t.Errorf("the id3/INFO title conflict should still show:\n%s", out)
	}
}
