package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStrictTrailingEmptyValueDropped checks that an edit whose value ends in a trailing empty
// element cannot round-trip through an ID3 text frame: the four ID3-backed formats warn
// value-dropped and fail --strict, while the formats that store an empty value verbatim
// (FLAC/Ogg/MP4/Matroska) round-trip it and stay exit 0.
func TestStrictTrailingEmptyValueDropped(t *testing.T) {
	t.Parallel()
	args := []string{"--set", "ARTIST=A", "--add", "ARTIST=B", "--add", "ARTIST="}

	for _, f := range []string{"notags.mp3", "notags.wav", "notags.aac", "notags.aiff"} {
		f := f
		t.Run("drops/"+f, func(t *testing.T) {
			t.Parallel()
			out, _, _ := runCLI(t, append([]string{"plan", copyFixture(t, td(f))}, args...)...)
			if !strings.Contains(out, "value-dropped") {
				t.Errorf("plan %s: want a value-dropped warning for the trailing empty:\n%s", f, out)
			}
			if _, _, code := runCLI(t, append([]string{"set", copyFixture(t, td(f)), "--strict"}, args...)...); code != 2 {
				t.Errorf("set --strict %s: exit = %d, want 2", f, code)
			}
		})
	}

	for _, f := range []string{"notags.flac", "notags.ogg", "notags.m4a", "notags.mka"} {
		f := f
		t.Run("roundtrips/"+f, func(t *testing.T) {
			t.Parallel()
			out, _, _ := runCLI(t, append([]string{"plan", copyFixture(t, td(f))}, args...)...)
			if strings.Contains(out, "value-dropped") {
				t.Errorf("plan %s: unexpected value-dropped warning (this format stores an empty value):\n%s", f, out)
			}
			if _, _, code := runCLI(t, append([]string{"set", copyFixture(t, td(f)), "--strict"}, args...)...); code != 0 {
				t.Errorf("set --strict %s: exit = %d, want 0 (the empty round-trips)", f, code)
			}
		})
	}
}

// TestStrictTrailingEmptyRegression guards the boundary from the other side: a normal multi-value
// ID3 edit with no trailing empty must stay exit 0 under --strict, and a lone empty value (one
// element) round-trips and does not warn.
func TestStrictTrailingEmptyRegression(t *testing.T) {
	t.Parallel()
	mp3 := td("notags.mp3")
	if _, _, code := runCLI(t, "set", copyFixture(t, mp3), "--set", "ARTIST=A", "--add", "ARTIST=B", "--strict"); code != 0 {
		t.Errorf("normal multi-value edit under --strict exit = %d, want 0", code)
	}
	if out, _, _ := runCLI(t, "plan", copyFixture(t, mp3), "--set", "ARTIST="); strings.Contains(out, "value-dropped") {
		t.Errorf("a lone empty ARTIST is one element and round-trips; must not warn value-dropped:\n%s", out)
	}
}

