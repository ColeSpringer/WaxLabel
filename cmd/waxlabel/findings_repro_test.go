package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// sampleWAV and sampleMP3 are declared in transfer_test.go (same package).

// writeTempImage writes data to a fresh temp file and returns its path.
func writeTempImage(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestVersionSubcommand(t *testing.T) {
	t.Parallel()
	sub, _, code := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("version exit = %d, want 0", code)
	}
	if !strings.HasPrefix(sub, "waxlabel version ") {
		t.Errorf("version output = %q, want 'waxlabel version ...'", sub)
	}
	flag, _, _ := runCLI(t, "--version")
	if sub != flag {
		t.Errorf("`version` (%q) and `--version` (%q) disagree", sub, flag)
	}
}

// Leading-dash paths need a "--" hint (shorthand and long).
func TestDashPathHint(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"-track.flac", "--track.flac"} {
		_, errb, code := runCLI(t, "dump", arg)
		if code != 2 {
			t.Errorf("dump %s exit = %d, want 2", arg, code)
		}
		if !strings.Contains(errb, "put '--' before it") {
			t.Errorf("dump %s missing the -- hint:\n%s", arg, errb)
		}
	}
}

// Zero spellings combine with --no-padding; positive --padding still conflicts.
func TestPaddingZeroCombinesWithNoPadding(t *testing.T) {
	t.Parallel()
	for _, pad := range []string{"0", "00", " 0 "} {
		f := copyFixture(t, sampleFLAC)
		out := filepath.Join(t.TempDir(), "out.flac")
		if _, errb, code := runCLI(t, "set", f, "--padding", pad, "--no-padding", "-o", out); code != 0 {
			t.Errorf("set --padding %q --no-padding exit = %d, want 0\n%s", pad, code, errb)
		}
	}
	if _, _, code := runCLI(t, "plan", sampleFLAC, "--padding", "16384", "--no-padding"); code != 2 {
		t.Errorf("positive --padding with --no-padding exit = %d, want 2", code)
	}
}

// Unidentifiable stdin reports "<stdin>", not the buffered temp path.
func TestStdinUnidentifiedNamesStdin(t *testing.T) {
	t.Parallel()
	_, errb, _ := runCLIStdin(t, "not audio at all", "dump", "-")
	if strings.Contains(errb, "waxlabel-stdin") {
		t.Errorf("buffered-stdin temp path leaked:\n%s", errb)
	}
	if !strings.Contains(errb, `could not identify "<stdin>"`) {
		t.Errorf("error should name <stdin>:\n%s", errb)
	}
}

// WAV notes padding flags do not apply; FLAC/MP3 honor them (MP3 --no-padding shrinks, must not say "no effect").
func TestPaddingNotePerFormat(t *testing.T) {
	t.Parallel()
	wav := copyFixture(t, sampleWAV)
	if _, errb, _ := runCLI(t, "set", wav, "--no-padding", "-o", filepath.Join(t.TempDir(), "o.wav")); !strings.Contains(errb, "does not apply to WAV") {
		t.Errorf("WAV --no-padding should note it does not apply:\n%s", errb)
	}
	// MP3 honors padding flags; no "does not apply" or "no effect" note.
	for _, flags := range [][]string{{"--no-padding"}, {"--padding", "30000"}} {
		args := append([]string{"set", copyFixture(t, sampleMP3)}, flags...)
		args = append(args, "-o", filepath.Join(t.TempDir(), "o.mp3"))
		if _, errb, _ := runCLI(t, args...); strings.Contains(errb, "does not apply") || strings.Contains(errb, "no effect") {
			t.Errorf("MP3 %v should get no padding note:\n%s", flags, errb)
		}
	}
	if _, errb, _ := runCLI(t, "plan", sampleFLAC, "--no-padding"); strings.Contains(errb, "does not apply") || strings.Contains(errb, "no effect") {
		t.Errorf("FLAC --no-padding should get no padding note:\n%s", errb)
	}
}

