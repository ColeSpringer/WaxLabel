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

// TestVersionSubcommand checks that `waxlabel version` prints the same line as --version.
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

// TestDashPathHint checks that an unknown flag shaped like a leading-dash file path
// suggests the "--" end-of-flags marker for both shorthand and long forms.
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

// TestPaddingZeroCombinesWithNoPadding checks that every spelling of zero combines
// with --no-padding, while a positive --padding still conflicts.
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

// TestStdinUnidentifiedNamesStdin checks that unidentifiable stdin is reported as
// "<stdin>" and never leaks the buffered temp path.
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

// TestPaddingNotePerFormat checks set/plan notes for padding flags that do not apply
// to a file's format. WAV gets a note; FLAC and MP3/AAC/MP4 honor the flags, so they
// get none. In particular, MP3 --no-padding shrinks via rewrite and must not claim
// "no effect".
func TestPaddingNotePerFormat(t *testing.T) {
	t.Parallel()
	wav := copyFixture(t, sampleWAV)
	if _, errb, _ := runCLI(t, "set", wav, "--no-padding", "-o", filepath.Join(t.TempDir(), "o.wav")); !strings.Contains(errb, "does not apply to WAV") {
		t.Errorf("WAV --no-padding should note it does not apply:\n%s", errb)
	}
	// MP3 honors the padding flags (--no-padding shrinks via a rewrite), so neither
	// --no-padding nor --padding N should draw a "does not apply / no effect" note.
	for _, flags := range [][]string{{"--no-padding"}, {"--padding", "30000"}} {
		args := append([]string{"set", copyFixture(t, sampleMP3)}, flags...)
		args = append(args, "-o", filepath.Join(t.TempDir(), "o.mp3"))
		if _, errb, _ := runCLI(t, args...); strings.Contains(errb, "does not apply") || strings.Contains(errb, "no effect") {
			t.Errorf("MP3 %v should get no padding note:\n%s", flags, errb)
		}
	}
	// FLAC honors both controls, so no note.
	if _, errb, _ := runCLI(t, "plan", sampleFLAC, "--no-padding"); strings.Contains(errb, "does not apply") || strings.Contains(errb, "no effect") {
		t.Errorf("FLAC --no-padding should get no padding note:\n%s", errb)
	}
}

// paddingLineHas reports whether caps's "padding:" line contains want, anchoring the
// level check so it cannot match "full"/"none"/"partial" printed on an unrelated
// dimension line (fields/pictures/chapters).
func paddingLineHas(capsOut, want string) bool {
	for _, line := range strings.Split(capsOut, "\n") {
		if strings.Contains(line, "padding:") {
			return strings.Contains(line, want)
		}
	}
	return false
}

// TestCapsPaddingLevel checks that caps reports the per-format padding level in text
// and JSON.
func TestCapsPaddingLevel(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ format, level string }{
		{"flac", "full"}, {"wav", "none"}, {"mp3", "partial"},
	} {
		out, _, code := runCLI(t, "caps", "--format", c.format)
		if code != 0 {
			t.Fatalf("caps --format %s exit = %d", c.format, code)
		}
		// Anchor the level to the padding: line - "full"/"none"/"partial" also appear on
		// the fields/pictures/chapters lines, so an unanchored Contains would pass even if
		// renderCaps printed the wrong padding level.
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

// TestUnknownKeySuggestions checks that near-miss --set and --clear keys draw a
// "did you mean?" suggestion, and that a typo'd --clear is surfaced instead of
// becoming a silent no-op.
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

// TestValueNotes checks that a non-boolean COMPILATION and a negative numbering value
// each draw an advisory note while still being written.
func TestValueNotes(t *testing.T) {
	t.Parallel()
	if _, errb, _ := runCLI(t, "plan", sampleFLAC, "--set", "COMPILATION=maybe"); !strings.Contains(errb, "does not look like a boolean") {
		t.Errorf("COMPILATION=maybe should note non-boolean:\n%s", errb)
	}
	if _, errb, _ := runCLI(t, "plan", sampleFLAC, "--set", "TRACKNUMBER=-3"); !strings.Contains(errb, "is negative") {
		t.Errorf("TRACKNUMBER=-3 should note negative:\n%s", errb)
	}
}

// TestLintSetAgreeOnWhitespaceNumeric checks that lint and set treat a whitespace-only or
// space-padded numeric the same way: set trims the value before deciding, so a whitespace-only
// TRACKNUMBER is the benign empty-value case (exit 0), and lint must reach the same verdict
// rather than validating the untrimmed value and flagging a malformed number.
func TestLintSetAgreeOnWhitespaceNumeric(t *testing.T) {
	t.Parallel()

	// A whitespace-only numeric: set writes the benign empty value (exit 0), lint stays clean.
	f := copyFixture(t, "../../testdata/notags.flac")
	if _, errb, code := runCLI(t, "set", f, "--set", "TRACKNUMBER=   "); code != 0 {
		t.Fatalf("set TRACKNUMBER=whitespace exit %d: %s", code, errb)
	}
	if out, _, code := runCLI(t, "lint", f); code != 0 {
		t.Errorf("lint of a whitespace-only TRACKNUMBER = exit %d, want 0 (clean, matching set)\n%s", code, out)
	}

	// A space-padded number validates on its trimmed form and stores the trimmed value.
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

// TestPictureEditing adds a roled picture with a description, then removes it by role.
// Description-only and unknown-role misuses remain usage errors.
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
	// --picture-description with nothing to attach to is a usage error.
	if _, _, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--picture-description", "x", "-o", filepath.Join(t.TempDir(), "n.flac")); code != 2 {
		t.Errorf("--picture-description alone exit = %d, want 2", code)
	}
	// An unknown role is a usage error listing the valid roles.
	_, errb, code := runCLI(t, "set", copyFixture(t, notagsFLAC), "--add-picture", "bogus="+png, "-o", filepath.Join(t.TempDir(), "b.flac"))
	if code != 2 || !strings.Contains(errb, "valid roles") {
		t.Errorf("unknown role exit = %d (want 2), stderr:\n%s", code, errb)
	}
}

