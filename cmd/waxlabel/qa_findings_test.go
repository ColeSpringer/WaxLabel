package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// --add-chapter dedups on the fields the CLI can author: start, end, and title.
// Parse-derived chapter language should not make the same user-authored chapter
// look distinct. The fixture is authored through the library because the CLI has
// no language syntax for chapters.
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

// caps reports container capability, not file health. A no-audio file can still
// have readable capabilities, so the command prints its report and exits 0.
func TestCapsEmptyNoAudioExit0(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "caps", emptyMP3)
	if code != 0 {
		t.Fatalf("caps no-audio exit = %d, want 0 (a successful capability read)", code)
	}
	if !strings.Contains(out, "format:") {
		t.Errorf("caps must still print the capability report:\n%s", out)
	}
}

// A no-audio file does not fail a dump batch. It is a successful metadata read
// with a warning, so the aggregate exit code stays 0 when all files are readable.
func TestDumpMixedBatchNoAudioExit0(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", sampleFLAC, emptyMP3)
	if code != 0 {
		t.Fatalf("dump good+no-audio exit = %d, want 0 (no-audio is a warning-only read)", code)
	}
	if !strings.Contains(out, "no-audio") {
		t.Errorf("dump batch missing the no-audio warning for the empty file:\n%s", out)
	}
}

// A corrupt file still dominates a dump batch at exit 4. The no-audio warning
// path must not weaken invalid-data results from files that cannot be parsed.
func TestDumpBatchCorruptStillDominates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A file the FLAC detector claims by its "fLaC" magic but cannot parse: invalid-data.
	bad := filepath.Join(dir, "garbage.flac")
	if err := os.WriteFile(bad, append([]byte("fLaC"), make([]byte, 64)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCLI(t, "dump", sampleFLAC, emptyMP3, bad); code != 4 {
		t.Errorf("dump good+no-audio+corrupt exit = %d, want 4 (corrupt dominates)", code)
	}
}

// caps follows the same batch contract as dump: a no-audio file with readable
// capabilities does not make the batch fail.
func TestCapsMixedBatchNoAudioExit0(t *testing.T) {
	t.Parallel()
	if _, _, code := runCLI(t, "caps", sampleFLAC, emptyMP3); code != 0 {
		t.Errorf("caps good+no-audio exit = %d, want 0", code)
	}
}

// In JSON mode, a no-audio file is a normal dump record with a warning. It is
// not represented as an error element.
func TestDumpJSONNoAudioNoError(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "dump", sampleFLAC, emptyMP3)
	if code != 0 {
		t.Fatalf("dump --json good+no-audio exit = %d, want 0", code)
	}
	docs := decodeJSONList[jsonDocument](t, out)
	if len(docs) != 2 {
		t.Fatalf("dump --json array len = %d, want 2\n%s", len(docs), out)
	}
	for i, d := range docs {
		if d.Error != nil {
			t.Errorf("element %d carries an error %+v; a no-audio file is a successful read", i, d.Error)
		}
	}
	sawNoAudio := false
	for _, d := range docs {
		for _, w := range d.Warnings {
			if w.Code == "no-audio" {
				sawNoAudio = true
			}
		}
	}
	if !sawNoAudio {
		t.Errorf("no element carried the no-audio warning:\n%s", out)
	}
}

// TestNoFilesNoteSuppressedUnderJSON checks that an empty --recursive walk under --json writes
// nothing to stderr (the stdout shape stays a clean []), matching the sibling noteSkipped, while
// text mode still prints the "no audio files found" advisory so an empty walk is not a silent
// success. It runs on both dump and lint, the read commands that walk directories.
func TestNoFilesNoteSuppressedUnderJSON(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"dump", "lint"} {
		cmd := cmd
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			empty := t.TempDir()

			// --json: nothing on stderr, exit 0.
			stdout, stderr, code := runCLI(t, "--json", cmd, "--recursive", empty)
			if code != 0 {
				t.Fatalf("%s --json empty walk exit = %d, want 0; stderr=%q", cmd, code, stderr)
			}
			if stderr != "" {
				t.Errorf("%s --json empty walk wrote to stderr: %q, want nothing (suppressed like noteSkipped)", cmd, stderr)
			}
			if strings.TrimSpace(stdout) != "[]" {
				t.Errorf("%s --json empty walk stdout = %q, want []", cmd, stdout)
			}

			// Text mode: the advisory still fires so the empty walk is not a silent no-op.
			_, textErr, code := runCLI(t, cmd, "--recursive", empty)
			if code != 0 {
				t.Fatalf("%s (text) empty walk exit = %d, want 0", cmd, code)
			}
			if !strings.Contains(textErr, "no audio files found") {
				t.Errorf("%s (text) empty walk stderr = %q, want the no-audio-files advisory", cmd, textErr)
			}
		})
	}
}

