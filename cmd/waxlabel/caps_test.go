package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// capsCardinality returns the reported cardinality for key in a caps result, or
// "" if the key is absent.
func capsCardinality(jc jsonCaps, key string) string {
	for _, k := range jc.Keys {
		if k.Key == key {
			return k.Cardinality
		}
	}
	return ""
}

func TestCapsFormatText(t *testing.T) {
	out, _, code := runCLI(t, "caps", "--format", "flac")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out=%q", code, out)
	}
	for _, want := range []string{"format:", "FLAC", "fields:", "editable keys", "TITLE", "ARTIST"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
	// ARTIST is multi-valued, TITLE single - the per-key cardinality column.
	if !strings.Contains(out, "ARTIST") || !strings.Contains(out, "multi") {
		t.Errorf("expected ARTIST shown as multi:\n%s", out)
	}
}

func TestCapsFileText(t *testing.T) {
	out, _, code := runCLI(t, "caps", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out=%q", code, out)
	}
	if !strings.Contains(out, sampleFLAC) {
		t.Errorf("file-aware output should name the file:\n%s", out)
	}
	if !strings.Contains(out, "FLAC") {
		t.Errorf("expected FLAC format:\n%s", out)
	}
}

func TestCapsFormatJSON(t *testing.T) {
	out, _, code := runCLI(t, "--json", "caps", "--format", "flac")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var jc jsonCaps
	if err := json.Unmarshal([]byte(out), &jc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if jc.SchemaVersion != schemaVersion {
		t.Errorf("schemaVersion = %d, want %d", jc.SchemaVersion, schemaVersion)
	}
	if jc.Format != "FLAC" {
		t.Errorf("format = %q, want FLAC", jc.Format)
	}
	if jc.File != "" {
		t.Errorf("--format mode should not set file, got %q", jc.File)
	}
	if jc.Fields == nil || jc.Pictures == nil || jc.Chapters == nil {
		t.Fatalf("expected fields/pictures/chapters dimensions: %+v", jc)
	}
	if jc.Fields.Write != "full" {
		t.Errorf("FLAC field write = %q, want full", jc.Fields.Write)
	}
	if got := capsCardinality(jc, "ARTIST"); got != "multi" {
		t.Errorf("ARTIST cardinality = %q, want multi", got)
	}
	if got := capsCardinality(jc, "TITLE"); got != "single" {
		t.Errorf("TITLE cardinality = %q, want single", got)
	}
}

func TestCapsChaptersMaxItemsJSON(t *testing.T) {
	// MP4 caps a chapter set at 255 (the 8-bit chpl count); caps surfaces it.
	out, _, code := runCLI(t, "--json", "caps", "--format", "mp4")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var jc jsonCaps
	if err := json.Unmarshal([]byte(out), &jc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if jc.Chapters == nil || jc.Chapters.MaxItems != 255 {
		t.Errorf("MP4 chapters MaxItems = %v, want 255", jc.Chapters)
	}
}

func TestCapsListsEditableVocabulary(t *testing.T) {
	// Every implemented format is fully field-writable today, so the editable-only
	// listing covers the whole known vocabulary; assert FLAC's editable keys are
	// exactly that set. (The format-independent catalog is the keys command's job.)
	var def jsonCaps
	out, _, _ := runCLI(t, "--json", "caps", "--format", "flac")
	if err := json.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("unmarshal default: %v", err)
	}
	if want := len(tag.KnownKeys()); len(def.Keys) != want {
		t.Errorf("caps listed %d editable keys, want the whole vocabulary (%d)", len(def.Keys), want)
	}
}

func TestCapsAllFlagIsGone(t *testing.T) {
	// The dead --all flag was dropped (discovery moved to the keys command); it is
	// now an unknown flag (usage error, exit 2).
	_, errOut, code := runCLI(t, "caps", "--format", "flac", "--all")
	if code != 2 {
		t.Fatalf("caps --all exit = %d, want 2 (unknown flag)", code)
	}
	if !strings.Contains(errOut, "unknown flag") {
		t.Errorf("caps --all stderr = %q, want it to mention an unknown flag", errOut)
	}
}

func TestCapsMultiFileJSONIsArray(t *testing.T) {
	out, _, code := runCLI(t, "--json", "caps", sampleFLAC, notagsFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	arr := decodeJSONList[jsonCaps](t, out)
	if len(arr) != 2 {
		t.Fatalf("array len = %d, want 2", len(arr))
	}
}

// TestCapsSingleFileJSONIsArray pins that caps over files is a list command: even
// a single file emits a one-element array, not a bare object
// (caps --format stays a single object - see TestCapsFormatJSON), so a script can
// consume caps over one or many files the same way.
func TestCapsSingleFileJSONIsArray(t *testing.T) {
	out, _, code := runCLI(t, "--json", "caps", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	jc := decodeJSONOne[jsonCaps](t, out)
	if jc.Format != "FLAC" {
		t.Errorf("format = %q, want FLAC", jc.Format)
	}
	// Unlike --format mode (File empty), the file form echoes the path back.
	if jc.File != sampleFLAC {
		t.Errorf("file = %q, want %q", jc.File, sampleFLAC)
	}
}

func TestCapsStdin(t *testing.T) {
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLIStdin(t, string(data), "caps", "-")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out=%q", code, out)
	}
	// The header reads "<stdin>", consistent with dump/verify/lint, and never the
	// buffered temp path.
	if !strings.HasPrefix(out, "<stdin>\n") {
		t.Errorf("stdin caps should display <stdin> as the name:\n%s", out)
	}
	if strings.Contains(out, "waxlabel-stdin") {
		t.Errorf("the buffered-stdin temp path leaked into output:\n%s", out)
	}
}

func TestCapsUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no args", []string{"caps"}},
		{"format with file", []string{"caps", "--format", "flac", sampleFLAC}},
		{"unknown format", []string{"caps", "--format", "bogus"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, stderr, code := runCLI(t, c.args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2 (usage)", code)
			}
			if stderr == "" {
				t.Error("expected an error message on stderr")
			}
		})
	}
}

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in   string
		want string // expected Format.String()
	}{
		{"flac", "FLAC"},
		{"FLAC", "FLAC"},
		{".flac", "FLAC"},
		{"mp3", "MP3"},
		{"m4a", "MP4"},
		{"mp4", "MP4"},
		{"alac", "MP4"},
		{"wav", "WAV"},
		{"wave", "WAV"},
		{"aiff", "AIFF"},
		{"aac", "AAC (ADTS)"},
		{"ogg", "Ogg Vorbis"},
		{"oga", "Ogg Vorbis"},
		{"vorbis", "Ogg Vorbis"},
		{"opus", "Ogg Opus"},
		{"mka", "Matroska"},
		{"mkv", "Matroska"},
		{"matroska", "Matroska"},
		// "webm" resolves to Matroska (its container) under the WebM subset option, so
		// caps --format webm describes the cover-refusing variant (see TestCapsFormatWebM).
		{"webm", "Matroska"},
	}
	for _, c := range cases {
		f, _, _, ok := parseFormat(c.in)
		if !ok {
			t.Errorf("parseFormat(%q) failed", c.in)
			continue
		}
		if f.String() != c.want {
			t.Errorf("parseFormat(%q) = %q, want %q", c.in, f.String(), c.want)
		}
	}
	if _, _, _, ok := parseFormat("nonsense"); ok {
		t.Error(`parseFormat("nonsense") should fail`)
	}
}