// Match want on the padding: line only, not fields/pictures/chapters.
func paddingLineHas(capsOut, want string) bool {
	for _, line := range strings.Split(capsOut, "\n") {
		if strings.Contains(line, "padding:") {
			return strings.Contains(line, want)
		}
	}
	return false
}

func TestCapsPaddingLevel(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ format, level string }{
		{"flac", "full"}, {"wav", "none"}, {"mp3", "partial"},
	} {
		out, _, code := runCLI(t, "caps", "--format", c.format)
		if code != 0 {
			t.Fatalf("caps --format %s exit = %d", c.format, code)
		}
		// "full"/"none"/"partial" also appear on other caps lines; anchor to padding:.
		if !paddingLineHas(out, c.level) {
			t.Errorf("caps --format %s should report padding %q on the padding: line:\n%s", c.format, c.level, out)
		}
		jsonOut, _, _ := runCLI(t, "caps", "--format", c.format, "--json")
		var jc jsonCaps
		if err := json.Unmarshal([]byte(jsonOut), &jc); err != nil {
			t.Fatalf("caps --json unmarshal: %v", err)
		}
		if jc.Padding != c.level {
			t.Errorf("caps --format %s --json padding = %q, want %q", c.format, jc.Padding, c.level)
		}
	}
}

// Near-miss keys get "did you mean?"; a typo'd --clear is surfaced, not a silent no-op.
func TestUnknownKeySuggestions(t *testing.T) {
	t.Parallel()
	_, errb, _ := runCLI(t, "plan", sampleFLAC, "--set", "TITEL=x")
	if !strings.Contains(errb, "did you mean TITLE?") {
		t.Errorf("--set TITEL should suggest TITLE:\n%s", errb)
	}
	_, errb, _ = runCLI(t, "plan", sampleFLAC, "--clear", "ARTIS")
	if !strings.Contains(errb, "clearing affects only a custom field") || !strings.Contains(errb, "did you mean ARTIST?") {
		t.Errorf("--clear ARTIS should note + suggest ARTIST:\n%s", errb)
	}
}

func TestValueNotes(t *testing.T) {
	t.Parallel()
	if _, errb, _ := runCLI(t, "plan", sampleFLAC, "--set", "COMPILATION=maybe"); !strings.Contains(errb, "does not look like a boolean") {
		t.Errorf("COMPILATION=maybe should note non-boolean:\n%s", errb)
	}
	if _, errb, _ := runCLI(t, "plan", sampleFLAC, "--set", "TRACKNUMBER=-3"); !strings.Contains(errb, "is negative") {
		t.Errorf("TRACKNUMBER=-3 should note negative:\n%s", errb)
	}
}

// lint and set must agree after set trims numerics (whitespace-only -> empty-value, not malformed).
func TestLintSetAgreeOnWhitespaceNumeric(t *testing.T) {
	t.Parallel()

	f := copyFixture(t, "../../testdata/notags.flac")
	if _, errb, code := runCLI(t, "set", f, "--set", "TRACKNUMBER=   "); code != 0 {
		t.Fatalf("set TRACKNUMBER=whitespace exit %d: %s", code, errb)
	}
	if out, _, code := runCLI(t, "lint", f); code != 0 {
		t.Errorf("lint of a whitespace-only TRACKNUMBER = exit %d, want 0 (clean, matching set)\n%s", code, out)
	}

	g := copyFixture(t, "../../testdata/notags.flac")
	if _, errb, code := runCLI(t, "set", g, "--set", "TRACKNUMBER= 3 "); code != 0 {
		t.Fatalf("set TRACKNUMBER=' 3 ' exit %d: %s", code, errb)
	}
	if out, _, code := runCLI(t, "lint", g); code != 0 {
		t.Errorf("lint of a space-padded TRACKNUMBER = exit %d, want 0\n%s", code, out)
	}
	dump, _, _ := runCLI(t, "dump", "--json", g)
	if got := tagValues(decodeJSONOne[jsonDocument](t, dump), "TRACKNUMBER"); len(got) != 1 || got[0] != "3" {
		t.Errorf("stored TRACKNUMBER = %v, want [3] (trimmed)", got)
	}
}