// writeLRC writes an LRC file in a temp dir and returns its path.
func writeLRC(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lyrics.lrc")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSyncedLyricsPartialDrop checks that a --synced-lyrics-file whose lines partly fail to
// produce a timed lyric warns and fails --strict, naming the dropped line numbers, while
// recognized structure (id tags, offset/length tags, bare section headers, blank lines) is not
// counted. An all-bad file still errors with the existing message, and a fully clean file is
// silent.
func TestSyncedLyricsPartialDrop(t *testing.T) {
	t.Parallel()
	partial := writeLRC(t, "[ar:Artist]\n[00:01.00]good\n[9:99.99]bad stamp\njust some text\n"+
		"[Chorus]\n[offset:+200]\n[length:03:45]\n[00:05.00]good two\n")

	out, _, _ := runCLI(t, "plan", copyFixture(t, notagsFLAC), "--synced-lyrics-file", partial)
	if !strings.Contains(out, "synced-lyrics-line-dropped") {
		t.Errorf("plan with a partial LRC: want a synced-lyrics-line-dropped warning:\n%s", out)
	}
	if !strings.Contains(out, "lines: 3, 4") {
		t.Errorf("the warning should name the dropped line numbers 3 and 4 (the metadata/section lines are not counted):\n%s", out)
	}
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--synced-lyrics-file", partial, "--strict"); code != 2 {
		t.Errorf("set --strict with a partial LRC: exit = %d, want 2", code)
	}

	// A fully clean file (only timed lines and recognized structure) is silent.
	clean := writeLRC(t, "[ti:Song]\n[00:01.00]One\n[Chorus]\n[00:12.50]Two\n")
	if out, _, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--synced-lyrics-file", clean); code != 0 || strings.Contains(out, "synced-lyrics-line-dropped") {
		t.Errorf("a clean LRC must not warn or fail; exit = %d:\n%s", code, out)
	}

	// An all-bad file still yields the existing "no timed lyric lines" usage error.
	allbad := writeLRC(t, "just text\n[9:99.99]bad\n")
	_, errb, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--synced-lyrics-file", allbad)
	if code != 2 || !strings.Contains(errb, "no timed lyric lines") {
		t.Errorf("all-bad LRC: exit = %d, want 2 with the no-timed-lines message; stderr:\n%s", code, errb)
	}
}

// TestRemovePictureRoleMiss checks that a --remove-picture role matching no picture warns and
// fails --strict rather than being a silent no-op, while an out-of-range index stays a hard usage
// error and a role that matches removes cleanly with no warning.
func TestRemovePictureRoleMiss(t *testing.T) {
	t.Parallel()

	// A role the file does not carry: warns, and fails --strict.
	out, _, _ := runCLI(t, "plan", copyFixture(t, notagsFLAC), "--remove-picture", "artist")
	if !strings.Contains(out, "picture-remove-role-miss") {
		t.Errorf("plan --remove-picture artist on a file with no artist picture: want a role-miss warning:\n%s", out)
	}
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--remove-picture", "artist", "--strict"); code != 2 {
		t.Errorf("set --strict --remove-picture artist (miss): exit = %d, want 2", code)
	}

	// An out-of-range index is still a hard usage error, not a warning.
	if _, errb, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--remove-picture", "9"); code != 2 || !strings.Contains(errb, "out of range") {
		t.Errorf("--remove-picture 9 (out of range): exit = %d, want 2 with an out-of-range error; stderr:\n%s", code, errb)
	}

	// A role that matches removes cleanly, with no role-miss warning even under --strict.
	png := writeTempImage(t, "back.png", minimalPNG())
	withPic := filepath.Join(t.TempDir(), "withpic.flac")
	if _, errb, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--add-picture", "back-cover="+png, "-o", withPic); code != 0 {
		t.Fatalf("add-picture exit = %d\n%s", code, errb)
	}
	out, _, code := runCLI(t, "set", withPic, "--remove-picture", "back-cover", "--strict", "-o", filepath.Join(t.TempDir(), "out.flac"))
	if code != 0 {
		t.Errorf("removing a matching role under --strict: exit = %d, want 0:\n%s", code, out)
	}
	if strings.Contains(out, "picture-remove-role-miss") {
		t.Errorf("a matching role removal must not warn role-miss:\n%s", out)
	}
}

// TestRemovePictureRoleMissPerFile checks the miss is attributed to the file that actually lacks
// the role, not bled across a bulk run: with an artist picture only on the first file, a two-file
// --remove-picture artist warns on the second file alone.
func TestRemovePictureRoleMissPerFile(t *testing.T) {
	t.Parallel()
	png := writeTempImage(t, "artist.png", minimalPNG())

	withPic := filepath.Join(t.TempDir(), "a.flac")
	if _, errb, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--add-picture", "artist="+png, "-o", withPic); code != 0 {
		t.Fatalf("add-picture exit = %d\n%s", code, errb)
	}
	noPic := copyFixture(t, notagsFLAC)

	out, _, _ := runCLI(t, "--json", "plan", withPic, noPic, "--remove-picture", "artist")
	reports := decodeJSONList[jsonReport](t, out)
	if len(reports) != 2 {
		t.Fatalf("want 2 plan reports, got %d:\n%s", len(reports), out)
	}
	for _, r := range reports {
		missed := false
		for _, w := range r.Warnings {
			if w.Code == "picture-remove-role-miss" {
				missed = true
			}
		}
		wantMiss := r.File == noPic
		if missed != wantMiss {
			t.Errorf("file %s: role-miss = %v, want %v (the miss must attribute to the file lacking the role only)", r.File, missed, wantMiss)
		}
	}
}

