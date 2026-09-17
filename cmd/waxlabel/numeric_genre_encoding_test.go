package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fileState returns bytes and mtime for no-churn checks: identical bytes can still bump mtime.
func fileState(t *testing.T, path string) ([]byte, string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return data, fi.ModTime().String()
}

// seedGenre seeds a fixture with GENRE stored as a name (starting state for both repros).
func seedGenre(t *testing.T, fixture, genre string) string {
	t.Helper()
	f := copyFixture(t, filepath.Join("..", "..", "testdata", fixture))
	if _, stderr, code := runCLI(t, "set", f, "--set", "GENRE="+genre, "-q"); code != 0 {
		t.Fatalf("seed %s with GENRE=%s: code=%d stderr=%s", fixture, genre, code, stderr)
	}
	return f
}

// --numeric-genre re-encodes genre storage, not the canonical value. Gates keyed on canonical
// equality used to skip it, leaving mixed "(17)" and "17" libraries after bulk runs.
//
// Repro A: genre already the canonical name (codec no-op before rebuild). Repro B: genre
// given as the reference (frame rebuilt, same name re-projected, plan collapsed). Both must
// reach the same stored form.
func TestNumericGenreAppliesWhenOnlyEncodingChanges(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		fixture string
		want    string
	}{
		{"notags.mp3", "(17)"}, // ID3v2.3 parenthesizes a reference
		{"notags.aac", "17"},   // ID3v2.4 writes it bare
		{"notags.aiff", "17"},  // ID3v2.4, in the embedded "ID3 " chunk
	} {
		t.Run(c.fixture, func(t *testing.T) {
			t.Parallel()
			a := seedGenre(t, c.fixture, "Rock")
			if _, stderr, code := runCLI(t, "set", a, "--set", "GENRE=Rock", "--numeric-genre", "-q"); code != 0 {
				t.Fatalf("repro A: code=%d stderr=%s", code, stderr)
			}
			if got := tconText(t, a); got != c.want {
				t.Errorf("repro A (--set GENRE=Rock --numeric-genre): TCON = %q, want %q", got, c.want)
			}

			b := seedGenre(t, c.fixture, "Rock")
			if _, stderr, code := runCLI(t, "set", b, "--set", "GENRE=17", "--numeric-genre", "-q"); code != 0 {
				t.Fatalf("repro B: code=%d stderr=%s", code, stderr)
			}
			if got := tconText(t, b); got != c.want {
				t.Errorf("repro B (--set GENRE=17 --numeric-genre): TCON = %q, want %q; "+
					"a supplied reference must reach the same stored form as a supplied name, "+
					"or one bulk pass leaves a mixed library", got, c.want)
			}

			// Genre still reads back as Rock; only storage changed.
			for _, f := range []string{a, b} {
				if out, _, code := runCLI(t, "dump", f); code != 0 || !strings.Contains(out, "Rock") {
					t.Errorf("dump: code=%d, want the stored reference to resolve to Rock\n%s", code, out)
				}
			}
		})
	}
}

