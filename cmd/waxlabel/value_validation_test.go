package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintAndNoteAgree: set notes and Document.Lint share value validators; same malformed verdict.
// RATING, RELEASESTATUS, RELEASETYPE, WORK, MOVEMENTNAME are free-form (neither flags).
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
		{"REPLAYGAIN_TRACK_GAIN=Inf dB", true}, // ParseFloat accepts Inf; gain must be finite
		{"REPLAYGAIN_TRACK_PEAK=0.988553", false},
		{"REPLAYGAIN_TRACK_PEAK=-0.5", true}, // peak is magnitude, not negative
		{"REPLAYGAIN_TRACK_PEAK=NaN", true},  // ParseFloat accepts NaN; peak must be finite
		{"COMPILATION=maybe", true},
		{"COMPILATION=1", false},
		{"RATING=abc", false}, // free-form
		{"TRACKNUMBER=abc", true},
		{"RECORDINGDATE=banana", true},
		{"RECORDINGDATE=2021-06", false},
		{"RELEASECOUNTRY=United Kingdom", true},
		{"RELEASECOUNTRY=GB", false},
		{"RELEASECOUNTRY=XW", false},      // MusicBrainz worldwide pseudo-code
		{"RELEASESTATUS=official", false}, // open vocabulary
		{"RELEASETYPE=album", false},
		{"ITUNESADVISORY=1", false},
		{"ITUNESADVISORY=256", true}, // exceeds single byte rtng atom stores
		{"ITUNESADVISORY=1.5", true},
		{"ITUNESGAPLESS=maybe", true},
		{"ITUNESGAPLESS=yes", false},
		{"BPM=174.99", false}, // fractional BPM is common and lints clean
		{"BPM=abc", true},
		{"MOVEMENT=3/12", true}, // no pair syntax at tag level; ID3 codec owns MVIN join
		{"MOVEMENT=3", false},
		{"WORK=anything at all", false}, // free text
	}
	for _, c := range cases {
		c := c
		t.Run(c.kv, func(t *testing.T) {
			t.Parallel()
			file := copyFixture(t, sampleFLAC)
			// Note on stderr ("kept as text"); lint on stdout ("malformed-*"); same verdict.
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

// TestValueDroppedWarningM4A: MP4 rejects bad track/disc slots; literal "0" never round-trips
// (decodePair drops 0 on read). Warning names canonical key; --strict escalates.
func TestValueDroppedWarningM4A(t *testing.T) {
	t.Parallel()
	notagsM4A := filepath.Join("..", "..", "testdata", "notags.m4a")

	for _, kv := range []string{"TRACKNUMBER=abc", "TRACKNUMBER=70000", "TRACKNUMBER=-3", "TRACKNUMBER=0",
		"MEDIATYPE=abc", "MEDIATYPE=256", "ITUNESADVISORY=256", "MOVEMENT=70000", "BPM=abc"} {
		if out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", kv); !strings.Contains(out, "value-dropped") {
			t.Errorf("plan --set %s: missing value-dropped warning:\n%s", kv, out)
		}
		if _, _, code := runCLI(t, "set", copyFixture(t, notagsM4A), "--set", kv, "--strict"); code != 2 {
			t.Errorf("set --strict --set %s: exit = %d, want 2", kv, code)
		}
	}

	// Fractional BPM rounds with value-coerced warning; --strict escalates like a drop.
	if out, _, code := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", "BPM=174.99"); !strings.Contains(out, "value-coerced") || code != 0 {
		t.Errorf("plan BPM=174.99: want a value-coerced warning at exit 0, got exit %d:\n%s", code, out)
	}
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsM4A), "--set", "BPM=174.99", "--strict"); code != 2 {
		t.Errorf("set --strict BPM=174.99: exit = %d, want 2 (a coercion escalates like a drop)", code)
	}

	// trkn atom names offending slot: TRACKTOTAL, not TRACKNUMBER.
	out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", "TRACKNUMBER=3", "--set", "TRACKTOTAL=abc")
	if !strings.Contains(out, "value-dropped") || !strings.Contains(out, "TRACKTOTAL") {
		t.Errorf("plan TRACKTOTAL=abc: want a value-dropped warning naming TRACKTOTAL:\n%s", out)
	}

	// TRACKNUMBER=0 with real total: 0 dropped on read; wording says "reads back as absent".
	out, _, _ = runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", "TRACKNUMBER=0", "--set", "TRACKTOTAL=12")
	if !strings.Contains(out, "value-dropped") || !strings.Contains(out, "TRACKNUMBER") {
		t.Errorf("plan TRACKNUMBER=0 TRACKTOTAL=12: the 0 is dropped on read, want a value-dropped warning naming TRACKNUMBER:\n%s", out)
	}
	if !strings.Contains(out, "reads back as absent") {
		t.Errorf("plan TRACKNUMBER=0: the 0-as-unset warning should say it reads back as absent, not that it cannot be represented:\n%s", out)
	}
	if overflow, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", "TRACKNUMBER=70000"); !strings.Contains(overflow, "cannot be represented") {
		t.Errorf("plan TRACKNUMBER=70000: a uint16 overflow should keep the 'cannot be represented' wording:\n%s", overflow)
	}
	// MEDIATYPE=2 fits stik byte; no warning (contrast MEDIATYPE=256 above).
	for _, kv := range []string{"MEDIATYPE=2", "TRACKNUMBER=5"} {
		if out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", kv); strings.Contains(out, "value-dropped") {
			t.Errorf("plan --set %s: unexpected value-dropped warning:\n%s", kv, out)
		}
	}
}

