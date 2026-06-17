package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/waxerr"
)

// The fixtures live in the library's testdata directory, two levels up from this
// command package.
var (
	sampleFLAC = filepath.Join("..", "..", "testdata", "sample.flac")
	notagsFLAC = filepath.Join("..", "..", "testdata", "notags.flac")
	sampleM4B  = filepath.Join("..", "..", "testdata", "sample_chapters.m4b")
)

// runCLI drives the CLI exactly as dispatch does in main, capturing stdout,
// stderr, and the process exit code. Each call builds a fresh command tree and
// holds no shared mutable state, so tests using it may run in parallel.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = dispatch(context.Background(), args, &out, &errb)
	return out.String(), errb.String(), code
}

// copyFixture copies a fixture into a fresh temp file the test may modify.
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dst
}

func tagValues(jd jsonDocument, key string) []string {
	for _, tg := range jd.Tags {
		if tg.Key == key {
			return tg.Values
		}
	}
	return nil
}

func TestDumpText(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{
		"format:  FLAC",
		"44100 Hz, 2 ch, 16-bit",
		"TITLE",
		"Original Title",
		// The acquired-file signature must surface as a warning.
		"[inherited-encoder]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump output missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestDumpChapters(t *testing.T) {
	t.Parallel()
	// Text dump lists chapters with their titles.
	out, _, code := runCLI(t, "dump", sampleM4B)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"chapters (3)", "Opening Credits", "Chapter One", "Chapter Two"} {
		if !strings.Contains(out, want) {
			t.Errorf("dump output missing %q\n--- got ---\n%s", want, out)
		}
	}
	// JSON dump carries them with millisecond timings.
	jout, _, code := runCLI(t, "--json", "dump", sampleM4B)
	if code != 0 {
		t.Fatalf("json dump exit = %d, want 0", code)
	}
	var jd jsonDocument
	if err := json.Unmarshal([]byte(jout), &jd); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jout)
	}
	if len(jd.Chapters) != 3 {
		t.Fatalf("json chapters = %d, want 3", len(jd.Chapters))
	}
	if jd.Chapters[1].Title != "Chapter One" || jd.Chapters[1].StartMs != 3000 {
		t.Errorf("json chapter 1 = %+v, want Chapter One @ 3000ms", jd.Chapters[1])
	}
}

func TestDumpJSON(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "dump", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var jd jsonDocument
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if jd.Format != "FLAC" {
		t.Errorf("format = %q, want FLAC", jd.Format)
	}
	if jd.Properties == nil || jd.Properties.SampleRate != 44100 {
		t.Errorf("properties = %+v", jd.Properties)
	}
	if jd.Properties.BitrateBps <= 1000 {
		t.Errorf("bitrateBps = %d, want raw bits/sec (>1000)", jd.Properties.BitrateBps)
	}
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Original Title" {
		t.Errorf("TITLE = %v", got)
	}
	if len(jd.Warnings) == 0 {
		t.Errorf("expected inherited-encoder warnings, got none")
	}
}

func TestDumpNative(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", "--native", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"native blocks", "STREAMINFO", "VORBIS_COMMENT", "sources", "vorbis"} {
		if !strings.Contains(out, want) {
			t.Errorf("native dump missing %q\n%s", want, out)
		}
	}
}

func TestPlanReportsOperationsWithoutWriting(t *testing.T) {
	t.Parallel()
	before, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLI(t, "plan", sampleFLAC, "--set", "TITLE=Changed")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "plan") || !strings.Contains(out, "size:") {
		t.Errorf("plan report unexpected:\n%s", out)
	}
	after, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("plan modified the source file")
	}
}

func TestPlanNoOp(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "plan", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "no changes") {
		t.Errorf("expected no-op report, got:\n%s", out)
	}
}

func TestSetRoundTrip(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)

	_, _, code := runCLI(t, "set", file,
		"--set", "TITLE=Brand New",
		"--add", "ARTIST=Featured",
		"--clear", "ENCODER",
		"--verify")
	if code != 0 {
		t.Fatalf("set exit = %d, want 0", code)
	}

	out, _, code := runCLI(t, "--json", "dump", file)
	if code != 0 {
		t.Fatalf("dump exit = %d, want 0", code)
	}
	var jd jsonDocument
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Brand New" {
		t.Errorf("TITLE = %v, want [Brand New]", got)
	}
	if got := tagValues(jd, "ARTIST"); len(got) != 2 || got[1] != "Featured" {
		t.Errorf("ARTIST = %v, want [Original Artist Featured]", got)
	}
	if got := tagValues(jd, "ENCODER"); got != nil {
		t.Errorf("ENCODER = %v, want absent (cleared)", got)
	}
}

