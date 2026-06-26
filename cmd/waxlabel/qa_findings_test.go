package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// TestSetAddChapterDedupsAcrossLanguageField is a QA-review regression: --add-chapter dedups
// on the fields the CLI can author (start/end/title), ignoring the parse-derived chapter
// language. A Matroska chapter carrying a language must not defeat that dedup and write a
// duplicate when the user re-adds the same start/title. The CLI has no language syntax, so the
// language-carrying chapter is authored through the library for the fixture.
func TestSetAddChapterDedupsAcrossLanguageField(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	doc, err := wl.ParseFile(ctx, filepath.Join("..", "..", "testdata", "sample.mka"))
	if err != nil {
		t.Fatal(err)
	}
	// One open-ended (End == 0) chapter carrying a language, matching what --add-chapter "0:01=X"
	// would produce except for the language the CLI cannot set.
	plan, err := doc.Edit().SetChapters(wl.Chapter{Start: time.Second, Title: "X", Language: "eng"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(t.TempDir(), "lang.mka")
	if _, _, err := plan.Execute(ctx, wl.SaveAsFile(tmp)); err != nil {
		t.Fatal(err)
	}

	if _, _, code := runCLI(t, "set", tmp, "--add-chapter", "0:01=X"); code != 0 {
		t.Fatalf("set --add-chapter exit = %d, want 0", code)
	}
	out, _, code := runCLI(t, "dump", tmp)
	if code != 0 {
		t.Fatalf("dump exit = %d, want 0", code)
	}
	if !strings.Contains(out, "chapters (1)") {
		t.Errorf("re-adding an existing chapter wrote a duplicate (language defeated the dedup):\n%s", out)
	}
}

// TestCapsEmptyNoAudioExit4 is the F6 regression for caps: a file with no decodable audio
// essence still prints its capability report, then exits 4 (invalid-data) - the same verdict
// dump/verify/set/lint reach, removing the read-command exit-0 outlier.
func TestCapsEmptyNoAudioExit4(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "caps", emptyMP3)
	if code != 4 {
		t.Fatalf("caps no-audio exit = %d, want 4", code)
	}
	if !strings.Contains(out, "format:") {
		t.Errorf("caps must still print the capability report before exiting 4:\n%s", out)
	}
}

// TestCapsFormatADTSAlias is the Codex polish: caps accepts "adts" as an alias for aac.
func TestCapsFormatADTSAlias(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "caps", "--format", "adts")
	if code != 0 {
		t.Fatalf("caps --format adts exit = %d, want 0", code)
	}
	if !strings.Contains(out, "AAC") {
		t.Errorf("caps --format adts should describe the AAC format:\n%s", out)
	}
}

// TestNumericGenreWriteWarnAsymmetry is the F10 numeric-genre regression: setting a bare
// numeric GENRE warns on an ID3 target (it reads back as the genre NAME) but not on a
// Vorbis target (it stays the literal number), discoverable at the point of action.
func TestNumericGenreWriteWarnAsymmetry(t *testing.T) {
	t.Parallel()
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")
	notagsFLAC := filepath.Join("..", "..", "testdata", "notags.flac")

	mp3Out, _, code := runCLI(t, "plan", notagsMP3, "--set", "GENRE=17")
	if code != 0 {
		t.Fatalf("plan MP3 GENRE=17 exit = %d, want 0", code)
	}
	if !strings.Contains(mp3Out, "numeric-genre") {
		t.Errorf("setting a bare numeric GENRE on an ID3 target must warn numeric-genre:\n%s", mp3Out)
	}

	flacOut, _, code := runCLI(t, "plan", notagsFLAC, "--set", "GENRE=17")
	if code != 0 {
		t.Fatalf("plan FLAC GENRE=17 exit = %d, want 0", code)
	}
	if strings.Contains(flacOut, "numeric-genre") {
		t.Errorf("a Vorbis target keeps GENRE=17 verbatim and must NOT warn numeric-genre:\n%s", flacOut)
	}

	// The detection matches the read path's resolver, so parenthesised and special-code
	// references warn too (they also read back as a name), not just a bare number.
	for _, ref := range []string{"(17)", "(RX)"} {
		out, _, code := runCLI(t, "plan", notagsMP3, "--set", "GENRE="+ref)
		if code != 0 {
			t.Fatalf("plan MP3 GENRE=%s exit = %d, want 0", ref, code)
		}
		if !strings.Contains(out, "numeric-genre") {
			t.Errorf("GENRE=%s on an ID3 target reads back as a name and must warn numeric-genre:\n%s", ref, out)
		}
	}
}

// TestSetOutputExistsBeatsWritabilityProbe is a QA-review regression: when an existing -o
// target (no --overwrite) sits in a read-only directory, the actionable "already exists; pass
// --overwrite" (exit 2) must win over the writability probe's I/O error (exit 6) - the probe
// must not pre-empt the more useful refusal, nor touch the filesystem on a refused write.
func TestSetOutputExistsBeatsWritabilityProbe(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not prevent the write")
	}
	dir := t.TempDir()
	existing := filepath.Join(dir, "out.flac")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	_, errb, code := runCLI(t, "set", sampleFLAC, "--set", "TITLE=X", "-o", existing)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (already-exists wins over the writability probe)", code)
	}
	if !strings.Contains(errb, "already exists") {
		t.Errorf("expected the actionable already-exists hint, got: %q", errb)
	}
}
