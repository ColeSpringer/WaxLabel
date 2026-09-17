package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAliasCollisionNote: conflicting --set aliases for one canonical field warn that
// last-write-wins discarded a value. Identical values and --json stay quiet.
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
		// Collision tracking runs before DATE= skips the empty-value note.
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
		// TRACK/TRACKNUMBER trim to the same stored "1"; not a real conflict.
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
