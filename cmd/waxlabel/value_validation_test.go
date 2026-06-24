package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLintAndNoteAgree (F4): the set-time malformed-value note and Document.Lint read
// one shared validator registry, so they must agree on which values are malformed -
// numeric, date, boolean, MEDIATYPE (a non-negative int), and ReplayGain (a decimal,
// optionally dB; a peak is non-negative). RATING is free-form and is flagged by
// neither. A valid value triggers neither half.
func TestLintAndNoteAgree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kv        string
		malformed bool
	}{
		{"MEDIATYPE=abc", true},
		{"MEDIATYPE=2", false},
		{"REPLAYGAIN_TRACK_GAIN=loud", true},
		{"REPLAYGAIN_TRACK_GAIN=-7.30 dB", false},
		{"REPLAYGAIN_TRACK_GAIN=Inf dB", true}, // ParseFloat accepts Inf; a gain must be finite
		{"REPLAYGAIN_TRACK_PEAK=0.988553", false},
		{"REPLAYGAIN_TRACK_PEAK=-0.5", true}, // a peak is a magnitude, never negative
		{"REPLAYGAIN_TRACK_PEAK=NaN", true},  // ParseFloat accepts NaN; a peak must be finite
		{"COMPILATION=maybe", true},
		{"COMPILATION=1", false},
		{"RATING=abc", false}, // free-form: malformed by neither
		{"TRACKNUMBER=abc", true},
		{"RECORDINGDATE=banana", true},
		{"RECORDINGDATE=2021-06", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.kv, func(t *testing.T) {
			t.Parallel()
			file := copyFixture(t, sampleFLAC)
			// The note half writes to stderr ("... kept as text where the format supports
			// it"); the lint half to stdout (a "malformed-*" finding code). They must reach
			// the same verdict.
			_, noteErr, _ := runCLI(t, "set", file, "--set", c.kv)
			noted := strings.Contains(noteErr, "kept as text")
			lintOut, _, _ := runCLI(t, "lint", file)
			linted := strings.Contains(lintOut, "malformed-")
			if noted != c.malformed {
				t.Errorf("%q: set note present = %v, want %v; stderr:\n%s", c.kv, noted, c.malformed, noteErr)
			}
			if linted != c.malformed {
				t.Errorf("%q: lint malformed present = %v, want %v; out:\n%s", c.kv, linted, c.malformed, lintOut)
			}
		})
	}
}

// TestValueDroppedWarningM4A (F1): an iTunes atom cannot hold a non-numeric, negative,
// or >65535 trkn/disk slot, nor a non-numeric stik - the encoder drops it silently. The
// plan now surfaces a value-dropped warning per dropped key (naming the offending slot,
// not the merged pair), and --strict escalates it to exit 2. A representable value (a
// uint32 stik like 70000, or the pair encoder's "absent" 0) warns about nothing.
func TestValueDroppedWarningM4A(t *testing.T) {
	t.Parallel()
	notagsM4A := filepath.Join("..", "..", "testdata", "notags.m4a")

	for _, kv := range []string{"TRACKNUMBER=abc", "TRACKNUMBER=70000", "TRACKNUMBER=-3", "MEDIATYPE=abc"} {
		if out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", kv); !strings.Contains(out, "value-dropped") {
			t.Errorf("plan --set %s: missing value-dropped warning:\n%s", kv, out)
		}
		if _, _, code := runCLI(t, "set", copyFixture(t, notagsM4A), "--set", kv, "--strict"); code != 2 {
			t.Errorf("set --strict --set %s: exit = %d, want 2", kv, code)
		}
	}

	// The shared trkn atom names the offending slot: TRACKTOTAL, not TRACKNUMBER.
	out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", "TRACKNUMBER=3", "--set", "TRACKTOTAL=abc")
	if !strings.Contains(out, "value-dropped") || !strings.Contains(out, "TRACKTOTAL") {
		t.Errorf("plan TRACKTOTAL=abc: want a value-dropped warning naming TRACKTOTAL:\n%s", out)
	}

	// Representable / absent values do not warn.
	for _, kv := range []string{"TRACKNUMBER=0", "MEDIATYPE=70000", "TRACKNUMBER=5"} {
		if out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", kv); strings.Contains(out, "value-dropped") {
			t.Errorf("plan --set %s: unexpected value-dropped warning:\n%s", kv, out)
		}
	}
}

// TestValueDroppedWarningVisibleOnNoOpOutput (review): a value-dropped edit whose only
// effect is the drop is a no-op write, but the warning must still surface - including
// the `set -o` path, which otherwise suppresses the no-op preview. TRACKNUMBER=70000 is
// a valid integer that overflows the uint16 atom, so it gets no stderr value note; the
// plan-body value-dropped warning is then the only signal the edit was rejected.
func TestValueDroppedWarningVisibleOnNoOpOutput(t *testing.T) {
	t.Parallel()
	in := copyFixture(t, filepath.Join("..", "..", "testdata", "notags.m4a"))
	out := filepath.Join(t.TempDir(), "out.m4a")
	stdout, _, code := runCLI(t, "set", in, "-o", out, "--set", "TRACKNUMBER=70000")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (a dropped value is a no-op write, not a failure)", code)
	}
	if !strings.Contains(stdout, "value-dropped") {
		t.Errorf("set -o no-op must still surface the value-dropped warning; got:\n%s", stdout)
	}
}

// TestMatroskaSingleValuedMultiWarning (F2): Matroska collapses Info.Title to one value
// in its codec result, so the single-valued-multi check must judge the edit intent, not
// the re-projected result - otherwise the one format that truly loses the value stays
// silent. The warning fires in the plan body, and --strict escalates it to exit 2.
func TestMatroskaSingleValuedMultiWarning(t *testing.T) {
	t.Parallel()
	if out, _, _ := runCLI(t, "plan", copyFixture(t, notagsMKA), "--set", "TITLE=A", "--add", "TITLE=B"); !strings.Contains(out, "single-valued-multi") {
		t.Errorf("plan TITLE A+B on Matroska: missing single-valued-multi warning:\n%s", out)
	}
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsMKA), "--set", "TITLE=A", "--add", "TITLE=B", "--strict"); code != 2 {
		t.Errorf("set --strict TITLE A+B on Matroska: exit = %d, want 2", code)
	}
}
