package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// TestRepeatedSetNotesTheDiscardedValue: repeated --set warns once with surviving value; identical repeat silent.
func TestRepeatedSetNotesTheDiscardedValue(t *testing.T) {
	t.Parallel()
	t.Run("differing", func(t *testing.T) {
		t.Parallel()
		f := copyFixture(t, sampleFLAC)
		// Three assignments: note names final survivor once, not each intermediate loser.
		_, note, code := runCLI(t, "set", f, "--set", "TITLE=A", "--set", "TITLE=B", "--set", "TITLE=C")
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		if !strings.Contains(note, `--set TITLE was given more than once; last value "C" was used`) {
			t.Errorf("want a repeated-key note naming the surviving value, got:\n%s", note)
		}
		if got := strings.Count(note, "was given more than once"); got != 1 {
			t.Errorf("repeated-key notes = %d, want exactly 1:\n%s", got, note)
		}
		// Note is advisory; write takes last value.
		if got := tagValues(dumpJSON(t, f), "TITLE"); len(got) != 1 || got[0] != "C" {
			t.Errorf("stored TITLE = %q, want [C] (last wins)", got)
		}
	})
	t.Run("identical", func(t *testing.T) {
		t.Parallel()
		f := copyFixture(t, sampleFLAC)
		_, note, code := runCLI(t, "set", f, "--set", "TITLE=A", "--set", "TITLE=A")
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		if strings.Contains(note, "more than once") {
			t.Errorf("an identical repeat discards nothing and should stay silent, got:\n%s", note)
		}
	})
	t.Run("alias", func(t *testing.T) {
		t.Parallel()
		f := copyFixture(t, sampleFLAC)
		_, note, code := runCLI(t, "set", f, "--set", "DATE=2001", "--set", "RECORDINGDATE=2002", "--set", "DATE=2003")
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		// Alias collision names both spellings and survivor once.
		if !strings.Contains(note, `--set DATE and --set RECORDINGDATE refer to the same field (RECORDINGDATE); last value "2003" was used`) {
			t.Errorf("the alias-collision note should name both spellings and the survivor, got:\n%s", note)
		}
		if got := strings.Count(note, "refer to the same field"); got != 1 {
			t.Errorf("alias-collision notes = %d, want exactly 1:\n%s", got, note)
		}
	})
}

// TestUnknownKeyNotesAreCapped: bulk unknown keys cap listed notes and aggregate the rest.
func TestUnknownKeyNotesAreCapped(t *testing.T) {
	t.Parallel()
	const n = 200
	// n unknown keys on one file.
	mk := func(f, flag, format string) []string {
		args := []string{"set", f}
		for i := range n {
			args = append(args, flag, fmt.Sprintf(format, i))
		}
		return args
	}

	t.Run("set", func(t *testing.T) {
		t.Parallel()
		f := copyFixture(t, sampleFLAC)
		_, note, code := runCLI(t, mk(f, "--set", "ZZKEY%04d=v")...)
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		if got := strings.Count(note, "is not a known key;"); got != noteListCap {
			t.Errorf("per-key notes = %d, want %d", got, noteListCap)
		}
		if want := fmt.Sprintf("note: %d more unknown key(s) not listed", n-noteListCap); !strings.Contains(note, want) {
			t.Errorf("want the aggregate line %q, got:\n%s", want, note)
		}
	})

	t.Run("clear", func(t *testing.T) {
		t.Parallel()
		f := copyFixture(t, sampleFLAC)
		_, note, code := runCLI(t, mk(f, "--clear", "ZZKEY%04d")...)
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		if got := strings.Count(note, "is not a known key ("); got != noteListCap {
			t.Errorf("per-key notes = %d, want %d", got, noteListCap)
		}
		if want := fmt.Sprintf("note: %d more unknown key(s) not listed", n-noteListCap); !strings.Contains(note, want) {
			t.Errorf("want the aggregate line %q, got:\n%s", want, note)
		}
	})

	t.Run("value-notes", func(t *testing.T) {
		t.Parallel()
		// Empty --set values flood notes the same way; cap applies here too.
		f := copyFixture(t, sampleFLAC)
		_, note, code := runCLI(t, mk(f, "--set", "ZZKEY%04d=")...)
		if code != 0 {
			t.Fatalf("set exit = %d\n%s", code, note)
		}
		if got := strings.Count(note, "writes an empty value"); got != noteListCap {
			t.Errorf("empty-value notes = %d, want %d", got, noteListCap)
		}
		if want := fmt.Sprintf("note: %d more note(s) not listed", n-noteListCap); !strings.Contains(note, want) {
			t.Errorf("want the aggregate line %q, got:\n%s", want, note)
		}
	})

	t.Run("strict lists every key", func(t *testing.T) {
		t.Parallel()
		// Strict failure lists every key; cap applies to notes only, not error envelope.
		f := copyFixture(t, sampleFLAC)
		_, errb, code := runCLI(t, append(mk(f, "--set", "ZZKEY%04d=v"), "--strict")...)
		if code != 2 {
			t.Fatalf("strict exit = %d, want 2\n%s", code, errb)
		}
		for _, i := range []int{0, n / 2, n - 1} {
			if key := fmt.Sprintf("ZZKEY%04d", i); !strings.Contains(errb, key) {
				t.Errorf("the strict key list omits %s:\n%s", key, errb)
			}
		}
		if strings.Contains(errb, "more)") {
			t.Errorf("the strict key list should not be truncated:\n%s", errb)
		}
	})
}

