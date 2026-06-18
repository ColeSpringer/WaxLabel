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
	emptyMP3   = filepath.Join("..", "..", "testdata", "empty.mp3") // tag-only/truncated MP3
)

// runCLI drives the CLI exactly as dispatch does in main, capturing stdout,
// stderr, and the process exit code. Each call builds a fresh command tree and
// holds no shared mutable state, so tests using it may run in parallel.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runCLIStdin(t, "", args...)
}

// runCLIStdin is runCLI with a standard-input string, for exercising the "-"
// path sentinel.
func runCLIStdin(t *testing.T, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = dispatch(context.Background(), args, strings.NewReader(stdin), &out, &errb)
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

// TestSetThroughSymlinkUpdatesTarget checks that editing through a symlink
// rewrites the link's target and leaves the symlink in place, instead of
// replacing the link with a regular file (which would silently diverge from the
// real file).
func TestSetThroughSymlinkUpdatesTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	real := filepath.Join(dir, "real.flac")
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, data, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.flac")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	if _, _, code := runCLI(t, "set", link, "--set", "TITLE=Linked"); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	// The link must still be a symlink, not a regular file.
	if fi, err := os.Lstat(link); err != nil {
		t.Fatalf("lstat link: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	// The edit landed on the target the link points at.
	out, _, _ := runCLI(t, "--json", "dump", real)
	var jd jsonDocument
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Linked" {
		t.Errorf("target TITLE = %v, want [Linked]", got)
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
		{"not-found", &fs.PathError{Op: "open", Path: "/x.flac", Err: fs.ErrNotExist}, 6, "not-found"},
		// A not-exist error that is not a *fs.PathError stays in the I/O class:
		// "not-found" promises the clean path-only message we can build only for a
		// PathError, so a rename race must not borrow that code with a raw message.
		{"rename-not-found", &os.LinkError{Op: "rename", Err: fs.ErrNotExist}, 6, "io"},
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

// TestClassifyNotFoundMessage checks the file-not-found message form and, just
// as importantly, that a *fs.PathError a caller has already wrapped with context
// (a temp-file create, a cover read) is not flattened back into "no such file":
// os.IsNotExist does not unwrap, so the caller's message survives and the error
// classifies as the generic I/O class.
func TestClassifyNotFoundMessage(t *testing.T) {
	t.Parallel()
	bare := &fs.PathError{Op: "open", Path: "/x.flac", Err: fs.ErrNotExist}
	if c := classifyError(bare); c.message != "no such file: /x.flac" {
		t.Errorf("bare message = %q, want %q", c.message, "no such file: /x.flac")
	}

	// Mirrors the real edit.go wrapping ("cover image: %w" - the read error
	// already carries the path), so a regression in that shape would surface here.
	wrapped := fmt.Errorf("cover image: %w", &fs.PathError{Op: "open", Path: "/x.png", Err: fs.ErrNotExist})
	c := classifyError(wrapped)
	if c.code != "io" || c.exitCode != 6 {
		t.Errorf("wrapped class = (%d,%q), want (6,\"io\")", c.exitCode, c.code)
	}
	if !strings.HasPrefix(c.message, "cover image:") {
		t.Errorf("wrapped message lost its context: %q", c.message)
	}
}

// TestSentinelsHaveNoProgramPrefix locks in that library sentinels carry no
// "waxlabel: " prefix; the CLI owns the single prefix, so embedding one would
// double it.
func TestSentinelsHaveNoProgramPrefix(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		waxerr.ErrUnsupportedFormat, waxerr.ErrInvalidData, waxerr.ErrNoTags,
		waxerr.ErrUnsupportedTag, waxerr.ErrPictureTooLarge, waxerr.ErrSizeTooLarge,
		waxerr.ErrTooDeep, waxerr.ErrSourceChanged, waxerr.ErrChainedStream,
		waxerr.ErrInvalidKey,
	} {
		if strings.HasPrefix(err.Error(), "waxlabel:") {
			t.Errorf("sentinel %q should not embed the program prefix", err.Error())
		}
	}
}

// TestDumpMissingFilePathOnce checks dump's per-file error names the path once
// (its own prefix), without the raw "open <path>:" that used to restate it.
func TestDumpMissingFilePathOnce(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	_, errb, code := runCLI(t, "dump", missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	if n := strings.Count(errb, missing); n != 1 {
		t.Errorf("path should appear exactly once, got %d:\n%s", n, errb)
	}
	if strings.Contains(errb, "open "+missing) {
		t.Errorf("raw 'open <path>:' should be gone:\n%s", errb)
	}
}

// TestPlanMissingFileMessage checks plan (now a per-file command) reports a
// missing file in the per-file form - the path once, then the bare reason, with
// no raw "open <path>:" prefix - matching dump and verify.
func TestPlanMissingFileMessage(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	_, errb, code := runCLI(t, "plan", missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	if want := "waxlabel: " + missing + ": "; !strings.Contains(errb, want) {
		t.Errorf("stderr = %q, want it to contain %q", errb, want)
	}
	if n := strings.Count(errb, missing); n != 1 {
		t.Errorf("path should appear exactly once, got %d:\n%s", n, errb)
	}
	if strings.Contains(errb, "open "+missing) {
		t.Errorf("raw 'open <path>:' should be gone:\n%s", errb)
	}
}

// TestDirectoryAsInput checks that handing a directory where a file is expected
// fails with a clear "is a directory" message in the invalid-data class, not the
// confusing "could not identify" unsupported-format error it produced before.
func TestDirectoryAsInput(t *testing.T) {
	t.Parallel()
	_, errb, code := runCLI(t, "dump", t.TempDir())
	if code != 4 {
		t.Fatalf("exit = %d, want 4 (invalid data)", code)
	}
	if !strings.Contains(errb, "is a directory") {
		t.Errorf("stderr should explain the directory: %q", errb)
	}
}

// TestTempCreateErrorNamesDir checks the atomic-write temp-create failure names
// the destination directory rather than the internal temp pattern. It also
// guards the E2/E3 interaction: the wrapped *fs.PathError must keep this message
// and not be flattened into "no such file: <temp-name>".
func TestTempCreateErrorNamesDir(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	badDir := filepath.Join(t.TempDir(), "no-such-dir")
	_, errb, code := runCLI(t, "set", file, "--set", "TITLE=X", "-o", filepath.Join(badDir, "out.flac"))
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	if !strings.Contains(errb, "create temp file in "+badDir) {
		t.Errorf("stderr should name the destination dir: %q", errb)
	}
	// The internal temp-file pattern is an implementation detail; it must not leak.
	if strings.Contains(errb, ".waxlabel-") {
		t.Errorf("internal temp pattern should not leak: %q", errb)
	}
}

// TestAddCoverMissingFileContext checks a missing cover file is reported with
// "cover image: <path>:" context so the user knows which input failed.
func TestAddCoverMissingFileContext(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	missing := filepath.Join(t.TempDir(), "cover.png")
	_, errb, code := runCLI(t, "set", file, "--add-cover", missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	if !strings.Contains(errb, "cover image:") || !strings.Contains(errb, missing) {
		t.Errorf("stderr should carry the cover context and path: %q", errb)
	}
	// The path is named once (by the underlying read error), not twice.
	if strings.Count(errb, missing) != 1 {
		t.Errorf("cover path should appear once: %q", errb)
	}
}

// TestAddCoverRejectsNonImage checks that pointing --add-cover at a file that is
// not a recognized image is a usage error (exit 2) by default, and that --force
// overrides it (embedding the bytes, which then sniff to octet-stream).
func TestAddCoverRejectsNonImage(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, notagsFLAC)
	notImage := filepath.Join(t.TempDir(), "cover.png")
	if err := os.WriteFile(notImage, []byte("this is plainly not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, errb, code := runCLI(t, "set", file, "--add-cover", notImage)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "not a recognized image") {
		t.Errorf("stderr should explain the rejection: %q", errb)
	}

	// --force embeds it anyway; with no recognizable header it stores as octet-stream.
	if _, _, code := runCLI(t, "set", file, "--add-cover", notImage, "--force"); code != 0 {
		t.Fatalf("--force exit = %d, want 0", code)
	}
	out, _, _ := runCLI(t, "--json", "dump", file)
	var jd jsonDocument
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(jd.Pictures) != 1 || jd.Pictures[0].MIME != "application/octet-stream" {
		t.Fatalf("pictures = %+v, want one forced octet-stream cover", jd.Pictures)
	}
}

// TestAddCoverAcceptsRecognizedImage checks a recognized image (a BMP, newly
// supported by the widened sniffer) embeds without --force and sniffs to its true
// MIME instead of degrading to application/octet-stream.
func TestAddCoverAcceptsRecognizedImage(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, notagsFLAC)
	bmp := []byte{
		'B', 'M', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		40, 0, 0, 0, 3, 0, 0, 0, 5, 0, 0, 0, 1, 0, 24, 0,
	}
	cover := filepath.Join(t.TempDir(), "cover.bmp")
	if err := os.WriteFile(cover, bmp, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCLI(t, "set", file, "--add-cover", cover); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	out, _, _ := runCLI(t, "--json", "dump", file)
	var jd jsonDocument
	if err := json.Unmarshal([]byte(out), &jd); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(jd.Pictures) != 1 || jd.Pictures[0].MIME != "image/bmp" {
		t.Fatalf("pictures = %+v, want one image/bmp cover", jd.Pictures)
	}
}

// TestSetExtensionMismatchWarns checks that writing to an output whose extension
// does not match the source format prints a non-fatal warning but still writes
// (WaxLabel does not transcode, so the name is merely misleading, not invalid).
func TestSetExtensionMismatchWarns(t *testing.T) {
	t.Parallel()
	src := copyFixture(t, sampleFLAC)
	dst := filepath.Join(t.TempDir(), "out.mp3")
	_, errb, code := runCLI(t, "set", src, "--set", "TITLE=X", "-o", dst)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (the warning is non-fatal)", code)
	}
	if !strings.Contains(errb, "does not transcode") {
		t.Errorf("stderr should warn about the extension mismatch: %q", errb)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("output should still be written: %v", err)
	}
}

// TestSetExtensionMatchNoWarn checks no warning is printed when the output
// extension matches the source format.
func TestSetExtensionMatchNoWarn(t *testing.T) {
	t.Parallel()
	src := copyFixture(t, sampleFLAC)
	dst := filepath.Join(t.TempDir(), "out.flac")
	_, errb, code := runCLI(t, "set", src, "--set", "TITLE=X", "-o", dst)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.Contains(errb, "transcode") {
		t.Errorf("a matching extension should not warn: %q", errb)
	}
}

// TestSetBulkInPlace edits several files in one invocation and prints a summary.
func TestSetBulkInPlace(t *testing.T) {
	t.Parallel()
	a := copyFixture(t, sampleFLAC)
	b := copyFixture(t, sampleFLAC)
	out, _, code := runCLI(t, "set", a, b, "--set", "TITLE=Bulk")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "2 changed, 0 unchanged, 0 failed") {
		t.Errorf("missing/incorrect summary:\n%s", out)
	}
	for _, f := range []string{a, b} {
		j, _, _ := runCLI(t, "--json", "dump", f)
		var jd jsonDocument
		if err := json.Unmarshal([]byte(j), &jd); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Bulk" {
			t.Errorf("%s TITLE = %v, want [Bulk]", f, got)
		}
	}
}

// TestSetRecursive walks a directory, editing only the audio files it contains
// and skipping unrelated files.
func TestSetRecursive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.flac", "sub/two.flac"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A non-audio file in the tree must be ignored by the extension filter.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, code := runCLI(t, "set", "--recursive", dir, "--set", "ALBUM=Rec")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "2 changed, 0 unchanged, 0 failed") {
		t.Errorf("expected two audio files edited:\n%s", out)
	}
}