// TestWAVAIFFStructuralOpsGatedOnChange checks that WAV and AIFF gate each id3-container op
// (pictures/chapters/synced lyrics) on its own change flag and canonical model count, like MP3 and
// AAC. An edit that actually adds a container reports it with the model count
// (len(edited.SyncedLyrics)); a tag-only edit on a file already carrying those containers emits
// none of the ops, because they are carried through unchanged rather than rewritten.
func TestWAVAIFFStructuralOpsGatedOnChange(t *testing.T) {
	t.Parallel()
	cover := writeTempImage(t, "cover.png", minimalPNG())
	for _, tc := range []struct{ name, fixture string }{
		{"wav", "../../testdata/notags.wav"},
		{"aiff", "../../testdata/notags.aiff"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// An actual synced-lyrics edit still reports the op, with the model set count (1).
			if ops := planOperations(t, copyFixture(t, tc.fixture), "--add-synced-lyric", "0:01=Hi"); !slices.Contains(ops, "synced lyrics: 1") {
				t.Errorf("adding synced lyrics: operations = %v, want a 'synced lyrics: 1' op", ops)
			}

			// Author a file carrying synced lyrics, a cover, and a chapter.
			f := copyFixture(t, tc.fixture)
			if _, errb, code := runCLI(t, "set", f,
				"--add-synced-lyric", "0:01=Hi",
				"--add-cover", cover,
				"--add-chapter", "0:00=Intro"); code != 0 {
				t.Fatalf("authoring exit %d: %s", code, errb)
			}

			// A tag-only edit carries all three containers through unchanged, so none of their
			// per-container ops appear - only the tag rewrite itself.
			ops := planOperations(t, f, "--set", "TITLE=Hello")
			for _, op := range ops {
				if strings.HasPrefix(op, "synced lyrics:") || strings.HasPrefix(op, "pictures:") || strings.HasPrefix(op, "chapters:") {
					t.Errorf("tag-only edit emitted a spurious container op %q; operations=%v", op, ops)
				}
			}
		})
	}
}

// TestMP3PictureClearOmitsZeroCountOp checks that clearing pictures on MP3 (through the shared
// RenderFrontTag) emits no "pictures: 0" op, matching the chapters and synced-lyrics lines and the
// WAV/AIFF gate, so all four codecs read uniformly. The removal is still recorded as a change, so
// nothing is silently lost.
func TestMP3PictureClearOmitsZeroCountOp(t *testing.T) {
	t.Parallel()
	cover := writeTempImage(t, "cover.png", minimalPNG())
	f := copyFixture(t, "../../testdata/notags.mp3")
	// Keep a TITLE so the write is a frame rewrite, not a full tag removal - isolating the
	// picture-line behavior rather than the removal path.
	if _, errb, code := runCLI(t, "set", f, "--set", "TITLE=Keep", "--add-cover", cover); code != 0 {
		t.Fatalf("authoring exit %d: %s", code, errb)
	}
	ops := planOperations(t, f, "--remove-pictures")
	for _, op := range ops {
		if strings.HasPrefix(op, "pictures:") {
			t.Errorf("picture-clear emitted %q; 'pictures: 0' must be suppressed. operations=%v", op, ops)
		}
	}
}