func TestSetNoOpWritesNothing(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLI(t, "set", file)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "No changes") {
		t.Errorf("expected no-op outcome, got:\n%s", out)
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("no-op set rewrote the file")
	}
}

func TestSetSaveAsLeavesOriginal(t *testing.T) {
	t.Parallel()
	src := copyFixture(t, sampleFLAC)
	dst := filepath.Join(t.TempDir(), "out.flac")
	before, _ := os.ReadFile(src)

	_, _, code := runCLI(t, "set", src, "--set", "ALBUM=Compilation", "-o", dst)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("output not written: %v", err)
	}
	after, _ := os.ReadFile(src)
	if !bytes.Equal(before, after) {
		t.Error("save-as modified the source file")
	}

	out, _, _ := runCLI(t, "--json", "dump", dst)
	var jd jsonDocument
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got := tagValues(jd, "ALBUM"); len(got) != 1 || got[0] != "Compilation" {
		t.Errorf("ALBUM = %v, want [Compilation]", got)
	}
}

func TestVerifyEssenceStableAcrossTagEdit(t *testing.T) {
	t.Parallel()
	// A tag-only edit must not change the audio-essence identity.
	out1, _, code := runCLI(t, "--json", "verify", sampleFLAC)
	if code != 0 {
		t.Fatalf("verify exit = %d", code)
	}
	var v1 jsonVerify
	if err := json.Unmarshal([]byte(out1), &v1); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out1)
	}
	if !strings.HasPrefix(v1.Essence, "sha256/flac-frames-v1:") {
		t.Errorf("essence = %q", v1.Essence)
	}

	file := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", file, "--set", "TITLE=Whatever"); code != 0 {
		t.Fatalf("set exit = %d", code)
	}
	out2, _, _ := runCLI(t, "--json", "verify", file)
	var v2 jsonVerify
	if err := json.Unmarshal([]byte(out2), &v2); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if v1.Essence != v2.Essence {
		t.Errorf("essence changed after tag edit:\n before %s\n after  %s", v1.Essence, v2.Essence)
	}
}

func TestVerifyWholeFileFlag(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "verify", "--whole-file", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "essence:") || !strings.Contains(out, "whole-file:") {
		t.Errorf("verify --whole-file output:\n%s", out)
	}
	if !strings.Contains(out, "whole-file-v1:") {
		t.Errorf("missing whole-file extent name:\n%s", out)
	}
}

// TestMultiLineTagValueAligns checks that a value containing a newline (lyrics)
// keeps the aligned layout: its continuation line is indented, not at column 0.
func TestMultiLineTagValueAligns(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", file, "--set", "LYRICS=line one\nline two"); code != 0 {
		t.Fatalf("set exit = %d", code)
	}
	out, _, code := runCLI(t, "dump", file)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Fatalf("both lyric lines should appear:\n%s", out)
	}
	// A continuation line at column 0 would show as "\nline two"; indentation
	// means it is preceded by spaces instead.
	if strings.Contains(out, "\nline two") {
		t.Errorf("continuation line not indented:\n%s", out)
	}
}

// TestClassifyError pins the exit-code/machine-code mapping that scripts rely on,
// including the failure types the CLI never reaches through a normal run.
func TestClassifyError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		code int
		mc   string
	}{
		{"canceled", context.Canceled, 130, "canceled"},
		{"deadline", context.DeadlineExceeded, 130, "timeout"},
		{"rename", &os.LinkError{Op: "rename", Err: errors.New("x")}, 6, "io"},
		{"open", &fs.PathError{Op: "open", Err: errors.New("x")}, 6, "io"},
		{"usage", &usageError{msg: "bad"}, 2, "usage"},
		{"invalid-key", fmt.Errorf("w: %w", waxerr.ErrInvalidKey), 2, "invalid-key"},
		{"unsupported", fmt.Errorf("w: %w", waxerr.ErrUnsupportedFormat), 3, "unsupported-format"},
		{"invalid-data", fmt.Errorf("w: %w", waxerr.ErrInvalidData), 4, "invalid-data"},
		{"source-changed", fmt.Errorf("w: %w", waxerr.ErrSourceChanged), 5, "source-changed"},
		{"unclassified", errors.New("boom"), 1, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := classifyError(tc.err)
			if c.exitCode != tc.code || c.code != tc.mc {
				t.Errorf("classify = (%d,%q), want (%d,%q)", c.exitCode, c.code, tc.code, tc.mc)
			}
		})
	}
}

func TestNotagsFixtureHasNoTags(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", notagsFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "tags:    (none)") {
		t.Errorf("expected no tags, got:\n%s", out)
	}
}