// TestSetBulkContinuesPastFailure checks the first error sets the exit class
// while the remaining files still process, reflected in the summary.
func TestSetBulkContinuesPastFailure(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	good := copyFixture(t, sampleFLAC)
	out, _, code := runCLI(t, "set", missing, good, "--set", "TITLE=Bulk")
	if code != 6 { // first failure is the missing file (not-found)
		t.Fatalf("exit = %d, want 6", code)
	}
	if !strings.Contains(out, "1 changed, 0 unchanged, 1 failed") {
		t.Errorf("summary should report one success and one failure:\n%s", out)
	}
	j, _, _ := runCLI(t, "--json", "dump", good)
	var jd jsonDocument
	if err := json.Unmarshal([]byte(j), &jd); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Bulk" {
		t.Errorf("the good file should still be edited: TITLE = %v", got)
	}
}

// TestSetOutputRejectsMultipleInputs checks -o is refused with more than one input.
func TestSetOutputRejectsMultipleInputs(t *testing.T) {
	t.Parallel()
	a := copyFixture(t, sampleFLAC)
	b := copyFixture(t, sampleFLAC)
	dst := filepath.Join(t.TempDir(), "out.flac")
	_, errb, code := runCLI(t, "set", a, b, "-o", dst, "--set", "TITLE=X")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "single file") {
		t.Errorf("want a clear -o-with-many-inputs rejection: %q", errb)
	}
}

