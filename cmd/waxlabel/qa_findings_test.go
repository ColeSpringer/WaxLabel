package main

import (
	"bytes"
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

// TestNumberTotalNonNumericDropsTotalCLI covers Finding 3 end to end on an ID3 target: a non-numeric
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

// TestReservedChapterKeyDroppedWithWarning covers Finding 8: a custom key in the reserved CHAPTERxxx
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

// TestSetTrimsMediaTypeAndReplayGain covers Finding 9 on the set path: MEDIATYPE and REPLAYGAIN_* are
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

// TestMalformedYearDroppedNotTruncated covers Finding 10 on an ID3v2.3 target: a malformed 5-digit
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