// Role+description add/remove; description-only and unknown roles are usage errors.
func TestPictureEditing(t *testing.T) {
	t.Parallel()
	png := writeTempImage(t, "back.png", minimalPNG())
	f := copyFixture(t, notagsFLAC)
	withPic := filepath.Join(t.TempDir(), "withpic.flac")
	if _, errb, code := runCLI(t, "set", f, "--add-picture", "back-cover="+png, "--picture-description", "rear sleeve", "-o", withPic); code != 0 {
		t.Fatalf("add-picture exit = %d\n%s", code, errb)
	}
	out, _, _ := runCLI(t, "dump", withPic)
	if !strings.Contains(out, "Back cover") || !strings.Contains(out, "rear sleeve") {
		t.Errorf("dump should show the back cover and its description:\n%s", out)
	}
	removed := filepath.Join(t.TempDir(), "removed.flac")
	if _, errb, code := runCLI(t, "set", withPic, "--remove-picture", "back-cover", "-o", removed); code != 0 {
		t.Fatalf("remove-picture exit = %d\n%s", code, errb)
	}
	if out, _, _ := runCLI(t, "dump", removed); strings.Contains(out, "Back cover") {
		t.Errorf("dump should no longer show a back cover:\n%s", out)
	}
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--picture-description", "x", "-o", filepath.Join(t.TempDir(), "n.flac")); code != 2 {
		t.Errorf("--picture-description alone exit = %d, want 2", code)
	}
	_, errb, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--add-picture", "bogus="+png, "-o", filepath.Join(t.TempDir(), "b.flac"))
	if code != 2 || !strings.Contains(errb, "valid roles") {
		t.Errorf("unknown role exit = %d (want 2), stderr:\n%s", code, errb)
	}
}

// Role names come from PictureType.String(); guards against silent renames breaking front-cover= etc.
func TestPictureRoleVocabulary(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]wl.PictureType{
		"front-cover": wl.PicFrontCover, // --add-cover alias
		"back-cover":  wl.PicBackCover,
		"artist":      wl.PicArtist,
		"lead-artist": wl.PicLeadArtist, // distinct from "artist"
		"other":       wl.PicOther,
	} {
		if got, ok := pictureRole(name); !ok || got != want {
			t.Errorf("pictureRole(%q) = %v, %v; want %v, true", name, got, ok, want)
		}
	}
	if _, ok := pictureRole("bogus-role"); ok {
		t.Error("pictureRole(bogus-role) should not resolve")
	}
}

// --add-cover replaces an existing front cover; does not append.
func TestAddCoverReplacesFrontCover(t *testing.T) {
	t.Parallel()
	coverA := writeTempImage(t, "a.png", minimalPNG())
	coverB := writeTempImage(t, "b.png", append(minimalPNG(), 0x7A)) // distinct PNG
	f := copyFixture(t, notagsFLAC)

	withA := filepath.Join(t.TempDir(), "a.flac")
	if _, errb, code := runCLI(t, "set", f, "--add-cover", coverA, "-o", withA); code != 0 {
		t.Fatalf("add cover A exit = %d\n%s", code, errb)
	}
	withB := filepath.Join(t.TempDir(), "b.flac")
	if _, errb, code := runCLI(t, "set", withA, "--add-cover", coverB, "-o", withB); code != 0 {
		t.Fatalf("add cover B exit = %d\n%s", code, errb)
	}
	out, _, _ := runCLI(t, "dump", withB)
	if n := strings.Count(out, "Front cover"); n != 1 {
		t.Errorf("after a second --add-cover, dump shows %d front covers, want 1 (replace, not duplicate):\n%s", n, out)
	}
}

