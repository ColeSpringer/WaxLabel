package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// sampleM4A carries TRACKNUMBER=2 / TRACKTOTAL=10, the base value the preservation tests
// expect an unstorable edit to keep.
var sampleM4A = filepath.Join("..", "..", "testdata", "sample.m4a")

// trackNumberOf dumps path and returns its TRACKNUMBER values (nil when absent).
func trackNumberOf(t *testing.T, path string) []string {
	t.Helper()
	return tagValues(dumpJSON(t, path), "TRACKNUMBER")
}

// TestMP4UnstorableNumberPreservesBase: setting a trkn slot to a genuinely unstorable value
// (past uint16) on a file that already has a valid one keeps the old value rather than
// erasing it, still warns value-dropped, and - since nothing else changed - collapses to a
// byte-identical no-op that --strict still escalates to exit 2. This is MP4's fixed-uint16
// divergence from the text formats, which store the raw string.
func TestMP4UnstorableNumberPreservesBase(t *testing.T) {
	t.Parallel()
	orig, err := os.ReadFile(sampleM4A)
	if err != nil {
		t.Fatal(err)
	}

	// The edit keeps the base value, warns, and exits 0.
	file := copyFixture(t, sampleM4A)
	out, errb, code := runCLI(t, "set", file, "--set", "TRACKNUMBER=99999")
	if code != 0 {
		t.Fatalf("set TRACKNUMBER=99999 exit = %d, want 0; stderr=%q", code, errb)
	}
	if !bytes.Contains([]byte(out), []byte("value-dropped")) {
		t.Errorf("set should warn value-dropped for the unstorable number:\n%s", out)
	}
	if got := trackNumberOf(t, file); len(got) != 1 || got[0] != "2" {
		t.Errorf("TRACKNUMBER after unstorable edit = %v, want [2] (base preserved)", got)
	}

	// Preserving the base value makes the write a byte-identical no-op.
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(orig, after) {
		t.Errorf("unstorable edit that preserves the base value should be a byte-identical no-op")
	}

	// The dropped value still escalates under --strict.
	strictFile := copyFixture(t, sampleM4A)
	if _, _, code := runCLI(t, "set", strictFile, "--set", "TRACKNUMBER=99999", "--strict"); code != 2 {
		t.Errorf("--strict on a dropped value exit = %d, want 2", code)
	}
}

// TestMP4NumberZeroAndClearUnchanged guards the two cases the preservation gate must NOT
// touch: a literal 0 still writes and reads back absent (the ZeroUnset case, which fits
// uint16 and is not "unstorable"), and an explicit clear still removes the value.
func TestMP4NumberZeroAndClearUnchanged(t *testing.T) {
	t.Parallel()

	zeroFile := copyFixture(t, sampleM4A)
	if _, errb, code := runCLI(t, "set", zeroFile, "--set", "TRACKNUMBER=0"); code != 0 {
		t.Fatalf("set TRACKNUMBER=0 exit = %d: %s", code, errb)
	}
	if got := trackNumberOf(t, zeroFile); len(got) != 0 {
		t.Errorf("TRACKNUMBER after =0 = %v, want absent (0 reads back unset, base not restored)", got)
	}

	clearFile := copyFixture(t, sampleM4A)
	if _, errb, code := runCLI(t, "set", clearFile, "--clear", "TRACKNUMBER"); code != 0 {
		t.Fatalf("clear TRACKNUMBER exit = %d: %s", code, errb)
	}
	if got := trackNumberOf(t, clearFile); len(got) != 0 {
		t.Errorf("TRACKNUMBER after clear = %v, want absent", got)
	}
}
