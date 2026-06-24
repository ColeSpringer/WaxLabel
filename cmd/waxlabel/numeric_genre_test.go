package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNumericGenreFlag covers the --numeric-genre flag end to end: it is bound on
// editFlags and resolves in writeOptions(), so both set and plan accept it via
// compile() with no per-caller wiring. With the flag, a recognized genre is stored
// as its ID3 numeric reference ("(17)" for Rock) instead of the name; without it the
// name is stored. Either way dump resolves the stored form back to the canonical
// name, so the flag changes only the on-disk encoding. A regression that drops the
// wiring would resurface here as an "unknown flag" error (non-zero exit) or a
// name-encoded TCON frame.
func TestNumericGenreFlag(t *testing.T) {
	t.Parallel()
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// tconText returns the decoded text of the first ID3v2 TCON (genre) frame: a
	// 10-byte frame header, then a text-encoding byte, then the text. Every offset is
	// bounded against len(data) so a malformed or truncated frame fails the test cleanly
	// instead of panicking. The fixtures here carry exactly one TCON, so the first match
	// is the genre frame; a wrong match would fail the value assertion below regardless.
	tconText := func(t *testing.T, path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		i := bytes.Index(data, []byte("TCON"))
		if i < 0 || i+10 > len(data) {
			t.Fatalf("no usable TCON frame in %s", path)
		}
		size := int(data[i+4])<<24 | int(data[i+5])<<16 | int(data[i+6])<<8 | int(data[i+7])
		end := i + 10 + size
		if size < 1 || end > len(data) {
			t.Fatalf("TCON frame size %d out of bounds (i=%d, len=%d)", size, i, len(data))
		}
		return string(data[i+11 : end]) // skip the 1-byte text-encoding marker
	}

	// With the flag: the numeric reference. Rock is genre 17 in the ID3v1 list, and
	// ID3v2.3 writes a numeric reference parenthesized.
	num := copyFixture(t, notagsMP3)
	if _, stderr, code := runCLI(t, "set", num, "--set", "GENRE=Rock", "--numeric-genre"); code != 0 {
		t.Fatalf("set --numeric-genre: code=%d stderr=%s", code, stderr)
	}
	if got := tconText(t, num); got != "(17)" {
		t.Errorf("TCON with --numeric-genre = %q, want %q", got, "(17)")
	}

	// Without the flag: the canonical name, confirming the flag is what changes it.
	name := copyFixture(t, notagsMP3)
	if _, _, code := runCLI(t, "set", name, "--set", "GENRE=Rock"); code != 0 {
		t.Fatalf("set (no flag): code=%d", code)
	}
	if got := tconText(t, name); got != "Rock" {
		t.Errorf("TCON without --numeric-genre = %q, want %q", got, "Rock")
	}

	// The numeric form still resolves back to the name on read (a round-trip detail
	// the flag must not break), and plan accepts the flag too (shared wiring).
	if stdout, _, code := runCLI(t, "dump", num); code != 0 || !strings.Contains(stdout, "Rock") {
		t.Errorf("dump of numeric-genre file: code=%d, want it to resolve to Rock\n%s", code, stdout)
	}
	if _, stderr, code := runCLI(t, "plan", name, "--set", "GENRE=Jazz", "--numeric-genre"); code != 0 {
		t.Fatalf("plan --numeric-genre: code=%d stderr=%s", code, stderr)
	}
}