// MP4: --numeric-genre swaps the text "\xa9gen" atom for numeric "gnre".
func TestNumericGenreAppliesToMP4(t *testing.T) {
	t.Parallel()
	f := seedGenre(t, "notags.m4a", "Rock")
	if data, _ := fileState(t, f); !bytes.Contains(data, []byte("\xa9gen")) {
		t.Fatalf("seed did not store a text genre atom")
	}
	if _, stderr, code := runCLI(t, "set", f, "--set", "GENRE=Rock", "--numeric-genre", "-q"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	data, _ := fileState(t, f)
	if !bytes.Contains(data, []byte("gnre")) {
		t.Error("--numeric-genre on MP4 should store the numeric gnre atom")
	}
	if bytes.Contains(data, []byte("\xa9gen")) {
		t.Error("the text genre atom should have been replaced, not kept alongside gnre")
	}
	if out, _, _ := runCLI(t, "dump", f); !strings.Contains(out, "Rock") {
		t.Errorf("the gnre atom should read back as Rock:\n%s", out)
	}
}

// MP4 chapter edits rewrite moov.udta and hit a separate no-op downgrade; encoding rewrites
// must force ilst rebuild there too.
func TestNumericGenreAppliesOnMP4ChapterPath(t *testing.T) {
	t.Parallel()
	f := seedGenre(t, "sample_chapters.m4b", "Rock")
	out, stderr, code := runCLI(t, "set", f, "--set", "GENRE=Rock", "--add-chapter", "0:02=A", "--numeric-genre")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if data, _ := fileState(t, f); !bytes.Contains(data, []byte("gnre")) {
		t.Errorf("--numeric-genre with a chapter edit should still store the gnre atom:\n%s", out)
	}
}

// WAV applies the flag only when an embedded "id3 " chunk exists. testdata has none; seed
// MUSICBRAINZ_TRACKID (not INFO-representable) to force the chunk.
func TestNumericGenreAppliesToWAVWithID3Chunk(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, filepath.Join("..", "..", "testdata", "notags.wav"))
	if _, stderr, code := runCLI(t, "set", f, "--set", "GENRE=Rock", "--set", "MUSICBRAINZ_TRACKID=abc", "-q"); code != 0 {
		t.Fatalf("seed: code=%d stderr=%s", code, stderr)
	}
	data, _ := fileState(t, f)
	if !bytes.Contains(data, []byte("id3 ")) {
		t.Fatal("the synthesized fixture has no id3 chunk; the rest of this test would prove nothing")
	}
	if got := tconText(t, f); got != "Rock" {
		t.Fatalf("seeded TCON = %q, want %q", got, "Rock")
	}
	if _, stderr, code := runCLI(t, "set", f, "--set", "GENRE=Rock", "--numeric-genre", "-q"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if got := tconText(t, f); got != "17" {
		t.Errorf("TCON in the id3 chunk = %q, want %q", got, "17")
	}
	// Second pass is a no-op.
	before, mtimeBefore := fileState(t, f)
	if _, _, code := runCLI(t, "set", f, "--set", "GENRE=Rock", "--numeric-genre", "-q"); code != 0 {
		t.Fatalf("second pass: code=%d", code)
	}
	if after, mtimeAfter := fileState(t, f); !bytes.Equal(before, after) || mtimeBefore != mtimeAfter {
		t.Error("a second --numeric-genre pass rewrote the WAV instead of settling")
	}
}

// Flag must apply only where it changes storage. No-op rewrites must not touch bytes or mtime.
func TestNumericGenreDoesNotChurn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		fixture string
		seed    []string // edit that reaches the settled state
		because string
	}{
		{
			name: "already numeric", fixture: "notags.mp3",
			seed:    []string{"--set", "GENRE=Rock", "--numeric-genre"},
			because: "idempotency: the second run finds the requested representation already stored",
		},
		{
			name: "already numeric, ID3v2.4", fixture: "notags.aac",
			seed:    []string{"--set", "GENRE=Rock", "--numeric-genre"},
			because: "v2.4 stores the reference bare, so the comparison must hold for that form too",
		},
		{
			name: "already numeric, AIFF ID3 chunk", fixture: "notags.aiff",
			seed:    []string{"--set", "GENRE=Rock", "--numeric-genre"},
			because: "idempotency through the embedded container rather than a front tag",
		},
		{
			// MUSICBRAINZ_TRACKID forces an id3 chunk; genre lands in both containers.
			name: "already numeric, WAV id3 chunk", fixture: "notags.wav",
			seed:    []string{"--set", "GENRE=Rock", "--set", "MUSICBRAINZ_TRACKID=abc", "--numeric-genre"},
			because: "the only WAV shape the flag reaches must settle like the rest",
		},
		{
			name: "no genre at all", fixture: "notags.mp3",
			seed:    []string{"--set", "TITLE=x"},
			because: "there is no genre to re-encode",
		},
		{
			name: "genre outside the 192-entry table", fixture: "notags.mp3",
			seed:    []string{"--set", "GENRE=Chiptune Surf"},
			because: "nothing resolves to a reference, so the render matches what is stored",
		},
		{
			name: "escaped literal that never resolved", fixture: "notags.mp3",
			seed:    []string{"--set", "GENRE=(255)"},
			because: "255 is out of range, so it stays literal; the escape is applied once, on the seed",
		},
		{
			name: "INFO-only WAV", fixture: "notags.wav",
			seed:    []string{"--set", "GENRE=Rock"},
			because: "LIST/INFO IGNR stores the name literally and there is no id3 chunk to re-encode",
		},
		{
			name: "native-text-only AIFF", fixture: "notags.aiff",
			seed:    []string{"--set", "TITLE=x"},
			because: "the native text chunks hold the edit, so no ID3 container exists",
		},
		{
			name: "MP4 already numeric", fixture: "notags.m4a",
			seed:    []string{"--set", "GENRE=Rock", "--numeric-genre"},
			because: "idempotency on the gnre atom",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := copyFixture(t, filepath.Join("..", "..", "testdata", c.fixture))
			args := append([]string{"set", f}, c.seed...)
			if _, stderr, code := runCLI(t, append(args, "-q")...); code != 0 {
				t.Fatalf("seed: code=%d stderr=%s", code, stderr)
			}
			before, mtimeBefore := fileState(t, f)

			out, stderr, code := runCLI(t, "set", f, "--numeric-genre")
			if code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			after, mtimeAfter := fileState(t, f)
			if !bytes.Equal(before, after) {
				t.Errorf("--numeric-genre rewrote the file (%d -> %d bytes); %s\n%s", len(before), len(after), c.because, out)
			}
			if mtimeBefore != mtimeAfter {
				t.Errorf("--numeric-genre bumped the mtime without changing the bytes; %s", c.because)
			}
			if !strings.Contains(out, "no changes") {
				t.Errorf("want a reported no-op; %s\n%s", c.because, out)
			}
		})
	}
}