// Repeated --add-cover in one command: last wins (PNG vs JPEG MIME proves survivor).
func TestAddCoverLastWinsWithinInvocation(t *testing.T) {
	t.Parallel()
	coverA := writeTempImage(t, "a.png", minimalPNG())
	coverB := writeTempImage(t, "b.jpg", minimalJPEG())
	f := copyFixture(t, notagsFLAC)

	out := filepath.Join(t.TempDir(), "out.flac")
	if _, errb, code := runCLI(t, "set", f, "--add-cover", coverA, "--add-cover", coverB, "-o", out); code != 0 {
		t.Fatalf("two --add-cover exit = %d\n%s", code, errb)
	}
	dump, _, _ := runCLI(t, "dump", out)
	if n := strings.Count(dump, "Front cover"); n != 1 {
		t.Errorf("two --add-cover in one invocation: %d front covers, want 1 (last-wins):\n%s", n, dump)
	}
	if !strings.Contains(dump, "image/jpeg") || strings.Contains(dump, "image/png") {
		t.Errorf("last-wins should keep coverB (the JPEG), not coverA (the PNG):\n%s", dump)
	}
}

// Last-wins does not skip validation: a bad earlier --add-cover path fails before write.
func TestAddCoverValidatesSupersededPaths(t *testing.T) {
	t.Parallel()
	good := writeTempImage(t, "good.png", minimalPNG())
	missing := filepath.Join(t.TempDir(), "missing.png")
	f := copyFixture(t, notagsFLAC)

	_, errb, code := runCLI(t, "set", f, "--add-cover", missing, "--add-cover", good, "-o", filepath.Join(t.TempDir(), "out.flac"))
	if code == 0 {
		t.Errorf("a missing superseded --add-cover path must fail the invocation, got exit 0")
	}
	if !strings.Contains(errb, "cover image") && !strings.Contains(errb, "missing.png") {
		t.Errorf("error should name the bad cover input; got: %s", errb)
	}
}

// --add-picture front-cover= appends; replacement is scoped to --add-cover only.
func TestAddPictureFrontCoverAppends(t *testing.T) {
	t.Parallel()
	coverA := writeTempImage(t, "a.png", minimalPNG())
	coverB := writeTempImage(t, "b.png", append(minimalPNG(), 0x7A)) // distinct PNG
	f := copyFixture(t, notagsFLAC)

	withA := filepath.Join(t.TempDir(), "a.flac")
	if _, errb, code := runCLI(t, "set", f, "--add-cover", coverA, "-o", withA); code != 0 {
		t.Fatalf("add cover A exit = %d\n%s", code, errb)
	}
	both := filepath.Join(t.TempDir(), "both.flac")
	if _, errb, code := runCLI(t, "set", withA, "--add-picture", "front-cover="+coverB, "-o", both); code != 0 {
		t.Fatalf("add-picture front-cover exit = %d\n%s", code, errb)
	}
	out, _, _ := runCLI(t, "dump", both)
	if n := strings.Count(out, "Front cover"); n != 2 {
		t.Errorf("--add-picture front-cover should append; dump shows %d front covers, want 2 (existing one preserved):\n%s", n, out)
	}
}

// MP4 cover art is image-only: non-front role or description warns; plain front cover and FLAC do not.
func TestMP4PictureMetadataDropped(t *testing.T) {
	t.Parallel()
	notagsM4A := filepath.Join("..", "..", "testdata", "notags.m4a")
	png := writeTempImage(t, "c.png", minimalPNG())

	out, _, code := runCLI(t, "plan", copyFixture(t, notagsM4A), "--add-picture", "back-cover="+png, "--picture-description", "rear")
	if code != 0 {
		t.Fatalf("plan exit = %d", code)
	}
	if !strings.Contains(out, "picture-metadata-dropped") {
		t.Errorf("MP4 back-cover/description should warn picture-metadata-dropped:\n%s", out)
	}
	if out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--add-cover", png); strings.Contains(out, "picture-metadata-dropped") {
		t.Errorf("MP4 plain front cover should not warn picture-metadata-dropped:\n%s", out)
	}
	if out, _, _ := runCLI(t, "plan", sampleFLAC, "--add-picture", "back-cover="+png, "--picture-description", "rear"); strings.Contains(out, "picture-metadata-dropped") {
		t.Errorf("FLAC back-cover/description should not warn:\n%s", out)
	}
}