// TestPlanBulkJSONArray checks a multi-file plan emits a JSON array.
func TestPlanBulkJSONArray(t *testing.T) {
	t.Parallel()
	a := copyFixture(t, sampleFLAC)
	b := copyFixture(t, sampleFLAC)
	out, _, code := runCLI(t, "--json", "plan", a, b, "--set", "TITLE=X")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var arr []jsonReport
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("multi-file plan should be a JSON array: %v\n%s", err, out)
	}
	if len(arr) != 2 {
		t.Fatalf("got %d plan reports, want 2", len(arr))
	}
}

// TestDumpStdin reads a file from standard input via "-".
func TestDumpStdin(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLIStdin(t, string(data), "dump", "-")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "format:  FLAC") {
		t.Errorf("dump - missing format:\n%s", out)
	}
	if strings.Contains(out, "waxlabel-stdin") {
		t.Errorf("the buffered-stdin temp path leaked into output:\n%s", out)
	}
}

// TestLintStdin lints a tag-only file read from standard input.
func TestLintStdin(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(emptyMP3)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLIStdin(t, string(data), "lint", "-")
	if code != 1 { // no-audio is a LintError -> issues found
		t.Fatalf("exit = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "no-audio") {
		t.Errorf("lint - missing no-audio finding:\n%s", out)
	}
}

// TestVerifyStdinKeepsDisplayName checks verify shows "-" rather than the temp path.
func TestVerifyStdinKeepsDisplayName(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLIStdin(t, string(data), "verify", "-")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "essence:") {
		t.Errorf("verify - missing essence:\n%s", out)
	}
	if strings.Contains(out, "waxlabel-stdin") {
		t.Errorf("the buffered-stdin temp path leaked into output:\n%s", out)
	}
}

