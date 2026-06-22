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

// td resolves a fixture name under the library's testdata directory.
func td(name string) string { return filepath.Join("..", "..", "testdata", name) }

// compactJSON re-marshals s into its whitespace-free form (validating it on the way)
// so a test can match exact field tokens like `"tags":[]` without indentation noise.
func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, s)
	}
	return b.String()
}

// lineWith returns the first output line containing sub (or "").
func lineWith(out, sub string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	return ""
}

// TestEmptyValuePreservedMatroska (A1): `set KEY=` writes a present empty value on
// Matroska/WebM that round-trips as [""] - it is no longer dropped (which had made it
// identical to `--clear KEY`). Both the SimpleTag path (ARTIST) and the Info.Title
// path are covered, on .mka and .webm.
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

	// set TITLE= (present empty, Info.Title path) differs from --clear TITLE (absent),
	// and the two write distinct bytes.
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

// TestEmptyValueKeptOnGeneralFormats (A1) locks the contract's load-bearing claim:
// only WAV/AIFF drop a present-empty *general* value; MP3/AAC/MP4 keep it (as does
// Matroska now). A regression here would mean a hidden third dropper.
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

// TestTrackNumberSlashSplitsAcrossFormats (A2): `--set TRACKNUMBER=3/12` yields the
// canonical TRACKNUMBER=3 + TRACKTOTAL=12 on every format. flac/ogg/opus/wav are the
// rows that actually exercise the new write-side split: their read path does NOT split
// a slash (it is non-standard for Vorbis/WAV), so a deleted splitNumberPairs would
// leave TRACKNUMBER=["3/12"] and fail them. mp3/m4a/mka also split on read (ID3 TRCK,
// MP4 trkn, Matroska projectTag), so they would pass even without the write-side split;
// they are kept to assert the cross-format canonical result is uniform.
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

// TestSetClearConflictRefused (B1): the same key given to both --set/--add and
// --clear (or --strip-encoder) is refused up front (exit 2, nothing written),
// regardless of typed order; set+add on one key stays legal.
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
	// --strip-encoder is a clear of ENCODER; the message names the flag actually typed.
	if _, stderr, code := runCLI(t, "plan", f, "--set", "ENCODER=x", "--strip-encoder"); code != 2 ||
		!strings.Contains(stderr, "--strip-encoder") {
		t.Errorf("set ENCODER + --strip-encoder: exit %d stderr %q, want exit 2 naming --strip-encoder", code, stderr)
	}
	// set+add on one key is legal: both write, neither removes.
	if _, _, code := runCLI(t, "plan", f, "--set", "ARTIST=A", "--add", "ARTIST=B"); code != 0 {
		t.Errorf("set+add on one key exit = %d, want 0 (legal)", code)
	}
}

// TestCapsWebMHeader (B3): the human header for WebM (by --format and by file) reads
// WebM - the same label copy renders - while the JSON format field stays the bare
// "Matroska" identity. Matroska itself is unaffected.
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
	// matroska stays Matroska in the human header too.
	if got := lineWith(mustRun(t, 0, "caps", "--format", "matroska"), "format:"); !strings.Contains(got, "Matroska") {
		t.Errorf("caps --format matroska header = %q, want Matroska", got)
	}
}

// TestCodecCaseNotUppercased (C1): the human dump shows the canonical codec case
// (Opus/Vorbis mixed, FLAC/AAC upper), matching the --json codec field exactly -
// no more ToUpper.
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

// TestJSONEmptyCollectionsAreArrays (D1): the iterable collection fields are always
// present as arrays (never omitted or null), so a `jq '.[].tags[]'`-style consumer
// works on an empty file too - across dump, lint, lint --fix, and plan.
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
	// lint --fix: changes/remaining empty on a clean file, operations always present.
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

// TestCapsKeysAlwaysArray (D1): caps emits keys as an array even for a capability
// with no writable keys (the latent read-only case no shipping format triggers), so
// it is pinned at the struct/init level rather than via the CLI.
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

// mustDumpJSON runs `dump <file> --json`, requiring exit 0, and returns stdout.
func mustDumpJSON(t *testing.T, file string) string {
	t.Helper()
	return mustRun(t, 0, "dump", file, "--json")
}

// mustRun runs the CLI and returns stdout, failing if the exit code is not wantCode
// (pass -1 to accept any code, e.g. lint --fix which may exit 0 or 1).
func mustRun(t *testing.T, wantCode int, args ...string) string {
	t.Helper()
	stdout, stderr, code := runCLI(t, args...)
	if wantCode >= 0 && code != wantCode {
		t.Fatalf("%v exit = %d, want %d; stderr=%s", args, code, wantCode, stderr)
	}
	return stdout
}
