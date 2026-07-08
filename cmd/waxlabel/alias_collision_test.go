package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAliasCollisionNote covers the alias-collision note: two differently-spelled
// --set assignments that resolve to the same canonical field (DATE and RECORDINGDATE both
// resolve to RECORDINGDATE) with conflicting values must warn that last-write-wins
// discarded one. An identical value, and --json, must not surface it.
func TestAliasCollisionNote(t *testing.T) {
	t.Parallel()
	const marker = "refer to the same field"

	t.Run("conflicting values warn", func(t *testing.T) {
		t.Parallel()
		_, stderr, _ := runCLI(t, "set", copyFixture(t, sampleFLAC), "--set", "DATE=2020", "--set", "RECORDINGDATE=2021")
		if !strings.Contains(stderr, marker) || !strings.Contains(stderr, "RECORDINGDATE") {
			t.Errorf("want an alias-collision note naming RECORDINGDATE; stderr:\n%s", stderr)
		}
	})

	t.Run("empty then set still warns", func(t *testing.T) {
		t.Parallel()
		// DATE= short-circuits the empty-value note; the collision tracking runs before that
		// continue, so the differing RECORDINGDATE=2021 is still caught.
		_, stderr, _ := runCLI(t, "set", copyFixture(t, sampleFLAC), "--set", "DATE=", "--set", "RECORDINGDATE=2021")
		if !strings.Contains(stderr, marker) {
			t.Errorf("empty DATE= then RECORDINGDATE=2021 should still note the collision; stderr:\n%s", stderr)
		}
	})

	t.Run("identical values do not warn", func(t *testing.T) {
		t.Parallel()
		_, stderr, _ := runCLI(t, "set", copyFixture(t, sampleFLAC), "--set", "DATE=2021", "--set", "RECORDINGDATE=2021")
		if strings.Contains(stderr, marker) {
			t.Errorf("identical values are not a collision; stderr:\n%s", stderr)
		}
	})

	t.Run("whitespace-only difference on a trimmable key does not warn", func(t *testing.T) {
		t.Parallel()
		// TRACK and TRACKNUMBER both resolve to TRACKNUMBER (a trimmable numeric key), and
		// "1" and " 1" both store as "1" - so this is not a real conflict and must not warn.
		_, stderr, _ := runCLI(t, "set", copyFixture(t, sampleFLAC), "--set", "TRACK=1", "--set", "TRACKNUMBER= 1")
		if strings.Contains(stderr, marker) {
			t.Errorf("a whitespace-only difference on a trimmable key must not warn (both store \"1\"); stderr:\n%s", stderr)
		}
	})

	t.Run("json suppresses the note", func(t *testing.T) {
		t.Parallel()
		out := filepath.Join(t.TempDir(), "out.flac")
		stdout, stderr, _ := runCLI(t, "set", copyFixture(t, sampleFLAC), "-o", out, "--json", "--set", "DATE=2020", "--set", "RECORDINGDATE=2021")
		if strings.Contains(stdout, marker) || strings.Contains(stderr, marker) {
			t.Errorf("--json must suppress the alias-collision note; stdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
	})
}
