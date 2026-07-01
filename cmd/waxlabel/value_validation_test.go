package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintAndNoteAgree checks that set-time notes and Document.Lint use the same value
// validators. Numeric, date, boolean, MEDIATYPE, and ReplayGain values should get the
// same malformed verdict in both paths. RATING is free-form and is flagged by neither.
// Valid values trigger neither path.
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

// TestValueDroppedWarningM4A checks the values MP4 cannot store faithfully. Numeric
// track and disc slots reject non-numeric, negative, and >65535 values. A literal "0"
// also warns when pairItem collapses the whole pair to absent, but 0/total stores
// cleanly. The warning names the dropped canonical key, and --strict escalates it.
func TestValueDroppedWarningM4A(t *testing.T) {
	t.Parallel()
	notagsM4A := filepath.Join("..", "..", "testdata", "notags.m4a")

	for _, kv := range []string{"TRACKNUMBER=abc", "TRACKNUMBER=70000", "TRACKNUMBER=-3", "TRACKNUMBER=0", "MEDIATYPE=abc"} {
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

	// A 0 that keeps the pair (0/total) and other storable values do not warn.
	out, _, _ = runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", "TRACKNUMBER=0", "--set", "TRACKTOTAL=12")
	if strings.Contains(out, "value-dropped") {
		t.Errorf("plan TRACKNUMBER=0 TRACKTOTAL=12: 0/12 writes fine, must not warn:\n%s", out)
	}
	for _, kv := range []string{"MEDIATYPE=70000", "TRACKNUMBER=5"} {
		if out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", kv); strings.Contains(out, "value-dropped") {
			t.Errorf("plan --set %s: unexpected value-dropped warning:\n%s", kv, out)
		}
	}
}

// TestValueDroppedWarningVisibleOnNoOpOutput checks that value-dropped warnings still
// surface when the dropped value makes the write a no-op. This matters for `set -o`,
// which otherwise suppresses the no-op preview. TRACKNUMBER=70000 is a valid integer
// that overflows the uint16 atom, so the plan-body warning is the only signal.
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

// TestMatroskaSingleValuedMultiWarning checks the edit intent, not only Matroska's
// re-projected result. Info.Title stores one value, so TITLE=A plus TITLE=B must warn
// even though the codec result collapses to one value. --strict escalates it to exit 2.
func TestMatroskaSingleValuedMultiWarning(t *testing.T) {
	t.Parallel()
	if out, _, _ := runCLI(t, "plan", copyFixture(t, notagsMKA), "--set", "TITLE=A", "--add", "TITLE=B"); !strings.Contains(out, "single-valued-multi") {
		t.Errorf("plan TITLE A+B on Matroska: missing single-valued-multi warning:\n%s", out)
	}
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsMKA), "--set", "TITLE=A", "--add", "TITLE=B", "--strict"); code != 2 {
		t.Errorf("set --strict TITLE A+B on Matroska: exit = %d, want 2", code)
	}
}

// TestArgTextValidationIsUsageError covers the F5 fix: an author-entered value carrying invalid
// UTF-8 (or, for file content, a NUL) is caught at the CLI boundary as a usage error (exit 2),
// not deferred to the library's exit-4 "file is corrupt" backstop - even on read-only `plan`
// against a valid file. Every author-text boundary routes through the shared checkArgText.
func TestArgTextValidationIsUsageError(t *testing.T) {
	t.Parallel()
	badUTF8 := "x\xffy" // a lone 0xff is invalid UTF-8

	// A valid cover so the --picture-description case fails on the description text, not on the
	// "needs at least one picture" branch.
	cover := writeTempImage(t, "cover.png", minimalPNG())

	argCases := map[string][]string{
		"--set value":         {"plan", sampleFLAC, "--set", "K=" + badUTF8},
		"--add value":         {"plan", sampleFLAC, "--add", "K=" + badUTF8},
		"--add-chapter title": {"plan", sampleFLAC, "--add-chapter", "1:30=" + badUTF8},
		"--add-synced-lyric":  {"plan", sampleFLAC, "--add-synced-lyric", "1:30=" + badUTF8},
		"--picture-description": {"plan", sampleFLAC,
			"--add-cover", cover, "--picture-description", "d\xffe"},
	}
	for name, args := range argCases {
		if _, _, code := runCLI(t, args...); code != 2 {
			t.Errorf("%s: exit = %d, want 2 (usage error, not exit 4)", name, code)
		}
	}

	// --synced-lyrics-file is file content, which can hold a NUL that is valid UTF-8 - the case a
	// UTF-8-only check would leave at exit 4. Both invalid UTF-8 and a NUL must land at exit 2.
	fileCases := map[string]string{
		"LRC invalid UTF-8": "[00:12.00]Bad" + badUTF8 + "Line\n",
		"LRC NUL byte":      "[00:12.00]Null\x00Line\n",
	}
	for name, content := range fileCases {
		lrc := filepath.Join(t.TempDir(), "lyrics.lrc")
		if err := os.WriteFile(lrc, []byte(content), 0o644); err != nil {
			t.Fatalf("%s: write LRC: %v", name, err)
		}
		if _, _, code := runCLI(t, "plan", sampleFLAC, "--synced-lyrics-file", lrc); code != 2 {
			t.Errorf("%s: exit = %d, want 2 (usage error, not exit 4)", name, code)
		}
	}
}