// TestNoOpJSONOperationsEmpty checks that a no-op plan/set/copy emits "operations": [] in
// JSON, not the human "no changes" sentinel: the operations array is defined as the structural
// write list (README), and a no-op writes nothing. This brings plan/set/copy in line with
// lint --fix, which already normalizes a no-op to [].
func TestNoOpJSONOperationsEmpty(t *testing.T) {
	t.Parallel()

	type report struct {
		NoOp       bool     `json:"noOp"`
		Operations []string `json:"operations"`
	}
	assertEmpty := func(name string, r report) {
		t.Helper()
		if !r.NoOp {
			t.Errorf("%s: noOp = false, want true (setup did not produce a no-op)", name)
		}
		if len(r.Operations) != 0 {
			t.Errorf("%s: operations = %v, want [] (not the 'no changes' sentinel)", name, r.Operations)
		}
	}

	// plan with no edits is a pure no-op.
	planF := copyFixture(t, "../../testdata/notags.flac")
	out, errb, code := runCLI(t, "--json", "plan", planF)
	if code != 0 {
		t.Fatalf("plan exit %d: %s", code, errb)
	}
	assertEmpty("plan", decodeJSONOne[report](t, out))

	// set to the value the file already holds is a no-op.
	setF := copyFixture(t, "../../testdata/notags.flac")
	if _, errb, code := runCLI(t, "set", setF, "--set", "TITLE=Same"); code != 0 {
		t.Fatalf("authoring set exit %d: %s", code, errb)
	}
	out, errb, code = runCLI(t, "--json", "set", setF, "--set", "TITLE=Same")
	if code != 0 {
		t.Fatalf("no-op set exit %d: %s", code, errb)
	}
	assertEmpty("set", decodeJSONOne[report](t, out))

	// copy of a metadata-free source onto a copy of itself carries nothing: a no-op copy. Copy
	// emits a single JSON object (not the list plan/set do), so decode it directly.
	copyDst := copyFixture(t, "../../testdata/notags.flac")
	cout, errb, code := runCLI(t, "--json", "copy", "../../testdata/notags.flac", copyDst)
	if code != 0 {
		t.Fatalf("no-op copy exit %d: %s", code, errb)
	}
	var jc report
	if err := json.Unmarshal([]byte(cout), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s", err, cout)
	}
	assertEmpty("copy", jc)
}

// planOperations runs `plan --json <file> <args...>` and returns the single report's operations.
func planOperations(t *testing.T, file string, args ...string) []string {
	t.Helper()
	out, errb, code := runCLI(t, append([]string{"--json", "plan", file}, args...)...)
	if code != 0 {
		t.Fatalf("plan exit %d: %s", code, errb)
	}
	return decodeJSONOne[struct {
		Operations []string `json:"operations"`
	}](t, out).Operations
}

// caps has no warnings field, so JSON mode represents a readable no-audio file
// as a clean capability record.
func TestCapsJSONNoAudioNoError(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "caps", emptyMP3)
	if code != 0 {
		t.Fatalf("caps --json no-audio exit = %d, want 0", code)
	}
	jc := decodeJSONOne[jsonCaps](t, out)
	if jc.Error != nil {
		t.Errorf("caps --json no-audio carries an error %+v, want none", jc.Error)
	}
}

// caps accepts "adts" as an alias for aac.
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