// TestValueDroppedWarningVisibleOnNoOpOutput: value-dropped must show on set -o no-op writes.
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

// TestMatroskaSingleValuedMultiWarning: edit intent, not re-projected result. TITLE=A+B warns
// on Matroska (Info.Title single-valued) and on FLAC (default path, exit 0); --strict -> 2.
func TestMatroskaSingleValuedMultiWarning(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, fixture, key string }{
		{"matroska TITLE", notagsMKA, "TITLE"},
		{"flac ALBUM", sampleFLAC, "ALBUM"},
	} {
		out, _, code := runCLI(t, "plan", copyFixture(t, c.fixture), "--set", c.key+"=A", "--add", c.key+"=B")
		if !strings.Contains(out, "single-valued-multi") {
			t.Errorf("plan %s A+B on %s: missing single-valued-multi warning:\n%s", c.key, c.name, out)
		}
		if code != 0 {
			t.Errorf("plan %s A+B on %s: exit = %d, want 0 (a note, not a failure)", c.key, c.name, code)
		}
	}
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsMKA), "--set", "TITLE=A", "--add", "TITLE=B", "--strict"); code != 2 {
		t.Errorf("set --strict TITLE A+B on Matroska: exit = %d, want 2", code)
	}
}

// TestArgTextValidationIsUsageError: invalid UTF-8 or NUL in author text is exit 2 at CLI
// boundary (checkArgText), not library exit 4, including read-only plan.
func TestArgTextValidationIsUsageError(t *testing.T) {
	t.Parallel()
	badUTF8 := "x\xffy" // a lone 0xff is invalid UTF-8

	// Valid cover so --picture-description fails on description, not missing-picture branch.
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

	// LRC file content: NUL is valid UTF-8 but must still exit 2 (not UTF-8-only gap at exit 4).
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

// TestMalformedValueNamesTheRealFault: multi-failure categories name the actual fault.
// Set note and lint message agree (TestLintAndNoteAgree pins verdict; this pins reason).
func TestMalformedValueNamesTheRealFault(t *testing.T) {
	t.Parallel()
	// wantNote overrides where note keeps "does not look like" phrasing for shape faults.
	cases := []struct {
		kv, code, want, wantNote string
	}{
		// Impossible date (right shape).
		{"RECORDINGDATE=2001-13-01", "malformed-date", "is not a real date", ""},
		{"RECORDINGDATE=2001-02-30", "malformed-date", "is not a real date", ""},
		{"RECORDINGDATE=0000", "malformed-date", "is not a real date", ""},
		// Wrong shape.
		{"RECORDINGDATE=2021-6-1", "malformed-date", "is not YYYY", ""},
		{"RECORDINGDATE=banana", "malformed-date", "is not YYYY", ""},
		// Number past atom ceiling (varies by key).
		{"BPM=70000", "malformed-number", "exceeds the maximum of 65535", ""},
		{"MEDIATYPE=9999", "malformed-number", "exceeds the maximum of 255", ""},
		{"ITUNESADVISORY=256", "malformed-number", "exceeds the maximum of 255", ""},
		{"MOVEMENT=70000", "malformed-number", "exceeds the maximum of 65535", ""},
		// Not a number: shape wording applies.
		{"BPM=abc", "malformed-number", "is not a non-negative number", "does not look like a non-negative number"},
		{"MEDIATYPE=abc", "malformed-number", "is not a non-negative integer", "does not look like a non-negative integer"},
		// Well-formed value invalid for key semantics.
		{"REPLAYGAIN_TRACK_PEAK=-0.5", "malformed-number", "a peak is an amplitude", ""},
		{"REPLAYGAIN_TRACK_GAIN=loud", "malformed-number", "is not a ReplayGain value", "does not look like a ReplayGain value"},
	}
	for _, c := range cases {
		t.Run(c.kv, func(t *testing.T) {
			t.Parallel()
			f := copyFixture(t, sampleFLAC)
			_, note, code := runCLI(t, "set", f, "--set", c.kv)
			if code != 0 {
				t.Fatalf("set exit = %d\n%s", code, note)
			}
			wantNote := c.wantNote
			if wantNote == "" {
				wantNote = c.want
			}
			if !strings.Contains(note, wantNote) {
				t.Errorf("set note = %q, want it to contain %q", note, wantNote)
			}
			out, _, _ := runCLI(t, "--json", "lint", f)
			jl := decodeJSONList[jsonLint](t, out)
			if len(jl) != 1 {
				t.Fatalf("want one lint result, got %d: %s", len(jl), out)
			}
			var msg string
			for _, fnd := range jl[0].Findings {
				if fnd.Code == c.code {
					msg = fnd.Message
				}
			}
			if msg == "" {
				t.Fatalf("lint reported no %s for %q:\n%s", c.code, c.kv, out)
			}
			if !strings.Contains(msg, c.want) {
				t.Errorf("lint message = %q, want it to contain %q", msg, c.want)
			}
		})
	}
}