// TestPictureRoleVocabulary pins the role names --add-picture accepts. The table is
// derived from PictureType.String(), so this guards against a String() reformat
// silently renaming a role and breaking front-cover= / back-cover= etc.
func TestPictureRoleVocabulary(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]wl.PictureType{
		"front-cover": wl.PicFrontCover, // also the --add-cover alias target
		"back-cover":  wl.PicBackCover,
		"artist":      wl.PicArtist,
		"lead-artist": wl.PicLeadArtist, // must stay distinct from "artist"
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

// TestAddCoverReplacesFrontCover verifies that --add-cover replaces an existing
// front cover instead of appending a duplicate.
func TestAddCoverReplacesFrontCover(t *testing.T) {
	t.Parallel()
	coverA := writeTempImage(t, "a.png", minimalPNG())
	coverB := writeTempImage(t, "b.png", append(minimalPNG(), 0x7A)) // distinct bytes, still a valid PNG header
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

// TestAddCoverLastWinsWithinInvocation checks that repeated --add-cover flags in one
// command produce a single front cover, with the final flag winning. coverA is a PNG
// and coverB is a JPEG, so the surviving MIME shows which one remained.
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

// TestAddCoverValidatesSupersededPaths checks that last-wins selection does not skip
// validation. A bad earlier --add-cover path must fail the command before any output is
// written, even if a later path would replace it.
func TestAddCoverValidatesSupersededPaths(t *testing.T) {
	t.Parallel()
	good := writeTempImage(t, "good.png", minimalPNG())
	missing := filepath.Join(t.TempDir(), "missing.png")
	f := copyFixture(t, notagsFLAC)

	// The superseded first path is missing, so the invocation must fail.
	_, errb, code := runCLI(t, "set", f, "--add-cover", missing, "--add-cover", good, "-o", filepath.Join(t.TempDir(), "out.flac"))
	if code == 0 {
		t.Errorf("a missing superseded --add-cover path must fail the invocation, got exit 0")
	}
	if !strings.Contains(errb, "cover image") && !strings.Contains(errb, "missing.png") {
		t.Errorf("error should name the bad cover input; got: %s", errb)
	}
}

// TestAddPictureFrontCoverAppends verifies that --add-picture front-cover=...
// keeps its documented append semantics. The replacement policy is scoped to
// --add-cover, so a deliberate second front cover is preserved.
func TestAddPictureFrontCoverAppends(t *testing.T) {
	t.Parallel()
	coverA := writeTempImage(t, "a.png", minimalPNG())
	coverB := writeTempImage(t, "b.png", append(minimalPNG(), 0x7A)) // distinct bytes, still a valid PNG header
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

// TestMP4PictureMetadataDropped: MP4 stores cover art as image data only, so adding a
// non-front role or a description warns that they will not be preserved - the saved
// file must not silently differ from the previewed edit. A plain front cover (no role,
// no description) and a format that does preserve them (FLAC) draw no such warning.
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
	// A plain front cover (the round-tripping case) draws no warning.
	if out, _, _ := runCLI(t, "plan", copyFixture(t, notagsM4A), "--add-cover", png); strings.Contains(out, "picture-metadata-dropped") {
		t.Errorf("MP4 plain front cover should not warn picture-metadata-dropped:\n%s", out)
	}
	// FLAC preserves role and description, so no warning there.
	if out, _, _ := runCLI(t, "plan", sampleFLAC, "--add-picture", "back-cover="+png, "--picture-description", "rear"); strings.Contains(out, "picture-metadata-dropped") {
		t.Errorf("FLAC back-cover/description should not warn:\n%s", out)
	}
}

// TestLegacyConflictWarningCLI verifies that editing a key also held in an MP3's
// preserved id3v1 trailer surfaces the legacy-conflict warning under the default
// policy, and that --legacy strip suppresses it by removing the legacy copy.
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

// TestZeroByteImageRefused checks that a 0-byte image is refused even with --force,
// unlike non-empty unsniffable bytes, which --force embeds and the plan makes visible.
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

// TestAddedPictureDetailInPlan checks that the plan lists an added picture's
// type/MIME/size beneath the picture-count change.
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

// TestLintMalformedNumber checks that a non-numeric track number is flagged, flipping
// a previously clean file to a non-zero lint exit.
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

// TestLintSkipsEmptyNumericValue checks that lint agrees with set for present-empty
// numeric values: set writes them with an empty-value advisory, so lint must not later
// call them malformed numbers.
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

// TestConflictCountInTagHeader checks that the dump tag header counts conflicting
// single-valued keys, so the header count matches the rows shown.
func TestConflictCountInTagHeader(t *testing.T) {
	t.Parallel()
	ts := tag.NewTagSet()
	ts.Add(tag.Title, "a", "b") // a known single-valued key with two values: a conflict
	var buf bytes.Buffer
	renderTags(&buf, ts)
	if got := buf.String(); !strings.Contains(got, "1 in conflict") {
		t.Errorf("tag header should report the conflict count:\n%s", got)
	}
}
