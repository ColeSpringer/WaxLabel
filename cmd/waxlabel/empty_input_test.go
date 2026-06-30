package main

import (
	"strings"
	"testing"
)

// TestRejectEmptyImagePath checks that empty image paths fail as usage errors with a clear
// message before file I/O starts.
func TestRejectEmptyImagePath(t *testing.T) {
	file := copyFixture(t, notagsFLAC)
	for _, args := range [][]string{
		{"--add-cover", ""},
		{"--add-picture", "front-cover="},
	} {
		full := append([]string{"set", file}, args...)
		_, stderr, code := runCLI(t, full...)
		if code != 2 || !strings.Contains(stderr, "image path cannot be empty") {
			t.Errorf("set %v: code %d, stderr %q; want exit 2 'image path cannot be empty'", args, code, stderr)
		}
	}
}

// TestEmptyNumberAdvisory checks that TRACKNUMBER=/5 writes successfully while advising
// that the number side is empty. A negative total reports only the negative-number note.
func TestEmptyNumberAdvisory(t *testing.T) {
	_, stderr, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--set", "TRACKNUMBER=/5")
	if code != 0 {
		t.Fatalf("TRACKNUMBER=/5 exit = %d, want 0 (valid, written): %s", code, stderr)
	}
	if !strings.Contains(stderr, "no number component") {
		t.Errorf("TRACKNUMBER=/5 stderr = %q, want the empty-number advisory", stderr)
	}
	if strings.Contains(stderr, "is negative") {
		t.Errorf("TRACKNUMBER=/5 must not fire the negative note: %q", stderr)
	}

	// /-5: only the negative note, not the empty-number one.
	_, stderr2, _ := runCLI(t, "set", copyFixture(t, notagsFLAC), "--set", "TRACKNUMBER=/-5")
	if !strings.Contains(stderr2, "is negative") {
		t.Errorf("TRACKNUMBER=/-5 stderr = %q, want the negative note", stderr2)
	}
	if strings.Contains(stderr2, "no number component") {
		t.Errorf("TRACKNUMBER=/-5 must not also fire the empty-number note: %q", stderr2)
	}
}