// Omitting the flag must not revert numeric storage on unrelated edits (avoids ping-pong).
//
// MP4 needs its own case: buildItems rebuilds ilst from canonical tags and would convert gnre
// back to "\xa9gen" even when genre was untouched.
func TestNumericGenreSurvivesAnUnrelatedEdit(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		fixture string
		check   func(t *testing.T, path string) // fails if numeric form was lost
	}{
		{"notags.mp3", func(t *testing.T, path string) {
			if got := tconText(t, path); got != "(17)" {
				t.Fatalf("TCON = %q, want it to stay %q", got, "(17)")
			}
		}},
		{"notags.aiff", func(t *testing.T, path string) {
			if got := tconText(t, path); got != "17" {
				t.Fatalf("TCON = %q, want it to stay %q", got, "17")
			}
		}},
		{"notags.m4a", func(t *testing.T, path string) {
			data, _ := fileState(t, path)
			if !bytes.Contains(data, []byte("gnre")) || bytes.Contains(data, []byte("\xa9gen")) {
				t.Fatal("the gnre atom was converted back to the text genre atom")
			}
		}},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			t.Parallel()
			f := seedGenre(t, c.fixture, "Rock")
			if _, stderr, code := runCLI(t, "set", f, "--set", "GENRE=Rock", "--numeric-genre", "-q"); code != 0 {
				t.Fatalf("numeric-genre pass: code=%d stderr=%s", code, stderr)
			}
			c.check(t, f)
			for i, title := range []string{"one", "two", "three"} {
				if _, _, code := runCLI(t, "set", f, "--set", "TITLE="+title, "-q"); code != 0 {
					t.Fatalf("title edit %d: code=%d", i, code)
				}
				c.check(t, f)
			}
		})
	}
}

// Changing the genre value drops numeric storage; --numeric-genre stays opt-in, not sticky.
func TestGenreValueChangeDropsTheNumericForm(t *testing.T) {
	t.Parallel()
	mp3 := seedGenre(t, "notags.mp3", "Rock")
	if _, _, code := runCLI(t, "set", mp3, "--set", "GENRE=Rock", "--numeric-genre", "-q"); code != 0 {
		t.Fatalf("mp3 numeric pass: code=%d", code)
	}
	if _, _, code := runCLI(t, "set", mp3, "--set", "GENRE=Jazz", "-q"); code != 0 {
		t.Fatalf("mp3 genre change: code=%d", code)
	}
	if got := tconText(t, mp3); got != "Jazz" {
		t.Errorf("MP3 TCON after a genre change with no flag = %q, want %q", got, "Jazz")
	}

	m4a := seedGenre(t, "notags.m4a", "Rock")
	if _, _, code := runCLI(t, "set", m4a, "--set", "GENRE=Rock", "--numeric-genre", "-q"); code != 0 {
		t.Fatalf("mp4 numeric pass: code=%d", code)
	}
	if _, _, code := runCLI(t, "set", m4a, "--set", "GENRE=Jazz", "-q"); code != 0 {
		t.Fatalf("mp4 genre change: code=%d", code)
	}
	if data, _ := fileState(t, m4a); bytes.Contains(data, []byte("gnre")) {
		t.Error("MP4 kept the gnre atom through a genre change made with no flag")
	}
}