// TestDiffStdinAgainstFile diffs standard input against the same file on disk.
func TestDiffStdinAgainstFile(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	_, _, code := runCLIStdin(t, string(data), "diff", "-", sampleFLAC)
	if code != 0 {
		t.Errorf("exit = %d, want 0 (identical metadata)", code)
	}
}

// TestDiffRejectsTwoStdin checks only one operand may read standard input.
func TestDiffRejectsTwoStdin(t *testing.T) {
	t.Parallel()
	_, errb, code := runCLIStdin(t, "x", "diff", "-", "-")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "one operand") {
		t.Errorf("want a clear two-stdin rejection: %q", errb)
	}
}

// TestSetStdinRequiresOutput checks editing standard input in place is rejected.
func TestSetStdinRequiresOutput(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	_, errb, code := runCLIStdin(t, string(data), "set", "-", "--set", "TITLE=X")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "standard input") {
		t.Errorf("want a clear in-place-stdin rejection: %q", errb)
	}
}

// TestSetJSONErrorIsPerFileObject pins set's single-file --json failure shape: a
// per-file result object carrying file + error (consistent with dump/verify/lint),
// not the bare terminal {schemaVersion,error} envelope.
func TestSetJSONErrorIsPerFileObject(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, _, code := runCLI(t, "--json", "set", missing, "--set", "TITLE=X")
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	var res jsonSetResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("expected a per-file result object: %v\n%s", err, out)
	}
	if res.File != missing {
		t.Errorf("file = %q, want %q", res.File, missing)
	}
	if res.Error == nil || res.Error.Code != "not-found" {
		t.Errorf("error = %+v, want code not-found", res.Error)
	}
}