// TestExitCodes checks the stable failure classification scripts may rely on.
func TestExitCodes(t *testing.T) {
	t.Parallel()
	junk := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(junk, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "nope.flac")

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"missing-file", []string{"dump", missing}, 6},
		{"unsupported-format", []string{"dump", junk}, 3},
		{"bad-key", []string{"plan", sampleFLAC, "--clear", "A=B"}, 2},
		{"missing-assign", []string{"plan", sampleFLAC, "--set", "TITLE"}, 2},
		{"unknown-preset", []string{"plan", sampleFLAC, "--preset", "bogus"}, 2},
		{"unknown-command", []string{"frobnicate", "x"}, 2},
		{"unknown-flag", []string{"dump", "--nope", sampleFLAC}, 2},
		{"missing-arg", []string{"plan"}, 2},
		{"success", []string{"dump", sampleFLAC}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, code := runCLI(t, tc.args...)
			if code != tc.want {
				t.Errorf("exit = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestJSONErrorEnvelope checks that a command-level failure under --json is a
// single well-formed envelope on stdout with the classified code.
func TestJSONErrorEnvelope(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, _, code := runCLI(t, "--json", "plan", missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	var je jsonError
	if err := json.Unmarshal([]byte(out), &je); err != nil {
		t.Fatalf("invalid JSON envelope: %v\n%s", err, out)
	}
	if je.Error.Code != "io" {
		t.Errorf("error code = %q, want io", je.Error.Code)
	}
}

// TestDumpJSONPerFileError checks that dump keeps going after a bad file and
// records the failure as a per-file error object (array for multiple files).
func TestDumpJSONPerFileError(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, _, code := runCLI(t, "--json", "dump", sampleFLAC, missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6 (a file failed)", code)
	}
	var docs []jsonDocument
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		t.Fatalf("invalid JSON array: %v\n%s", err, out)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
	if docs[0].Error != nil {
		t.Errorf("first doc should have parsed: %+v", docs[0].Error)
	}
	if docs[1].Error == nil || docs[1].Error.Code != "io" {
		t.Errorf("second doc should carry an io error: %+v", docs[1].Error)
	}
}

// TestJSONErrorRoutingOnEarlyAbort checks that --json still routes the terminal
// error to stdout as an envelope even when cobra aborts during command/flag
// resolution (before it binds the persistent flag).
func TestJSONErrorRoutingOnEarlyAbort(t *testing.T) {
	t.Parallel()
	cases := [][]string{
		{"--json", "frobnicate", "x"},            // unknown command
		{"dump", "--nope", "--json", sampleFLAC}, // bad flag positioned before --json
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			out, errb, code := runCLI(t, args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			var je jsonError
			if err := json.Unmarshal([]byte(out), &je); err != nil {
				t.Fatalf("stdout is not a JSON envelope: %v\nstdout=%q stderr=%q", err, out, errb)
			}
			if je.Error.Code != "usage" {
				t.Errorf("error code = %q, want usage", je.Error.Code)
			}
		})
	}
}

// TestSetShowsPlanBeforeFailedWrite checks that the plan preview is printed even
// when the write fails, matching the help ("the plan is printed before the
// outcome").
func TestSetShowsPlanBeforeFailedWrite(t *testing.T) {
	t.Parallel()
	// Save-as into a non-existent directory: planning succeeds, the write fails.
	bad := filepath.Join(t.TempDir(), "no-such-dir", "out.flac")
	out, _, code := runCLI(t, "set", sampleFLAC, "--set", "TITLE=X", "-o", bad)
	if code != 6 {
		t.Fatalf("exit = %d, want 6 (io)", code)
	}
	if !strings.Contains(out, "plan") {
		t.Errorf("plan should be printed before the failed write:\n%s", out)
	}
}

func TestHumanDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00"},
		{500 * time.Millisecond, "0.50s"},
		{time.Second, "1.00s"},
		{59500 * time.Millisecond, "59.50s"},
		{59999 * time.Millisecond, "1:00"}, // boundary: must not be "60.00s"
		{60 * time.Second, "1:00"},
		{90 * time.Second, "1:30"},
		{3661 * time.Second, "1:01:01"},
	}
	for _, tc := range cases {
		if got := humanDuration(tc.d); got != tc.want {
			t.Errorf("humanDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestWriteWrapped(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	// A trailing newline must not produce a stray indent-only continuation line.
	writeWrapped(&b, 4, "a\nb\n")
	if got, want := b.String(), "a\n    b\n"; got != want {
		t.Errorf("trailing newline: got %q, want %q", got, want)
	}
	// An internal blank line is preserved.
	b.Reset()
	writeWrapped(&b, 2, "x\n\ny")
	if got, want := b.String(), "x\n  \n  y\n"; got != want {
		t.Errorf("internal blank: got %q, want %q", got, want)
	}
}