// Unconvertible TCON forms must not rewrite (fixtures hand-built; the writer never emits them).
func TestNumericGenreLeavesUnconvertibleFormsAlone(t *testing.T) {
	t.Parallel()
	for _, tcon := range []string{
		"(RX)",     // a special reference with no ID3v1 index
		"RX",       // the bare form of the same
		"(255)",    // an out-of-range reference, so a literal
		"(17)Hard", // reference plus refinement: two canonical values in one frame value
		"(17)(8)",  // two references in one frame value
	} {
		t.Run(tcon, func(t *testing.T) {
			t.Parallel()
			f := writeMP3WithTCON(t, tcon)
			// Confirm fixture TCON before asserting no-op (bad frame size would weaken the case).
			if got := tconText(t, f); got != tcon {
				t.Fatalf("fixture stores TCON %q, want %q", got, tcon)
			}
			before, mtimeBefore := fileState(t, f)
			out, stderr, code := runCLI(t, "set", f, "--numeric-genre")
			if code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			after, mtimeAfter := fileState(t, f)
			if !bytes.Equal(before, after) || mtimeBefore != mtimeAfter {
				t.Errorf("--numeric-genre rewrote TCON %q; the conversion cannot improve it\n%s", tcon, out)
			}
		})
	}
}

// writeMP3WithTCON builds an MP3 with a lone Latin-1 TCON frame holding body verbatim.
func writeMP3WithTCON(t *testing.T, body string) string {
	t.Helper()
	audio, err := os.ReadFile(filepath.Join("..", "..", "testdata", "notags.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	frame := append([]byte("TCON"), 0, 0, 0, byte(len(body)+1), 0, 0) // 4-cc, 32-bit size, 2 flag bytes
	frame = append(frame, 0)                                          // Latin-1 text encoding
	frame = append(frame, body...)

	size := len(frame)
	tag := append([]byte("ID3"), 3, 0, 0) // v2.3, no flags
	tag = append(tag,
		byte(size>>21)&0x7F, byte(size>>14)&0x7F, byte(size>>7)&0x7F, byte(size)&0x7F) // sync-safe size
	tag = append(tag, frame...)

	p := filepath.Join(t.TempDir(), "tcon.mp3")
	if err := os.WriteFile(p, append(tag, audio...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Encoding-only write has no other operations; plan must still report "genre encoding rewrite".
func TestNumericGenrePlanReportsTheOperation(t *testing.T) {
	t.Parallel()
	f := seedGenre(t, "notags.mp3", "Rock")
	out, _, code := runCLI(t, "plan", f, "--set", "GENRE=Rock", "--numeric-genre")
	if code != 0 {
		t.Fatalf("plan: code=%d", code)
	}
	if !strings.Contains(out, "genre encoding rewrite") {
		t.Errorf("plan of an encoding-only write must name the operation:\n%s", out)
	}

	// Settled file: plan reports no changes, not a spurious operations block.
	if _, _, code := runCLI(t, "set", f, "--set", "GENRE=Rock", "--numeric-genre", "-q"); code != 0 {
		t.Fatalf("set: code=%d", code)
	}
	settled, _, code := runCLI(t, "plan", f, "--set", "GENRE=Rock", "--numeric-genre")
	if code != 0 {
		t.Fatalf("plan of a settled file: code=%d", code)
	}
	if !strings.Contains(settled, "no changes") || strings.Contains(settled, "genre encoding rewrite") {
		t.Errorf("plan of an already-numeric file should report no changes:\n%s", settled)
	}
}