// TestSetRecursiveNoFiles checks a --recursive walk that matches no audio files
// prints a note (rather than silently succeeding) and, in JSON, emits [] not null.
func TestSetRecursiveNoFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("no audio here"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errb, code := runCLI(t, "set", "--recursive", dir, "--set", "TITLE=X")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(errb, "no audio files found") {
		t.Errorf("expected a no-files note on stderr, got: %q", errb)
	}
	out, _, _ := runCLI(t, "--json", "plan", "--recursive", dir, "--set", "TITLE=X")
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("JSON output = %q, want [] (not null)", strings.TrimSpace(out))
	}
}

// TestSetStdinUsageBeatsCoverRead checks the stdin-in-place usage error is
// reported before any --add-cover file is read: the actionable usage error
// (exit 2) wins over a cover read error (exit 6), and no disk read happens.
func TestSetStdinUsageBeatsCoverRead(t *testing.T) {
	t.Parallel()
	missingCover := filepath.Join(t.TempDir(), "cover.jpg")
	_, errb, code := runCLIStdin(t, "ignored", "set", "-", "--add-cover", missingCover, "--set", "TITLE=X")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage), stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "standard input") {
		t.Errorf("want the stdin usage error, got: %q", errb)
	}
	if strings.Contains(errb, "cover image") {
		t.Errorf("the cover file should not have been read: %q", errb)
	}
}

// TestSetStdinToOutput reads from standard input and writes to a file with -o.
func TestSetStdinToOutput(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out.flac")
	_, _, code := runCLIStdin(t, string(data), "set", "-", "-o", dst, "--set", "TITLE=FromStdin")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	j, _, _ := runCLI(t, "--json", "dump", dst)
	var jd jsonDocument
	if err := json.Unmarshal([]byte(j), &jd); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "FromStdin" {
		t.Errorf("TITLE = %v, want [FromStdin]", got)
	}
}

// TestCopyIntoOggReportsChaptersNotModeled exercises the transfer-report render
// end to end: copying a chaptered source onto an Ogg destination (no chapter
// support) must report the drop as "unsupported: not modeled", not the doubled
// "unsupported: unsupported" the old Ogg Representation produced.
func TestCopyIntoOggReportsChaptersNotModeled(t *testing.T) {
	t.Parallel()
	src := filepath.Join("..", "..", "testdata", "chapters.mka")
	dst := copyFixture(t, filepath.Join("..", "..", "testdata", "sample.ogg"))
	out, errb, code := runCLI(t, "copy", src, dst)
	if code != 0 {
		t.Fatalf("copy exit = %d: %s", code, errb)
	}
	if !strings.Contains(out, "unsupported: not modeled") {
		t.Errorf("ogg chapter drop should read 'unsupported: not modeled':\n%s", out)
	}
	if strings.Contains(out, "unsupported: unsupported") {
		t.Errorf("doubled label regressed:\n%s", out)
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
	if je.Error.Code != "not-found" {
		t.Errorf("error code = %q, want not-found", je.Error.Code)
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
	if docs[1].Error == nil || docs[1].Error.Code != "not-found" {
		t.Errorf("second doc should carry a not-found error: %+v", docs[1].Error)
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
