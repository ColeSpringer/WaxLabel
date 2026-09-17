package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// td resolves a fixture name under testdata.
func td(name string) string { return filepath.Join("..", "..", "testdata", name) }

// compactJSON re-marshals JSON without whitespace for exact token matches.
func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, s)
	}
	return b.String()
}

// lineWith returns the first output line containing sub.
func lineWith(out, sub string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	return ""
}

// TestEmptyValuePreservedMatroska: set KEY= writes present empty [""], distinct from --clear.
// Covers SimpleTag (ARTIST) and Info.Title on .mka and .webm.
func TestEmptyValuePreservedMatroska(t *testing.T) {
	for _, src := range []string{notagsMKA, sampleWebMF} {
		f := copyFixture(t, src)
		if _, _, code := runCLI(t, "set", f, "--set", "ARTIST=", "-q"); code != 0 {
			t.Fatalf("%s: set ARTIST= exit %d", src, code)
		}
		jd := decodeJSONOne[jsonDocument](t, mustDumpJSON(t, f))
		if v := tagValues(jd, "ARTIST"); len(v) != 1 || v[0] != "" {
			t.Errorf("%s: ARTIST = %v, want one present empty value", src, v)
		}
	}

	// set TITLE= (present empty) vs --clear TITLE (absent); distinct bytes.
	t1, t2 := copyFixture(t, notagsMKA), copyFixture(t, notagsMKA)
	runCLI(t, "set", t1, "--set", "TITLE=", "-q")
	runCLI(t, "set", t2, "--clear", "TITLE", "-q")
	if v := tagValues(decodeJSONOne[jsonDocument](t, mustDumpJSON(t, t1)), "TITLE"); len(v) != 1 || v[0] != "" {
		t.Errorf("set TITLE= -> %v, want one present empty value", v)
	}
	if v := tagValues(decodeJSONOne[jsonDocument](t, mustDumpJSON(t, t2)), "TITLE"); v != nil {
		t.Errorf("--clear TITLE -> %v, want absent", v)
	}
	b1, _ := os.ReadFile(t1)
	b2, _ := os.ReadFile(t2)
	if bytes.Equal(b1, b2) {
		t.Error("set TITLE= and --clear TITLE produced identical bytes (they must differ)")
	}
}

// TestEmptyValueKeptOnGeneralFormats: MP3, AAC, MP4, FLAC, Ogg, Matroska keep present-empty.
// WAV/AIFF native exception covered separately.
func TestEmptyValueKeptOnGeneralFormats(t *testing.T) {
	for _, src := range []string{td("notags.mp3"), td("notags.aac"), notagsM4A} {
		f := copyFixture(t, src)
		if _, _, code := runCLI(t, "set", f, "--set", "ARTIST=", "-q"); code != 0 {
			t.Fatalf("%s: set ARTIST= exit %d", src, code)
		}
		jd := decodeJSONOne[jsonDocument](t, mustDumpJSON(t, f))
		if v := tagValues(jd, "ARTIST"); len(v) != 1 || v[0] != "" {
			t.Errorf("%s: ARTIST = %v, want a kept present-empty value", src, v)
		}
	}
}

// TestWAVAIFFPresentEmptyNativeRoundTrip: WAV ZSTR and AIFF zero-length chunks round-trip
// present-empty without forcing an ID3 chunk.
func TestWAVAIFFPresentEmptyNativeRoundTrip(t *testing.T) {
	for _, src := range []string{td("notags.wav"), td("notags.aiff")} {
		t.Run(filepath.Base(src), func(t *testing.T) {
			bare := copyFixture(t, src)
			if _, _, code := runCLI(t, "set", bare, "--set", "ARTIST=", "-q"); code != 0 {
				t.Fatalf("set ARTIST= exit %d", code)
			}
			if v := tagValues(decodeJSONOne[jsonDocument](t, mustDumpJSON(t, bare)), "ARTIST"); len(v) != 1 || v[0] != "" {
				t.Errorf("bare native chunk: ARTIST = %v, want a kept present-empty value", v)
			}
		})
	}
}

// TestTrackNumberSlashSplitsAcrossFormats: TRACKNUMBER=3/12 -> TRACKNUMBER=3, TRACKTOTAL=12 everywhere.
// FLAC/Ogg/Opus/WAV need write-side split; others also assert uniform result.
func TestTrackNumberSlashSplitsAcrossFormats(t *testing.T) {
	for _, src := range []string{
		td("notags.flac"), td("notags.ogg"), td("notags.opus"), td("notags.wav"),
		td("notags.mp3"), td("notags.m4a"), td("notags.mka"),
	} {
		f := copyFixture(t, src)
		if _, _, code := runCLI(t, "set", f, "--set", "TRACKNUMBER=3/12", "-q"); code != 0 {
			t.Fatalf("%s: set exit %d", src, code)
		}
		jd := decodeJSONOne[jsonDocument](t, mustDumpJSON(t, f))
		if v := tagValues(jd, "TRACKNUMBER"); len(v) != 1 || v[0] != "3" {
			t.Errorf("%s: TRACKNUMBER = %v, want [3]", src, v)
		}
		if v := tagValues(jd, "TRACKTOTAL"); len(v) != 1 || v[0] != "12" {
			t.Errorf("%s: TRACKTOTAL = %v, want [12]", src, v)
		}
	}
}

