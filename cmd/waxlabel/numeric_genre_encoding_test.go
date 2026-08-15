package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fileState returns a file's bytes and modification time, for the no-churn assertions: a
// rewrite that produced identical bytes would still bump the mtime, breaking hard links and
// backup tools, so both have to hold.
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

// seedGenre copies a fixture and stores GENRE textually, the starting state both repros
// begin from: a file whose genre is already the requested value, stored as a name.
func seedGenre(t *testing.T, fixture, genre string) string {
	t.Helper()
	f := copyFixture(t, filepath.Join("..", "..", "testdata", fixture))
	if _, stderr, code := runCLI(t, "set", f, "--set", "GENRE="+genre, "-q"); code != 0 {
		t.Fatalf("seed %s with GENRE=%s: code=%d stderr=%s", fixture, genre, code, stderr)
	}
	return f
}

// TestNumericGenreAppliesWhenOnlyEncodingChanges is the report's defect: --numeric-genre
// changes the ENCODING of the genre, not its canonical value, so every gate that keys on
// canonical equality used to drop it. A bulk normalisation run then converted only the files
// whose genre also changed, producing exactly the mixed-representation library the flag
// exists to eliminate.
//
// Two repros reach it through different gates and both must be covered. A) the value is
// already the canonical name, so the codec's no-op fast path fires before any frame is
// rebuilt. B) the value is given as the reference itself, so the frame IS rebuilt, but the
// result re-projects to the same name and the no-op downgrade collapses the plan.
//
// Both land on the same stored form. A bare reference the edit supplies is resolved and
// re-emitted in the write version's canonical form, so one pass cannot leave a library
// mixing "17" and "(17)" depending on how each file's genre happened to be given.
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

			// Either way the genre still reads back as Rock: the flag changes the storage,
			// never the value.
			for _, f := range []string{a, b} {
				if out, _, code := runCLI(t, "dump", f); code != 0 || !strings.Contains(out, "Rock") {
					t.Errorf("dump: code=%d, want the stored reference to resolve to Rock\n%s", code, out)
				}
			}
		})
	}
}

// The same defect on MP4, whose genre is an atom rather than an ID3 frame: --numeric-genre
// swaps the text "\xa9gen" atom for the numeric "gnre" one.
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

// MP4 has a second write path: a chapter edit rewrites the whole moov.udta and reaches its
// own no-op downgrade, which passed a hardcoded "nothing structural changed". An encoding
// rewrite has to force the ilst rebuild there too, or the flag is dropped on exactly the
// files a chapter edit touches.
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

// WAV is the one format where the flag applies conditionally: the genre lives in LIST/INFO
// IGNR, which stores the name literally, unless the file also carries an embedded "id3 "
// chunk. testdata has no such WAV, so the fixture is synthesized here - MUSICBRAINZ_TRACKID
// is not INFO-representable, so storing it forces the chunk into existence.
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
	// And it settles: a second pass leaves the file untouched.
	before, mtimeBefore := fileState(t, f)
	if _, _, code := runCLI(t, "set", f, "--set", "GENRE=Rock", "--numeric-genre", "-q"); code != 0 {
		t.Fatalf("second pass: code=%d", code)
	}
	if after, mtimeAfter := fileState(t, f); !bytes.Equal(before, after) || mtimeBefore != mtimeAfter {
		t.Error("a second --numeric-genre pass rewrote the WAV instead of settling")
	}
}

// TestNumericGenreDoesNotChurn is the other half of the fix: the flag must apply wherever it
// has an effect and nowhere else. A rewrite that changes nothing still bumps the mtime and
// breaks hard links, so a bulk run over a library must leave the settled files completely
// untouched - identical bytes AND an unchanged mtime.
func TestNumericGenreDoesNotChurn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		fixture string
		seed    []string // the edit that establishes the settled state
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
			// MUSICBRAINZ_TRACKID is not INFO-representable, so storing it forces the id3
			// chunk into existence; the genre then lands in both containers.
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

// The absence of the flag is not a request to re-encode back, so an unrelated edit on a file
// already storing the reference must leave the genre alone. Without this the file would
// ping-pong between the two representations on alternating runs, which no amount of
// re-running the same command would reveal.
//
// MP4 needs its own row and does not get this for free. The ID3 codecs preserve a TCON no
// canonical change touched, but buildItems rebuilds every ilst item from the canonical tags,
// so an unrelated edit re-derives the genre atom from scratch and would silently convert an
// existing gnre back to the text atom.
func TestNumericGenreSurvivesAnUnrelatedEdit(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		fixture string
		check   func(t *testing.T, path string) // fails if the numeric form was lost
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

// A genre edit that changes the value does drop back to the name, on every format. The
// preservation above is scoped to "this edit did not touch the genre" so that --numeric-genre
// stays the only way to ask for the numeric form, rather than becoming sticky forever.
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

// The forms --numeric-genre must leave alone, driven through the real CLI. Each renders
// identically with and without the conversion, so rewriting one would swap a reference the
// file already holds for its plain name, or downgrade a spec-legal ID3v2.3 frame to the
// nonstandard NUL-separated extension. The fixtures are hand-built ID3 tags, because
// WaxLabel's own writer never produces these forms.
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
			// Prove the fixture parses as intended before asserting on it: a mis-sized frame
			// header would decode to a different value list and silently weaken the case.
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

// writeMP3WithTCON builds an MP3 carrying an ID3v2.3 tag whose only frame is a Latin-1 TCON
// holding body verbatim, so a test can seed a stored form WaxLabel's writer never emits.
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

// An encoding-only write changes no canonical value and, since "Rock" and "(17)" are the
// same length, no byte count either, so no other operation line fires. Without a line of its
// own the plan would report a real write with no operations at all. Containment, not an
// exact list: a padding line can legitimately join it.
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

	// And the other half: once the file is settled, plan reports no changes rather than an
	// operations block for a write that will not happen.
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