// TestMediaTypeOversizedDropped checks that a MEDIATYPE past the single byte the stik atom stores
// is dropped and warned (and fails --strict) rather than widening the atom, and is absent from the
// written file, while a value in range stores.
func TestMediaTypeOversizedDropped(t *testing.T) {
	t.Parallel()

	out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--set", "MEDIATYPE=256")
	if !strings.Contains(out, "value-dropped") {
		t.Errorf("plan MEDIATYPE=256: want a value-dropped warning:\n%s", out)
	}
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsM4A), "--set", "MEDIATYPE=256", "--strict"); code != 2 {
		t.Errorf("set --strict MEDIATYPE=256: exit = %d, want 2", code)
	}

	written := filepath.Join(t.TempDir(), "big.m4a")
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsM4A), "--set", "MEDIATYPE=256", "-o", written); code != 0 {
		t.Fatalf("set MEDIATYPE=256 (non-strict) exit = %d, want 0 (a dropped value is a no-op write)", code)
	}
	if got := tagValues(dumpJSON(t, written), "MEDIATYPE"); len(got) != 0 {
		t.Errorf("MEDIATYPE=256 must be absent from the written file (not a widened atom), got %v", got)
	}

	stored := filepath.Join(t.TempDir(), "ok.m4a")
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsM4A), "--set", "MEDIATYPE=2", "-o", stored); code != 0 {
		t.Fatalf("set MEDIATYPE=2 exit = %d", code)
	}
	if got := tagValues(dumpJSON(t, stored), "MEDIATYPE"); len(got) != 1 || got[0] != "2" {
		t.Errorf("MEDIATYPE=2 fits the atom and must be stored, got %v", got)
	}
}

// TestSilentDropFailsStrictMatrix is the standing contract test for the invariant behind these
// findings: a value or input the write silently drops must surface a warning and fail --strict,
// never exit 0. Adding a future format or drop path to the matrix guards the whole contract in one
// place, alongside the per-finding tests that pin the exact messages.
func TestSilentDropFailsStrictMatrix(t *testing.T) {
	t.Parallel()
	partial := writeLRC(t, "[00:01.00]good\n[9:99.99]bad\n[00:05.00]good two\n")

	cases := []struct {
		name     string
		fixture  string
		args     []string
		warnCode string
	}{
		{"id3 trailing empty", td("notags.mp3"), []string{"--set", "ARTIST=A", "--add", "ARTIST="}, "value-dropped"},
		{"lrc line dropped", notagsFLAC, []string{"--synced-lyrics-file", partial}, "synced-lyrics-line-dropped"},
		{"picture role miss", notagsFLAC, []string{"--remove-picture", "artist"}, "picture-remove-role-miss"},
		{"mediatype oversized", notagsM4A, []string{"--set", "MEDIATYPE=256"}, "value-dropped"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, _, _ := runCLI(t, append([]string{"plan", copyFixture(t, c.fixture)}, c.args...)...)
			if !strings.Contains(out, c.warnCode) {
				t.Errorf("plan: want warning %q in the report:\n%s", c.warnCode, out)
			}
			if _, _, code := runCLI(t, append([]string{"set", copyFixture(t, c.fixture), "--strict"}, c.args...)...); code != 2 {
				t.Errorf("set --strict: exit = %d, want 2 (a silent drop must fail --strict)", code)
			}
		})
	}
}