// A bare numeric GENRE warns on an ID3 target because it reads back as the genre
// name. Formats that keep the value literally - Vorbis (FLAC), and WAV in its LIST/INFO
// IGNR slot - should not warn. AIFF stores genre only in its ID3 chunk, so it warns like
// MP3/AAC (it is not exempt the way WAV is, despite both carrying a native container).
func TestNumericGenreWriteWarnAsymmetry(t *testing.T) {
	t.Parallel()
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// Every target where the genre resolves to a name on read must warn: MP3/AAC (pure ID3) and
	// AIFF (ID3 chunk; no native genre text chunk to keep the literal number).
	for _, name := range []string{"notags.mp3", "notags.aac", "notags.aiff"} {
		fix := filepath.Join("..", "..", "testdata", name)
		out, _, code := runCLI(t, "plan", fix, "--set", "GENRE=17")
		if code != 0 {
			t.Fatalf("plan %s GENRE=17 exit = %d, want 0", name, code)
		}
		if !strings.Contains(out, "numeric-genre") {
			t.Errorf("a bare numeric GENRE on %s must warn numeric-genre (it reads back as the name):\n%s", name, out)
		}
	}

	// Targets that keep "17" verbatim must not warn: Vorbis (FLAC) and WAV's LIST/INFO IGNR.
	for _, name := range []string{"notags.flac", "notags.wav"} {
		fix := filepath.Join("..", "..", "testdata", name)
		out, _, code := runCLI(t, "plan", fix, "--set", "GENRE=17")
		if code != 0 {
			t.Fatalf("plan %s GENRE=17 exit = %d, want 0", name, code)
		}
		if strings.Contains(out, "numeric-genre") {
			t.Errorf("%s keeps GENRE=17 verbatim and must not warn numeric-genre:\n%s", name, out)
		}
	}

	// A literal value beginning with "(" is escaped on write, so it round-trips verbatim
	// instead of being resolved to a genre name. The warning and the round-trip should
	// agree on that behavior.
	for _, ref := range []string{"(17)", "(RX)", "(2020) Best Of"} {
		out, _, code := runCLI(t, "plan", notagsMP3, "--set", "GENRE="+ref)
		if code != 0 {
			t.Fatalf("plan MP3 GENRE=%s exit = %d, want 0", ref, code)
		}
		if strings.Contains(out, "numeric-genre") {
			t.Errorf("GENRE=%s round-trips verbatim and should not warn numeric-genre:\n%s", ref, out)
		}

		f := copyFixture(t, notagsMP3)
		if _, stderr, code := runCLI(t, "set", f, "--set", "GENRE="+ref); code != 0 {
			t.Fatalf("set GENRE=%s exit = %d: %s", ref, code, stderr)
		}
		dumped, _, code := runCLI(t, "--json", "dump", f)
		if code != 0 {
			t.Fatalf("dump after GENRE=%s exit = %d", ref, code)
		}
		if !strings.Contains(dumped, `"`+ref+`"`) {
			t.Errorf("GENRE=%s did not round-trip verbatim:\n%s", ref, dumped)
		}
	}
}

// TestNumberTotalNonNumericDropsTotalCLI covers end to end on an ID3 target: a non-numeric
// TRACKNUMBER plus a canonical TRACKTOTAL cannot compose "n/total" (the reader would read "A1/12" as
// one literal value with the total lost), so the number is written verbatim, the total is dropped
// with a value-dropped warning, and it reads back as "A1" - never the corrupt "A1/12". The write is
// idempotent, and a literal "A1/12" set alone is preserved verbatim and unwarned.
func TestNumberTotalNonNumericDropsTotalCLI(t *testing.T) {
	t.Parallel()
	notagsMP3 := filepath.Join("..", "..", "testdata", "notags.mp3")

	// plan surfaces the value-dropped warning keyed to TRACKTOTAL.
	planOut, _, code := runCLI(t, "plan", notagsMP3, "--set", "TRACKNUMBER=A1", "--set", "TRACKTOTAL=12")
	if code != 0 {
		t.Fatalf("plan exit = %d, want 0", code)
	}
	if !strings.Contains(planOut, "value-dropped") || !strings.Contains(planOut, "TRACKTOTAL") {
		t.Errorf("a non-numeric number dropping a canonical total must warn value-dropped on TRACKTOTAL:\n%s", planOut)
	}

	f := copyFixture(t, notagsMP3)
	if _, stderr, code := runCLI(t, "set", f, "--set", "TRACKNUMBER=A1", "--set", "TRACKTOTAL=12"); code != 0 {
		t.Fatalf("set exit = %d: %s", code, stderr)
	}
	dumped, _, code := runCLI(t, "--json", "dump", f)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if strings.Contains(dumped, "A1/12") {
		t.Errorf("TRACKNUMBER must read back as \"A1\", not the corrupt \"A1/12\":\n%s", dumped)
	}
	if !strings.Contains(dumped, `"TRACKNUMBER"`) || strings.Contains(dumped, `"TRACKTOTAL"`) {
		t.Errorf("want TRACKNUMBER=A1 present and TRACKTOTAL absent (dropped):\n%s", dumped)
	}

	// Idempotent: re-setting the retained TRACKNUMBER=A1 changes nothing.
	before, _ := os.ReadFile(f)
	if _, stderr, code := runCLI(t, "set", f, "--set", "TRACKNUMBER=A1"); code != 0 {
		t.Fatalf("second set exit = %d: %s", code, stderr)
	}
	if after, _ := os.ReadFile(f); !bytes.Equal(before, after) {
		t.Error("re-setting the retained TRACKNUMBER changed bytes (not idempotent)")
	}

	// A literal "A1/12" as the number alone is preserved verbatim and not warned.
	planVerbatim, _, _ := runCLI(t, "plan", notagsMP3, "--set", "TRACKNUMBER=A1/12")
	if strings.Contains(planVerbatim, "value-dropped") {
		t.Errorf("a verbatim TRACKNUMBER=A1/12 must not warn value-dropped:\n%s", planVerbatim)
	}
	g := copyFixture(t, notagsMP3)
	if _, stderr, code := runCLI(t, "set", g, "--set", "TRACKNUMBER=A1/12"); code != 0 {
		t.Fatalf("set A1/12 exit = %d: %s", code, stderr)
	}
	if dumped, _, _ := runCLI(t, "--json", "dump", g); !strings.Contains(dumped, "A1/12") {
		t.Errorf("TRACKNUMBER=A1/12 alone must round-trip verbatim:\n%s", dumped)
	}
}

