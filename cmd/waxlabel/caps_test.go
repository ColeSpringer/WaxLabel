package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// capsCardinality returns the cardinality caps reports for key, or "" if absent.
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
	// ARTIST multi, TITLE single in the cardinality column.
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
	// MP4 chapter count is capped at 255 (8-bit chpl).
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

// TestCapsM4BChapterTitleByteConstraint: caps --format m4b documents the 255-byte title cap.
func TestCapsM4BChapterTitleByteConstraint(t *testing.T) {
	out, _, code := runCLI(t, "caps", "--format", "m4b")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "255 bytes") {
		t.Errorf("caps --format m4b should surface the 255-byte chapter-title constraint:\n%s", out)
	}
}

func TestCapsListsEditableVocabulary(t *testing.T) {
	// All formats are fully field-writable; FLAC editable keys should match KnownKeys.
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
	// --all is an unknown flag (exit 2).
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

// TestCapsSingleFileJSONIsArray: caps over files is a list command (one-element array).
// caps --format stays a bare object (TestCapsFormatJSON).
func TestCapsSingleFileJSONIsArray(t *testing.T) {
	out, _, code := runCLI(t, "--json", "caps", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	jc := decodeJSONOne[jsonCaps](t, out)
	if jc.Format != "FLAC" {
		t.Errorf("format = %q, want FLAC", jc.Format)
	}
	// File form echoes the path; --format mode leaves File empty.
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
	// Header is "<stdin>", not the buffered temp path.
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

// TestCapsJSONShortBoolForm: --json=1 on usage error still emits JSON envelope via wantsJSON scan.
func TestCapsJSONShortBoolForm(t *testing.T) {
	stdout, stderr, code := runCLI(t, "caps", "--format", "bogus", "--json=1")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("error should be JSON on stdout, not text on stderr; stderr:\n%s", stderr)
	}
	var env struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not the JSON error envelope: %v\n%s", err, stdout)
	}
	if env.Error == nil || env.Error.Code == "" {
		t.Errorf("expected a populated JSON error envelope, got %s", stdout)
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
		{"vorbis", "Ogg Vorbis"},
		{"opus", "Ogg Opus"},
		{"oggflac", "Ogg FLAC"},
		{"mka", "Matroska"},
		{"mkv", "Matroska"},
		{"matroska", "Matroska"},
		// webm resolves to Matroska under WebM subset (TestCapsFormatWebM).
		{"webm", "Matroska"},
	}
	for _, c := range cases {
		f, _, _, err := parseFormat(c.in)
		ok := err == nil
		if !ok {
			t.Errorf("parseFormat(%q) failed", c.in)
			continue
		}
		if f.String() != c.want {
			t.Errorf("parseFormat(%q) = %q, want %q", c.in, f.String(), c.want)
		}
	}
	if _, _, _, err := parseFormat("nonsense"); err == nil {
		t.Error(`parseFormat("nonsense") should fail`)
	}
	// .oga is ambiguous; error must name both Ogg codecs.
	_, _, _, err := parseFormat("oga")
	if err == nil {
		t.Fatal(`parseFormat("oga") should report the ambiguity`)
	}
	for _, want := range []string{"Ogg Vorbis", "Ogg FLAC"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("parseFormat(\"oga\") error = %q, want it to name %q", err, want)
		}
	}
	// "ogg" as a format name means Vorbis despite shared .ogg extension.
	if f, _, _, err := parseFormat("ogg"); err != nil || f.String() != "Ogg Vorbis" {
		t.Errorf(`parseFormat("ogg") = %v, %v; want Ogg Vorbis`, f, err)
	}
}

