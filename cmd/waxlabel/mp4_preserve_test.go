package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// sampleM4A has TRACKNUMBER=2 / TRACKTOTAL=10 for preservation tests.
var sampleM4A = filepath.Join("..", "..", "testdata", "sample.m4a")

// trackNumberOf dumps path and returns its TRACKNUMBER values (nil when absent).
func trackNumberOf(t *testing.T, path string) []string {
	t.Helper()
	return tagValues(dumpJSON(t, path), "TRACKNUMBER")
}

// TestMP4UnstorableNumberPreservesBase: unstorable trkn keeps base value, warns, byte-identical
// no-op; --strict escalates (MP4 uint16 vs text raw string).
func TestMP4UnstorableNumberPreservesBase(t *testing.T) {
	t.Parallel()
	orig, err := os.ReadFile(sampleM4A)
	if err != nil {
		t.Fatal(err)
	}

	// Keeps base, warns, exit 0.
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

	// Base preserved -> byte-identical no-op.
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(orig, after) {
		t.Errorf("unstorable edit that preserves the base value should be a byte-identical no-op")
	}

	// --strict still escalates dropped value.
	strictFile := copyFixture(t, sampleM4A)
	if _, _, code := runCLI(t, "set", strictFile, "--set", "TRACKNUMBER=99999", "--strict"); code != 2 {
		t.Errorf("--strict on a dropped value exit = %d, want 2", code)
	}
}

// TestMP4NumberZeroAndClearUnchanged: zero unset and explicit clear are outside preservation gate.
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