// Default policy warns legacy-conflict on stale id3v1; --legacy strip removes the copy.
func TestLegacyConflictWarningCLI(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "plan", copyFixture(t, sampleMP3), "--set", "TITLE=Brand New")
	if code != 0 {
		t.Fatalf("plan exit = %d", code)
	}
	if !strings.Contains(out, "legacy-conflict") {
		t.Errorf("plan should warn legacy-conflict on a stale id3v1 copy:\n%s", out)
	}
	if out, _, _ := runCLI(t, "plan", copyFixture(t, sampleMP3), "--set", "TITLE=Brand New", "--legacy", "strip"); strings.Contains(out, "legacy-conflict") {
		t.Errorf("--legacy strip should resolve the conflict, not warn:\n%s", out)
	}
}

// 0-byte image refused even with --force; non-empty unsniffable bytes embed under --force.
func TestZeroByteImageRefused(t *testing.T) {
	t.Parallel()
	empty := writeTempImage(t, "empty.jpg", nil)
	if _, errb, code := runCLI(t, "plan", sampleFLAC, "--add-cover", empty, "--force"); code != 2 || !strings.Contains(errb, "file is empty") {
		t.Errorf("0-byte cover with --force: exit %d (want 2), stderr:\n%s", code, errb)
	}
	junk := writeTempImage(t, "junk.jpg", []byte("NOT-AN-IMAGE"))
	out, _, code := runCLI(t, "plan", sampleFLAC, "--add-cover", junk, "--force")
	if code != 0 {
		t.Fatalf("non-empty unsniffable cover with --force exit = %d, want 0", code)
	}
	if !strings.Contains(out, "application/octet-stream") {
		t.Errorf("plan should make the unsniffable cover's MIME visible:\n%s", out)
	}
}

func TestAddedPictureDetailInPlan(t *testing.T) {
	t.Parallel()
	png := writeTempImage(t, "c.png", minimalPNG())
	out, _, code := runCLI(t, "plan", copyFixture(t, notagsFLAC), "--add-cover", png)
	if code != 0 {
		t.Fatalf("plan exit = %d", code)
	}
	if !strings.Contains(out, "+ pictures: 1") {
		t.Errorf("plan should report the added picture count:\n%s", out)
	}
	if !strings.Contains(out, "Front cover") || !strings.Contains(out, "image/png") {
		t.Errorf("plan should detail the added picture (type + MIME):\n%s", out)
	}
}

func TestLintMalformedNumber(t *testing.T) {
	t.Parallel()
	bad := filepath.Join(t.TempDir(), "bad.flac")
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--set", "TRACKNUMBER=abc", "-o", bad); code != 0 {
		t.Fatalf("set TRACKNUMBER=abc exit = %d, want 0 (written faithfully)", code)
	}
	out, _, code := runCLI(t, "lint", bad)
	if code != 1 {
		t.Fatalf("lint exit = %d, want 1 (issues found)\n%s", code, out)
	}
	if !strings.Contains(out, "malformed-number") {
		t.Errorf("lint should report malformed-number:\n%s", out)
	}
}

// Present-empty numerics: set writes with empty-value advisory; lint must not flag malformed-number.
func TestLintSkipsEmptyNumericValue(t *testing.T) {
	t.Parallel()
	f := filepath.Join(t.TempDir(), "empty.flac")
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--set", "TRACKNUMBER=", "-o", f); code != 0 {
		t.Fatalf("set TRACKNUMBER= exit = %d, want 0", code)
	}
	out, _, code := runCLI(t, "lint", f)
	if code != 0 || strings.Contains(out, "malformed-number") {
		t.Errorf("empty TRACKNUMBER: lint exit %d, out %q; want 0 and no malformed-number (empty is not malformed)", code, out)
	}
}

// Tag header conflict count must match rows shown.
func TestConflictCountInTagHeader(t *testing.T) {
	t.Parallel()
	ts := tag.NewTagSet()
	ts.Add(tag.Title, "a", "b") // single-valued key, two values
	var buf bytes.Buffer
	renderTags(&buf, ts)
	if got := buf.String(); !strings.Contains(got, "1 in conflict") {
		t.Errorf("tag header should report the conflict count:\n%s", got)
	}
}