// TestCapsReportsEveryOperand: a directory or a missing path fails alone; the files beside it
// are still described.
func TestCapsReportsEveryOperand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.flac")

	out, errb, code := runCLI(t, "caps", sampleFLAC, missing, dir)
	if code != 6 {
		t.Errorf("exit %d, want 6 (not-found outranks the directory's usage error)", code)
	}
	if !strings.Contains(out, "editable keys") {
		t.Errorf("the file should still be described:\n%s", out)
	}
	for _, want := range []string{
		"waxlabel: " + missing + ": no such file or directory\n",
		"waxlabel: " + dir + ": is a directory, not a file\n",
	} {
		if !strings.Contains(errb, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errb)
		}
	}

	out, _, code = runCLI(t, "--json", "caps", sampleFLAC, missing, dir)
	got := decodeJSONList[jsonCaps](t, out)
	if code != 6 || len(got) != 3 {
		t.Fatalf("--json: exit %d, %d elements; want 6 and 3\n%s", code, len(got), out)
	}
	if got[0].Error != nil || got[0].Format != "FLAC" {
		t.Errorf("element 0 = %+v, want the FLAC report", got[0])
	}
	if got[1].Error == nil || got[1].Error.Code != "not-found" || got[2].Error == nil || got[2].Error.Code != "usage" {
		t.Errorf("errors = %+v, %+v; want not-found and usage", got[1].Error, got[2].Error)
	}
}

// TestParseFormatTakesPrintedNames: every name caps, dump and the README print for a format
// or container is accepted back, in any case and with or without its punctuation.
func TestParseFormatTakesPrintedNames(t *testing.T) {
	t.Parallel()
	for _, f := range wl.Formats() {
		name := f.String()
		for _, in := range []string{name, strings.ToUpper(name), strings.ToLower(name), strings.ReplaceAll(name, " ", "")} {
			// A WMA file has one container, and reports it.
			want := map[wl.Format]string{wl.FormatWMA: "ASF"}[f]
			if got, _, container, err := parseFormat(in); err != nil || got != f || container != want {
				t.Errorf("parseFormat(%q) = %v, %q, %v; want %v, %q", in, got, container, err, f, want)
			}
		}
	}
	for in, want := range map[string]struct {
		format    wl.Format
		container string
	}{
		"rf64": {wl.FormatWAV, "RF64"}, "BW64": {wl.FormatWAV, "BW64"}, "aifc": {wl.FormatAIFF, "AIFC"},
		"AIFF-C": {wl.FormatAIFF, "AIFC"}, "WebM": {wl.FormatMatroska, "WebM"}, "asf": {wl.FormatWMA, "ASF"},
	} {
		if got, _, container, err := parseFormat(in); err != nil || got != want.format || container != want.container {
			t.Errorf("parseFormat(%q) = %v, %q, %v; want %v, %q", in, got, container, err, want.format, want.container)
		}
	}
	for _, bad := range []string{"", " ", "()", "ogg (vorbis)x", "monkey"} {
		if _, _, _, err := parseFormat(bad); err == nil {
			t.Errorf("parseFormat(%q) should fail", bad)
		}
	}

	out, _, code := runCLI(t, "caps", "--format", "AAC (ADTS)")
	if code != 0 || !strings.Contains(out, "format:        AAC (ADTS)") {
		t.Errorf("caps --format 'AAC (ADTS)': exit %d\n%s", code, out)
	}
	out, _, code = runCLI(t, "--json", "caps", "--format", "rf64")
	var jc jsonCaps
	if err := json.Unmarshal([]byte(out), &jc); err != nil || code != 0 || jc.Format != "WAV" || jc.Subformat != "RF64" {
		t.Errorf("caps --format rf64 --json: exit %d, format %q, subformat %q (%v)", code, jc.Format, jc.Subformat, err)
	}
	// A format named with no file reports the subformat a file of it does.
	for name, fixture := range map[string]string{"aifc": "sample.aifc", "wma": "sample.wma", "asf": "sample.wma", "rf64": "sample-rf64.wav", "flac": "sample.flac"} {
		out, _, _ = runCLI(t, "--json", "caps", td(fixture))
		file := decodeJSONOne[jsonCaps](t, out)
		out, _, _ = runCLI(t, "--json", "caps", "--format", name)
		if err := json.Unmarshal([]byte(out), &jc); err != nil || jc.Format != file.Format || jc.Subformat != file.Subformat {
			t.Errorf("caps --format %s: format %q, subformat %q (%v); %s gives %q, %q", name, jc.Format, jc.Subformat, err, fixture, file.Format, file.Subformat)
		}
	}
}
