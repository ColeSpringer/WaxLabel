package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tconText returns the first TCON frame body. Bounds-checked; plain BE size works for genre
// bodies under 128 (sync-safe equivalent). Scans whole file for WAV/AIFF id3 chunks.
func tconText(t *testing.T, path string) string {
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

// TestNumericGenreFlag: --numeric-genre stores "(17)" for Rock; dump still resolves to name.
// Shared editFlags wiring covers set and plan.
func TestNumericGenreFlag(t *testing.T) {
	t.Parallel()
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// Rock is ID3 genre 17; flag stores "(17)".
	num := copyFixture(t, notagsMP3)
	if _, stderr, code := runCLI(t, "set", num, "--set", "GENRE=Rock", "--numeric-genre"); code != 0 {
		t.Fatalf("set --numeric-genre: code=%d stderr=%s", code, stderr)
	}
	if got := tconText(t, num); got != "(17)" {
		t.Errorf("TCON with --numeric-genre = %q, want %q", got, "(17)")
	}

	// Without flag: stores canonical name.
	name := copyFixture(t, notagsMP3)
	if _, _, code := runCLI(t, "set", name, "--set", "GENRE=Rock"); code != 0 {
		t.Fatalf("set (no flag): code=%d", code)
	}
	if got := tconText(t, name); got != "Rock" {
		t.Errorf("TCON without --numeric-genre = %q, want %q", got, "Rock")
	}

	// Numeric form round-trips to name on dump; plan accepts the flag.
	if stdout, _, code := runCLI(t, "dump", num); code != 0 || !strings.Contains(stdout, "Rock") {
		t.Errorf("dump of numeric-genre file: code=%d, want it to resolve to Rock\n%s", code, stdout)
	}
	if _, stderr, code := runCLI(t, "plan", name, "--set", "GENRE=Jazz", "--numeric-genre"); code != 0 {
		t.Fatalf("plan --numeric-genre: code=%d stderr=%s", code, stderr)
	}
}

// TestBareRXCRGenreResolvesAndWarns: bare RX/CR warn on write and read; "(RX)" stays literal.
func TestBareRXCRGenreResolvesAndWarns(t *testing.T) {
	t.Parallel()
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	for _, c := range []struct{ in, want string }{{"RX", "Remix"}, {"CR", "Cover"}} {
		// Plan warns numeric-genre for bare reference.
		out, _, code := runCLI(t, "plan", notagsMP3, "--set", "GENRE="+c.in)
		if code != 0 {
			t.Fatalf("plan GENRE=%s exit = %d, want 0", c.in, code)
		}
		if !strings.Contains(out, "numeric-genre") {
			t.Errorf("bare GENRE=%s on an ID3 target must warn numeric-genre (reads back as %q):\n%s", c.in, c.want, out)
		}

		// Dump resolves to display name with read-time numeric-genre warning.
		f := copyFixture(t, notagsMP3)
		if _, stderr, code := runCLI(t, "set", f, "--set", "GENRE="+c.in); code != 0 {
			t.Fatalf("set GENRE=%s exit = %d: %s", c.in, code, stderr)
		}
		dumped, _, code := runCLI(t, "dump", f)
		if code != 0 {
			t.Fatalf("dump after GENRE=%s exit = %d", c.in, code)
		}
		if !strings.Contains(dumped, c.want) {
			t.Errorf("bare GENRE=%s should read back resolved to %q:\n%s", c.in, c.want, dumped)
		}
		if !strings.Contains(dumped, "numeric-genre") {
			t.Errorf("dumping a bare TCON=%s should raise the read-time numeric-genre warning:\n%s", c.in, dumped)
		}
	}

	// "(RX)" round-trips verbatim without warning.
	if out, _, _ := runCLI(t, "plan", notagsMP3, "--set", "GENRE=(RX)"); strings.Contains(out, "numeric-genre") {
		t.Errorf("(RX) round-trips verbatim and must not warn numeric-genre:\n%s", out)
	}
}