// TestReservedChapterKeyDroppedWithWarning: a custom key in the reserved CHAPTERxxx
// namespace cannot be written as a Vorbis custom field (on read the chapter model owns it), so
// setting CHAPTER005=hijack on a FLAC must warn value-dropped and leave the key absent from the tag
// view - not claim it was written and then lose it silently.
func TestReservedChapterKeyDroppedWithWarning(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, filepath.Join("..", "..", "testdata", "notags.flac"))
	out, _, code := runCLI(t, "set", f, "--set", "CHAPTER005=hijack")
	if code != 0 {
		t.Fatalf("set exit = %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "value-dropped") || !strings.Contains(out, "CHAPTER005") {
		t.Errorf("setting a reserved CHAPTERxxx key must warn value-dropped:\n%s", out)
	}
	dumped, _, code := runCLI(t, "--json", "dump", f)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if strings.Contains(dumped, "CHAPTER005") || strings.Contains(dumped, "hijack") {
		t.Errorf("the reserved key must be absent from the tag view, but leaked into the file:\n%s", dumped)
	}
}

// TestSetTrimsMediaTypeAndReplayGain covers the set path: MEDIATYPE and REPLAYGAIN_* are
// single-token values, so surrounding whitespace in a --set value is trimmed before storage the same
// way it is for numeric and date keys, while the internal space in "-7.30 dB" survives.
func TestSetTrimsMediaTypeAndReplayGain(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, filepath.Join("..", "..", "testdata", "notags.flac"))
	if _, stderr, code := runCLI(t, "set", f, "--set", "MEDIATYPE= 2 ", "--set", "REPLAYGAIN_TRACK_GAIN= -7.30 dB "); code != 0 {
		t.Fatalf("set exit = %d: %s", code, stderr)
	}
	j, _, _ := runCLI(t, "--json", "dump", f)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "MEDIATYPE"); len(got) != 1 || got[0] != "2" {
		t.Errorf("MEDIATYPE = %v, want [\"2\"]", got)
	}
	if got := tagValues(jd, "REPLAYGAIN_TRACK_GAIN"); len(got) != 1 || got[0] != "-7.30 dB" {
		t.Errorf("REPLAYGAIN_TRACK_GAIN = %v, want [\"-7.30 dB\"]", got)
	}
}

