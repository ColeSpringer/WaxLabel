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
// name. Vorbis stores the same value literally, so it should not warn.
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
		t.Errorf("a Vorbis target keeps GENRE=17 verbatim and must not warn numeric-genre:\n%s", flacOut)
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