// TestDiffNumericSignLeadingZeroNotAChange: +3 (text) vs 3 (MP4 canonical) is same number; no diff change.
func TestDiffNumericSignLeadingZeroNotAChange(t *testing.T) {
	flac := copyFixture(t, td("notags.flac"))
	m4a := copyFixture(t, td("notags.m4a"))
	if _, _, code := runCLI(t, "set", flac, "--set", "TRACKNUMBER=+3", "-q"); code != 0 {
		t.Fatalf("set flac exit %d", code)
	}
	if _, _, code := runCLI(t, "set", m4a, "--set", "TRACKNUMBER=3", "-q"); code != 0 {
		t.Fatalf("set m4a exit %d", code)
	}

	out, _, code := runCLI(t, "--json", "diff", flac, m4a)
	if code > 1 {
		t.Fatalf("diff exit %d: %s", code, out)
	}
	if diffHasKeyChange(t, out, "TRACKNUMBER") {
		t.Errorf("diff reported a TRACKNUMBER change across formats; +3 and 3 are the same number\n%s", out)
	}
}

// TestDiffNumericFoldScopedToNumberSlotsAndCrossFormat: same-format leading-zero delta is real;
// cross-format fold applies only to track/disc slots MP4 canonicalizes.
func TestDiffNumericFoldScopedToNumberSlotsAndCrossFormat(t *testing.T) {
	// Same format: 03 vs 3 stored verbatim; must report change.
	a := copyFixture(t, td("notags.flac"))
	b := copyFixture(t, td("notags.flac"))
	if _, _, code := runCLI(t, "set", a, "--set", "TRACKNUMBER=03", "-q"); code != 0 {
		t.Fatalf("set a exit %d", code)
	}
	if _, _, code := runCLI(t, "set", b, "--set", "TRACKNUMBER=3", "-q"); code != 0 {
		t.Fatalf("set b exit %d", code)
	}
	out, _, code := runCLI(t, "--json", "diff", a, b)
	if code > 1 {
		t.Fatalf("diff exit %d: %s", code, out)
	}
	if !diffHasKeyChange(t, out, "TRACKNUMBER") {
		t.Errorf("same-format 03 vs 3 must report a TRACKNUMBER change (both stored verbatim)\n%s", out)
	}

	// Cross-format PLAYCOUNT: no canonicalization; 007 vs 7 is a real change.
	fl := copyFixture(t, td("notags.flac"))
	m4 := copyFixture(t, td("notags.m4a"))
	if _, _, code := runCLI(t, "set", fl, "--set", "PLAYCOUNT=007", "-q"); code != 0 {
		t.Fatalf("set flac playcount exit %d", code)
	}
	if _, _, code := runCLI(t, "set", m4, "--set", "PLAYCOUNT=7", "-q"); code != 0 {
		t.Fatalf("set m4a playcount exit %d", code)
	}
	out2, _, code2 := runCLI(t, "--json", "diff", fl, m4)
	if code2 > 1 {
		t.Fatalf("diff exit %d: %s", code2, out2)
	}
	if !diffHasKeyChange(t, out2, "PLAYCOUNT") {
		t.Errorf("cross-format play count 007 vs 7 must report a change (no format canonicalizes it)\n%s", out2)
	}
}

// diffHasKeyChange reports whether diff --json lists a change for key.
func diffHasKeyChange(t *testing.T, out, key string) bool {
	t.Helper()
	var jd jsonDiff
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("invalid diff JSON: %v\n%s", err, out)
	}
	for _, tc := range jd.Tags {
		if tc.Key == key {
			return true
		}
	}
	return false
}

// TestSetClearConflictRefused: same key cannot be set and cleared; set+add on one key stays legal.
func TestSetClearConflictRefused(t *testing.T) {
	f := copyFixture(t, sampleFLAC)
	for _, args := range [][]string{
		{"plan", f, "--clear", "TITLE", "--set", "TITLE=NEW"},
		{"plan", f, "--set", "TITLE=NEW", "--clear", "TITLE"},
	} {
		_, stderr, code := runCLI(t, args...)
		if code != 2 {
			t.Errorf("%v exit = %d, want 2", args[2:], code)
		}
		if !strings.Contains(stderr, "TITLE") || !strings.Contains(stderr, "conflict") {
			t.Errorf("%v stderr = %q, want it to name TITLE and the conflict", args[2:], stderr)
		}
	}
	// --strip-encoder clears ENCODER; message names the flag typed.
	if _, stderr, code := runCLI(t, "plan", f, "--set", "ENCODER=x", "--strip-encoder"); code != 2 ||
		!strings.Contains(stderr, "--strip-encoder") {
		t.Errorf("set ENCODER + --strip-encoder: exit %d stderr %q, want exit 2 naming --strip-encoder", code, stderr)
	}
	// set+add on one key: legal (both write).
	if _, _, code := runCLI(t, "plan", f, "--set", "ARTIST=A", "--add", "ARTIST=B"); code != 0 {
		t.Errorf("set+add on one key exit = %d, want 0 (legal)", code)
	}
}