// --preserve-mtime on standard input has nothing to preserve: the buffered copy's mtime is the
// time it was read.
func TestPreserveMtimeOnStdinNotes(t *testing.T) {
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.flac")
	_, stderr, code := runCLIStdin(t, string(data), "set", "-", "-o", out, "--set", "TITLE=x", "--preserve-mtime")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "--preserve-mtime has no effect") {
		t.Errorf("stderr = %q, want a note that --preserve-mtime has no effect on standard input", stderr)
	}
}

// TestSetFrameIDSpelledKeysAreCustomFields: --set TCON=Jazz and --set TXXX=foo are custom fields
// like any other: the plan and dump show each under its own name, the file's genre frame is left
// alone, and --clear removes them.
func TestSetFrameIDSpelledKeysAreCustomFields(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleMP3)
	out, errb, code := runCLI(t, "set", f, "--set", "TCON=Jazz", "--set", "TXXX=foo")
	if code != 0 {
		t.Fatalf("set exit = %d\n%s", code, errb)
	}
	if !strings.Contains(out, "+ TCON: Jazz") || !strings.Contains(out, "+ TXXX: foo") {
		t.Errorf("the plan does not list the two custom fields:\n%s", out)
	}
	jd := dumpJSON(t, f)
	for key, want := range map[string]string{"TCON": "Jazz", "TXXX": "foo", "GENRE": "Rock"} {
		if got := tagValues(jd, key); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want [%s]", key, got, want)
		}
	}
	if _, errb, code := runCLI(t, "set", f, "--clear", "TCON", "--clear", "TXXX"); code != 0 {
		t.Fatalf("clear exit = %d\n%s", code, errb)
	}
	jd = dumpJSON(t, f)
	if len(tagValues(jd, "TCON")) != 0 || len(tagValues(jd, "TXXX")) != 0 {
		t.Errorf("the clear left TCON %q or TXXX %q", tagValues(jd, "TCON"), tagValues(jd, "TXXX"))
	}
	if got := tagValues(jd, "GENRE"); len(got) != 1 || got[0] != "Rock" {
		t.Errorf("GENRE = %q after clearing the custom TCON, want [Rock]", got)
	}
}

// TestSetUSLTMeansLyrics: USLT is an alias of LYRICS on every format, so --set USLT=x writes the
// lyrics, not a custom field, and keys lists it among LYRICS' aliases.
func TestSetUSLTMeansLyrics(t *testing.T) {
	t.Parallel()
	for _, src := range []string{sampleFLAC, sampleMP3, sampleM4A} {
		f := copyFixture(t, src)
		if _, errb, code := runCLI(t, "set", f, "--set", "USLT=words"); code != 0 {
			t.Fatalf("%s: set exit = %d\n%s", src, code, errb)
		}
		jd := dumpJSON(t, f)
		if got := tagValues(jd, "LYRICS"); len(got) != 1 || got[0] != "words" || len(tagValues(jd, "USLT")) != 0 {
			t.Errorf("%s: LYRICS = %q, USLT = %q; want the lyrics set and no custom field", src, got, tagValues(jd, "USLT"))
		}
	}
	out, _, code := runCLI(t, "keys")
	if code != 0 || !strings.Contains(out, "(aliases: UNSYNCEDLYRICS, USLT)") {
		t.Errorf("keys exit = %d, want LYRICS' aliases to list USLT:\n%s", code, out)
	}
}

// TestClearAliasSpelledAlbumArtist: ffmpeg leaves a FLAC's "ALBUM ARTIST" field as TXXX:ALBUM ARTIST,
// which reads as ALBUMARTIST, so --clear "ALBUM ARTIST" removes it along with TPE2 instead of
// deleting TPE2 and leaving the TXXX behind.
func TestClearAliasSpelledAlbumArtist(t *testing.T) {
	t.Parallel()
	audio, err := os.ReadFile(filepath.Join("..", "..", "testdata", "notags.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	txxx := testfile.ID3Frame(4, "TXXX", []byte("\x03ALBUM ARTIST\x00Alias AA\x00"))
	f := writeTempFile(t, "alias.mp3", slices.Concat(testfile.ID3v2(4, testfile.TextFrame(4, "TPE2", "Real AA"), txxx), audio))
	if got := tagValues(dumpJSON(t, f), "ALBUMARTIST"); len(got) != 2 {
		t.Fatalf("ALBUMARTIST = %q, want both spellings' values", got)
	}
	if _, errb, code := runCLI(t, "set", f, "--clear", "ALBUM ARTIST"); code != 0 {
		t.Fatalf("clear exit = %d\n%s", code, errb)
	}
	jd := dumpJSON(t, f)
	if len(tagValues(jd, "ALBUMARTIST")) != 0 || len(tagValues(jd, "ALBUM ARTIST")) != 0 {
		t.Errorf("after the clear ALBUMARTIST = %q, ALBUM ARTIST = %q; want neither", tagValues(jd, "ALBUMARTIST"), tagValues(jd, "ALBUM ARTIST"))
	}
}