// TestMalformedYearDroppedNotTruncated covers an ID3v2.3 target: a malformed 5-digit
// year and a non-canonical compact date have no valid 4-digit year, so they must be dropped with a
// value-dropped warning rather than silently truncated to a valid-but-wrong "1000"/"2021".
func TestMalformedYearDroppedNotTruncated(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"10000", "20210503"} {
		f := copyFixture(t, filepath.Join("..", "..", "testdata", "notags.mp3"))
		out, _, code := runCLI(t, "set", f, "--set", "RECORDINGDATE="+v)
		if code != 0 {
			t.Fatalf("set RECORDINGDATE=%s exit = %d:\n%s", v, code, out)
		}
		if !strings.Contains(out, "value-dropped") || !strings.Contains(out, "4-digit year") {
			t.Errorf("RECORDINGDATE=%s must warn value-dropped (no valid 4-digit year):\n%s", v, out)
		}
		dumped, _, _ := runCLI(t, "--json", "dump", f)
		if strings.Contains(dumped, "1000") || strings.Contains(dumped, `"RECORDINGDATE"`) {
			t.Errorf("RECORDINGDATE=%s stored a wrong/truncated year instead of dropping:\n%s", v, dumped)
		}
	}
}

// When an existing -o target is used without --overwrite, the actionable
// already-exists refusal should win over any writability probe error. The probe
// must not preempt the clearer refusal or touch the target path.
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

// Bare DISC/TRACK and spaced or underscored ALBUM ARTIST spellings resolve to
// canonical keys. DISC/TRACK are outside ClosestKey's suggestion distance, so
// aliases keep them from being treated as custom fields.
func TestSetBareDiscTrackAliasResolves(t *testing.T) {
	// --strict now succeeds for each bare/aliased spelling.
	for _, kv := range []string{"DISC=1", "TRACK=2", "ALBUM ARTIST=The Band", "ALBUM_ARTIST=The Band"} {
		f := copyFixture(t, sampleFLAC)
		if _, stderr, code := runCLI(t, "set", f, "--set", kv, "--strict", "-q"); code != 0 {
			t.Errorf("set %q --strict: code=%d (want 0), stderr=%q", kv, code, stderr)
		}
	}

	// Non-strict: no custom-field note, and the values land on the canonical keys.
	f := copyFixture(t, sampleFLAC)
	_, stderr, code := runCLI(t, "set", f, "--set", "DISC=1", "--set", "TRACK=2")
	if code != 0 {
		t.Fatalf("set DISC/TRACK: code=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, "custom field") {
		t.Errorf("DISC/TRACK must not be noted as custom fields; stderr=%q", stderr)
	}
	jd := decodeJSONOne[jsonDocument](t, mustDumpJSON(t, f))
	if v := tagValues(jd, "DISCNUMBER"); len(v) != 1 || v[0] != "1" {
		t.Errorf("DISC=1 should project canonical DISCNUMBER=[1]; got %v", v)
	}
	if v := tagValues(jd, "TRACKNUMBER"); len(v) != 1 || v[0] != "2" {
		t.Errorf("TRACK=2 should project canonical TRACKNUMBER=[2]; got %v", v)
	}

	// DISC=1 projects canonical DISCNUMBER on every format (resolution is format-independent).
	for _, name := range []string{"notags.flac", "notags.ogg", "notags.mp3", "notags.m4a"} {
		ff := copyFixture(t, td(name))
		if _, errb, c := runCLI(t, "set", ff, "--set", "DISC=1", "-q"); c != 0 {
			t.Fatalf("%s: set DISC=1 exit %d stderr=%q", name, c, errb)
		}
		if v := tagValues(decodeJSONOne[jsonDocument](t, mustDumpJSON(t, ff)), "DISCNUMBER"); len(v) != 1 || v[0] != "1" {
			t.Errorf("%s: DISC=1 should project DISCNUMBER=[1]; got %v", name, v)
		}
	}

	// A number-pair on the alias still splits to DiscNumber + DiscTotal after resolving.
	pair := copyFixture(t, td("notags.flac"))
	if _, _, c := runCLI(t, "set", pair, "--set", "DISC=1/2", "-q"); c != 0 {
		t.Fatalf("set DISC=1/2 exit %d", c)
	}
	pjd := decodeJSONOne[jsonDocument](t, mustDumpJSON(t, pair))
	if v := tagValues(pjd, "DISCNUMBER"); len(v) != 1 || v[0] != "1" {
		t.Errorf("DISC=1/2: DISCNUMBER = %v, want [1]", v)
	}
	if v := tagValues(pjd, "DISCTOTAL"); len(v) != 1 || v[0] != "2" {
		t.Errorf("DISC=1/2: DISCTOTAL = %v, want [2]", v)
	}
}