// TestCapsWebMHeader: human header says WebM; JSON format stays bare "Matroska".
func TestCapsWebMHeader(t *testing.T) {
	if got := lineWith(mustRun(t, 0, "caps", "--format", "webm"), "format:"); !strings.Contains(got, "WebM") {
		t.Errorf("caps --format webm header = %q, want it to say WebM", got)
	}
	if got := lineWith(mustRun(t, 0, "caps", sampleWebMF), "format:"); !strings.Contains(got, "WebM") {
		t.Errorf("caps file.webm header = %q, want it to say WebM", got)
	}
	var jc jsonCaps
	if err := json.Unmarshal([]byte(mustRun(t, 0, "caps", "--format", "webm", "--json")), &jc); err != nil {
		t.Fatalf("caps --format webm --json: %v", err)
	}
	if jc.Format != "Matroska" {
		t.Errorf("caps --format webm JSON format = %q, want the bare Matroska identity", jc.Format)
	}
	// matroska header unchanged.
	if got := lineWith(mustRun(t, 0, "caps", "--format", "matroska"), "format:"); !strings.Contains(got, "Matroska") {
		t.Errorf("caps --format matroska header = %q, want Matroska", got)
	}
}

// TestCodecCaseNotUppercased: dump shows canonical codec case; human and JSON agree.
func TestCodecCaseNotUppercased(t *testing.T) {
	for _, c := range []struct{ file, want string }{
		{td("sample.opus"), "Opus"},
		{td("sample.ogg"), "Vorbis"},
		{sampleFLAC, "FLAC"},
		{td("sample.aac"), "AAC"},
	} {
		audio := lineWith(mustRun(t, 0, "dump", c.file), "audio:")
		if !strings.Contains(audio, c.want) {
			t.Errorf("%s: audio line = %q, want codec %q (canonical case)", c.file, audio, c.want)
		}
		if up := strings.ToUpper(c.want); up != c.want && strings.Contains(audio, up) {
			t.Errorf("%s: audio line %q still upper-cases the codec to %q", c.file, audio, up)
		}
		jd := decodeJSONOne[jsonDocument](t, mustDumpJSON(t, c.file))
		if jd.Properties == nil || jd.Properties.Codec != c.want {
			t.Errorf("%s: --json codec != %q (human and JSON must agree)", c.file, c.want)
		}
	}
}

// TestJSONEmptyCollectionsAreArrays: collection fields are [] not omitted/null on empty files.
func TestJSONEmptyCollectionsAreArrays(t *testing.T) {
	dump := compactJSON(t, mustDumpJSON(t, td("notags.mp3")))
	for _, want := range []string{`"tags":[]`, `"pictures":[]`, `"chapters":[]`, `"warnings":[]`} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump --json missing %s\n%s", want, dump)
		}
	}
	if lint := compactJSON(t, mustRun(t, 0, "lint", td("notags.mp3"), "--json")); !strings.Contains(lint, `"findings":[]`) {
		t.Errorf("lint --json missing findings:[]\n%s", lint)
	}
	// lint --fix: changes/remaining empty on clean file; operations always present.
	fix := compactJSON(t, mustRun(t, -1, "lint", "--fix", copyFixture(t, td("notags.mp3")), "--json"))
	for _, want := range []string{`"changes":[]`, `"remaining":[]`, `"operations":`} {
		if !strings.Contains(fix, want) {
			t.Errorf("lint --fix --json missing %s\n%s", want, fix)
		}
	}
	if plan := compactJSON(t, mustRun(t, 0, "plan", sampleFLAC, "--json")); !strings.Contains(plan, `"warnings":[]`) {
		t.Errorf("plan --json missing warnings:[]\n%s", plan)
	}
}

// TestCapsKeysAlwaysArray: caps keys is [] even when capability has no writable keys.
func TestCapsKeysAlwaysArray(t *testing.T) {
	jc := buildCaps("", "", wl.Capabilities{})
	if jc.Keys == nil {
		t.Fatal("buildCaps Keys is nil; want a non-nil empty slice")
	}
	b, err := json.Marshal(jc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"keys":[]`) {
		t.Errorf("caps JSON missing keys:[]\n%s", b)
	}
}

// mustDumpJSON runs dump --json, requiring exit 0.
func mustDumpJSON(t *testing.T, file string) string {
	t.Helper()
	return mustRun(t, 0, "dump", file, "--json")
}

// mustRun runs CLI; wantCode -1 accepts any exit (e.g. lint --fix).
func mustRun(t *testing.T, wantCode int, args ...string) string {
	t.Helper()
	stdout, stderr, code := runCLI(t, args...)
	if wantCode >= 0 && code != wantCode {
		t.Fatalf("%v exit = %d, want %d; stderr=%s", args, code, wantCode, stderr)
	}
	return stdout
}
