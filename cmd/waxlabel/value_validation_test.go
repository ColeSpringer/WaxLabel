package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintAndNoteAgree checks that set-time notes and Document.Lint use the same value
// validators. Numeric, date, boolean, MP4-integer, BPM, ReplayGain, and RELEASECOUNTRY
// values should get the same malformed verdict in both paths. RATING is free-form and is
// flagged by neither, as are RELEASESTATUS, RELEASETYPE, WORK, and MOVEMENTNAME. Valid
// values trigger neither path.
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
		{"RELEASECOUNTRY=United Kingdom", true},
		{"RELEASECOUNTRY=GB", false},
		{"RELEASECOUNTRY=XW", false},      // the MusicBrainz worldwide pseudo-code
		{"RELEASESTATUS=official", false}, // an open vocabulary: no shape to check
		{"RELEASETYPE=album", false},
		{"ITUNESADVISORY=1", false},
		{"ITUNESADVISORY=256", true}, // past the single byte the rtng atom stores
		{"ITUNESADVISORY=1.5", true},
		{"ITUNESGAPLESS=maybe", true},
		{"ITUNESGAPLESS=yes", false},
		{"BPM=174.99", false}, // fractional BPM is common (Serato/MIK/Traktor) and lints clean
		{"BPM=abc", true},
		{"MOVEMENT=3/12", true}, // no pair syntax at the tag level; the ID3 codec owns the MVIN join
		{"MOVEMENT=3", false},
		{"WORK=anything at all", false}, // free text: no shape to check
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
// warns in either slot - decodePair drops a 0 on read, so it never round-trips, even paired
// with a real total. The warning names the dropped canonical key, and --strict escalates it.
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

	// A fractional BPM stores (rounded to nearest, exit 0 with a value-coerced warning), and
	// --strict escalates the coercion to exit 2 like a drop, the Compilation-mirror behavior.
	if out, _, code := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", "BPM=174.99"); !strings.Contains(out, "value-coerced") || code != 0 {
		t.Errorf("plan BPM=174.99: want a value-coerced warning at exit 0, got exit %d:\n%s", code, out)
	}
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsM4A), "--set", "BPM=174.99", "--strict"); code != 2 {
		t.Errorf("set --strict BPM=174.99: exit = %d, want 2 (a coercion escalates like a drop)", code)
	}

	// The shared trkn atom names the offending slot: TRACKTOTAL, not TRACKNUMBER.
	out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", "TRACKNUMBER=3", "--set", "TRACKTOTAL=abc")
	if !strings.Contains(out, "value-dropped") || !strings.Contains(out, "TRACKTOTAL") {
		t.Errorf("plan TRACKTOTAL=abc: want a value-dropped warning naming TRACKTOTAL:\n%s", out)
	}

	// TRACKNUMBER=0 with a real total still loses the 0 on read (decodePair drops a 0 slot), so it
	// warns for TRACKNUMBER even though 12 stores fine. Its wording says the 0 reads back as
	// absent (the bytes are written, just not round-tripped), not that it cannot be represented -
	// unlike a genuine uint16 overflow, which keeps the "cannot be represented" phrasing.
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
	// MEDIATYPE=2 is a defined stik media kind (fits the single byte the atom stores), so it is
	// written and does not warn - the counterpart to the MEDIATYPE=256 drop above.
	for _, kv := range []string{"MEDIATYPE=2", "TRACKNUMBER=5"} {
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
//
// The FLAC/ALBUM row pins the same warning on the default (non-strict) path of a format
// that stores the extra values fine: the typed projection still reads only the first, so
// the note fires and the run exits 0, which is the designed outcome rather than a gap.
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

// TestArgTextValidationIsUsageError: an author-entered value carrying invalid
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

// TestMalformedValueNamesTheRealFault: a category with more than one way to fail must say
// which one happened. "2001-13-01" is exactly YYYY-MM-DD, so calling it the wrong shape is
// false - the month is what does not exist - and BPM=70000 is plainly a non-negative
// number that merely exceeds the atom's ceiling. A value that really is the wrong shape
// keeps the shape wording. Both surfaces read one classifier, so the set-time note and the
// lint finding agree per value (TestLintAndNoteAgree pins the verdict; this pins the
// reason).
func TestMalformedValueNamesTheRealFault(t *testing.T) {
	t.Parallel()
	// want is the substring both surfaces must carry. wantNote overrides it for the cases
	// where the note keeps its own "does not look like" phrasing, which is the pre-existing
	// per-surface convention for a shape fault: only the reason has to agree, not the voice.
	cases := []struct {
		kv, code, want, wantNote string
	}{
		// Right shape, impossible date.
		{"RECORDINGDATE=2001-13-01", "malformed-date", "is not a real date", ""},
		{"RECORDINGDATE=2001-02-30", "malformed-date", "is not a real date", ""},
		{"RECORDINGDATE=0000", "malformed-date", "is not a real date", ""},
		// Genuinely the wrong shape: unpadded, and not a date at all.
		{"RECORDINGDATE=2021-6-1", "malformed-date", "is not YYYY", ""},
		{"RECORDINGDATE=banana", "malformed-date", "is not YYYY", ""},
		// A real number past its atom's ceiling, which differs per key.
		{"BPM=70000", "malformed-number", "exceeds the maximum of 65535", ""},
		{"MEDIATYPE=9999", "malformed-number", "exceeds the maximum of 255", ""},
		{"ITUNESADVISORY=256", "malformed-number", "exceeds the maximum of 255", ""},
		{"MOVEMENT=70000", "malformed-number", "exceeds the maximum of 65535", ""},
		// Not a number at all: the shape wording still applies.
		{"BPM=abc", "malformed-number", "is not a non-negative number", "does not look like a non-negative number"},
		{"MEDIATYPE=abc", "malformed-number", "is not a non-negative integer", "does not look like a non-negative integer"},
		// A well-formed figure that a peak simply cannot be.
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
