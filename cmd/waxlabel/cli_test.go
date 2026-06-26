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

// decodeJSONList unmarshals a list command's --json output into a slice. The list
// commands (dump/verify/lint/set/plan, and caps over files) always emit a JSON
// array, so this is the single decode path for their output; callers assert the
// element count they expect.
func decodeJSONList[T any](t *testing.T, data string) []T {
	t.Helper()
	var arr []T
	if err := json.Unmarshal([]byte(data), &arr); err != nil {
		t.Fatalf("expected a JSON array: %v\n%s", err, data)
	}
	return arr
}

// decodeJSONOne is decodeJSONList for the single-path case: it asserts exactly one
// element and returns it.
func decodeJSONOne[T any](t *testing.T, data string) T {
	t.Helper()
	arr := decodeJSONList[T](t, data)
	if len(arr) != 1 {
		t.Fatalf("array len = %d, want 1\n%s", len(arr), data)
	}
	return arr[0]
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

// TestDumpJSONCodecCanonical checks that properties.codec is the canonical,
// container-neutral name while the container's raw spelling is preserved in
// codecProfile (and omitted when the raw name was already canonical).
func TestDumpJSONCodecCanonical(t *testing.T) {
	t.Parallel()
	sampleOpus := filepath.Join("..", "..", "testdata", "sample.opus")
	cases := []struct{ file, codec, profile string }{
		{sampleM4B, "AAC", "mp4a"},   // MP4 fourcc preserved under canonical AAC
		{sampleFLAC, "FLAC", "flac"}, // FLAC's lowercase preserved
		{sampleOpus, "Opus", ""},     // already canonical: no profile
	}
	for _, c := range cases {
		out, _, code := runCLI(t, "dump", c.file, "--json")
		if code != 0 {
			t.Fatalf("%s: exit = %d\n%s", c.file, code, out)
		}
		jd := decodeJSONOne[jsonDocument](t, out)
		if jd.Properties == nil || jd.Properties.Codec != c.codec || jd.Properties.CodecProfile != c.profile {
			t.Errorf("%s: codec=%q profile=%q, want %q/%q", c.file, jd.Properties.Codec, jd.Properties.CodecProfile, c.codec, c.profile)
		}
	}
}

// TestDumpJSONOmitsBitDepthForLossy is B2: a lossy codec (AAC) decodes to PCM at
// the decoder's chosen depth, so a container-stored "16-bit" is noise. The JSON
// dump zeroes bitsPerSample for such codecs and omitempty drops it - matching the
// text view's bitDepthMeaningful gate so the two never disagree. A lossless FLAC,
// whose depth is a real stored width, keeps it. The AAC fixtures store a literal
// 16 at the parser (see internal/mp4 sampleSize), so this exercises the gate, not
// an already-absent field.
func TestDumpJSONOmitsBitDepthForLossy(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "dump", sampleM4B, "--json")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	// The raw stream must not carry the key at all (omitempty over the zeroed field).
	if strings.Contains(out, "bitsPerSample") {
		t.Errorf("AAC/MP4 dump should omit bitsPerSample:\n%s", out)
	}
	jd := decodeJSONOne[jsonDocument](t, out)
	if jd.Properties == nil || jd.Properties.BitsPerSample != 0 {
		t.Errorf("AAC bitsPerSample = %d, want 0 (omitted)", jd.Properties.BitsPerSample)
	}
	// A lossless FLAC keeps its real, fixed-width depth.
	fout, _, _ := runCLI(t, "dump", sampleFLAC, "--json")
	fd := decodeJSONOne[jsonDocument](t, fout)
	if fd.Properties == nil || fd.Properties.BitsPerSample != 16 {
		t.Errorf("FLAC bitsPerSample = %d, want 16 (a real depth is kept)", fd.Properties.BitsPerSample)
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
	jd := decodeJSONOne[jsonDocument](t, jout)
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
	jd := decodeJSONOne[jsonDocument](t, out)
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
	for _, want := range []string{"native blocks", "STREAMINFO", "VORBIS_COMMENT", "families", "vorbis"} {
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
	jd := decodeJSONOne[jsonDocument](t, out)
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
	// Re-set the title to its current value so the edit resolves to a no-op (a
	// bare `set file` with no edit flags is now a usage error - see TestSetNoEditsRejected).
	out, _, code := runCLI(t, "set", file, "--set", "TITLE=Original Title")
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

// TestSetNoEditsRejected (U1): an in-place `set <file>` with no edit flags is a
// usage error (exit 2) - a forgotten edit flag rather than a deliberate no-op. With
// -o it is a verbatim copy and stays allowed (covered by TestSetOutputNoOpVerbatim).
func TestSetNoEditsRejected(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	_, stderr, code := runCLI(t, "set", file)
	if code != 2 {
		t.Fatalf("set with no edits exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "no edits given") {
		t.Errorf("stderr = %q, want it to mention 'no edits given'", stderr)
	}
	// A write-shaping flag (e.g. --no-padding) counts as an edit, so it is allowed.
	if _, _, code := runCLI(t, "set", file, "--no-padding"); code != 0 {
		t.Errorf("set --no-padding (re-pad in place) exit = %d, want 0", code)
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
	jd := decodeJSONOne[jsonDocument](t, out)
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
	jd := decodeJSONOne[jsonDocument](t, out)
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
	v1 := decodeJSONOne[jsonVerify](t, out1)
	if !strings.HasPrefix(v1.Essence, "sha256/flac-frames-v1:") {
		t.Errorf("essence = %q", v1.Essence)
	}

	file := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", file, "--set", "TITLE=Whatever"); code != 0 {
		t.Fatalf("set exit = %d", code)
	}
	out2, _, _ := runCLI(t, "--json", "verify", file)
	v2 := decodeJSONOne[jsonVerify](t, out2)
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

// TestVerifyQuietTSV (#6): --quiet emits one tab-separated "essence<TAB>path" line
// per file, with no labels and no blank line between records, so the output pipes
// cleanly into sort/uniq for deduplication.
func TestVerifyQuietTSV(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "verify", "-q", sampleFLAC, notagsFLAC)
	if code != 0 {
		t.Fatalf("verify -q exit = %d, want 0", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("verify -q produced %d lines, want exactly 2 (no blank separators):\n%q", len(lines), out)
	}
	for _, line := range lines {
		cols := strings.Split(line, "\t")
		if len(cols) != 2 {
			t.Errorf("line %q has %d tab-separated columns, want 2 (essence, path)", line, len(cols))
		}
		if !strings.HasPrefix(cols[0], "sha256/flac-frames-v1:") {
			t.Errorf("first column %q is not an essence digest", cols[0])
		}
	}
	// The labeled block must not appear in quiet mode.
	if strings.Contains(out, "essence:") {
		t.Errorf("quiet output should not carry the labeled block:\n%s", out)
	}
}

// TestVerifyQuietWholeFileThreeColumns (#6): under --whole-file the quiet line
// carries the whole-file digest as a third column.
func TestVerifyQuietWholeFileThreeColumns(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "verify", "-q", "--whole-file", sampleFLAC)
	if code != 0 {
		t.Fatalf("verify -q --whole-file exit = %d, want 0", code)
	}
	cols := strings.Split(strings.TrimRight(out, "\n"), "\t")
	if len(cols) != 3 {
		t.Fatalf("verify -q --whole-file columns = %d, want 3 (essence, whole-file, path):\n%q", len(cols), out)
	}
	if !strings.Contains(cols[1], "whole-file-v1:") {
		t.Errorf("second column %q should be the whole-file digest", cols[1])
	}
}

// TestVerifyQuietNoOpUnderJSON (#6): --quiet is a text-mode choice; under --json the
// stream shape is unchanged (a JSON array, not TSV).
func TestVerifyQuietNoOpUnderJSON(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "--json", "verify", "-q", sampleFLAC)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.Contains(out, "\t") {
		t.Errorf("--json verify -q should stay JSON, not TSV:\n%s", out)
	}
	v := decodeJSONList[jsonVerify](t, out)
	if len(v) != 1 || v[0].Essence == "" {
		t.Errorf("--json verify -q should emit a normal JSON array: %+v", v)
	}
}

// TestSetQuietSilentOnSuccess (#4): a single-file set -q prints nothing on success
// (no plan preview, no outcome line) on either stream.
func TestSetQuietSilentOnSuccess(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	out, errb, code := runCLI(t, "set", "-q", file, "--set", "TITLE=Quiet")
	if code != 0 {
		t.Fatalf("set -q exit = %d, want 0", code)
	}
	if out != "" || errb != "" {
		t.Errorf("set -q should be silent on success; stdout=%q stderr=%q", out, errb)
	}
	// The edit still applied.
	j, _, _ := runCLI(t, "--json", "dump", file)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "Quiet" {
		t.Errorf("set -q did not apply the edit: TITLE = %v", got)
	}
}

// TestSetQuietKeepsSummaryAndErrors (#4): quiet suppresses the per-file preview and
// outcome but keeps the multi-file summary and any per-file error.
func TestSetQuietKeepsSummaryAndErrors(t *testing.T) {
	t.Parallel()
	good := copyFixture(t, sampleFLAC)
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, errb, code := runCLI(t, "set", "-q", good, missing, "--set", "TITLE=X")
	if code == 0 {
		t.Fatalf("a missing file should fail the run; exit = %d", code)
	}
	// The per-file plan/outcome is gone, but the summary remains (and has no leading
	// blank line, since there is no per-file output above it).
	if strings.Contains(out, "plan") || strings.Contains(out, "Saved") {
		t.Errorf("quiet stdout should omit the per-file preview/outcome:\n%s", out)
	}
	if strings.TrimRight(out, "\n") != "1 changed, 0 unchanged, 1 failed" {
		t.Errorf("quiet stdout should be just the summary, got:\n%q", out)
	}
	// The error still surfaces on stderr.
	if !strings.Contains(errb, "nope.flac") {
		t.Errorf("quiet stderr should still report the failed file:\n%s", errb)
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
		{"chained-stream", fmt.Errorf("w: %w", waxerr.ErrChainedStream), 3, "unsupported-stream"},
		{"unaligned-stream", fmt.Errorf("w: %w", waxerr.ErrUnalignedStream), 3, "unsupported-alignment"},
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
	if c := classifyError(bare); c.message != "/x.flac: no such file or directory" {
		t.Errorf("bare message = %q, want %q", c.message, "/x.flac: no such file or directory")
	}

	// Mirrors the real edit.go wrapping (pictureLoadError), so a regression in that
	// shape surfaces here: it Unwraps to the *fs.PathError (so it stays io/exit 6, not
	// flattened to not-found) but renders the bare cause without Go's "open" verb (M4).
	wrapped := &pictureLoadError{label: "cover image", path: "/x.png", err: &fs.PathError{Op: "open", Path: "/x.png", Err: fs.ErrNotExist}}
	c := classifyError(wrapped)
	if c.code != "io" || c.exitCode != 6 {
		t.Errorf("wrapped class = (%d,%q), want (6,\"io\")", c.exitCode, c.code)
	}
	// "<label>: <path>: <bare cause>" - the bare PathError.Err string, with no "open"
	// verb and no doubled path. (The OS-level "no such file or directory" wording is
	// pinned end to end by TestAddCoverMissingFileContext; here fs.ErrNotExist reads
	// "file does not exist", so derive the want from it rather than hardcode a reason.)
	if want := "cover image: /x.png: " + fs.ErrNotExist.Error(); c.message != want {
		t.Errorf("wrapped message = %q, want %q", c.message, want)
	}
	if strings.Contains(c.message, "open") {
		t.Errorf("wrapped message still carries Go's \"open\" verb: %q", c.message)
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
		waxerr.ErrUnalignedStream, waxerr.ErrInvalidKey,
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
// fails as a usage error (exit 2) naming --recursive, rather than falling through
// to the parser's invalid-data class (exit 4). This both fixes the exit class and
// points the user at the flag that would walk the directory for audio files.
func TestDirectoryAsInput(t *testing.T) {
	t.Parallel()
	_, errb, code := runCLI(t, "dump", t.TempDir())
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	if !strings.Contains(errb, "is a directory") {
		t.Errorf("stderr should explain the directory: %q", errb)
	}
	if !strings.Contains(errb, "--recursive") {
		t.Errorf("stderr should point at --recursive: %q", errb)
	}
}

// TestTempCreateErrorNamesDir checks the atomic-write temp-create failure names
// the destination directory rather than the internal temp pattern. It also
// guards the E2/E3 interaction: the wrapped *fs.PathError must keep this message
// and not be flattened into "no such file: <temp-name>". The trigger is an
// existing-but-unwritable directory (a missing -o dir is now caught up front as a
// usage error - see TestSetOutputParentDirMissing - so it never reaches the write).
func TestTempCreateErrorNamesDir(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not prevent the temp create")
	}
	file := copyFixture(t, sampleFLAC)
	roDir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o755) }) // let TempDir cleanup remove it
	_, errb, code := runCLI(t, "set", file, "--set", "TITLE=X", "-o", filepath.Join(roDir, "out.flac"))
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	if !strings.Contains(errb, "create temp file in "+roDir) {
		t.Errorf("stderr should name the destination dir: %q", errb)
	}
	// The internal temp-file pattern is an implementation detail; it must not leak.
	if strings.Contains(errb, ".waxlabel-") {
		t.Errorf("internal temp pattern should not leak: %q", errb)
	}
}

// TestSetOutputParentDirMissing (#6): a -o path whose parent directory does not
// exist is a usage error (exit 2) reported before the plan prints, not a late
// temp-create io error. A parent that is a regular file is rejected the same way.
func TestSetOutputParentDirMissing(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)

	missing := filepath.Join(t.TempDir(), "no-such-dir", "out.flac")
	out, errb, code := runCLI(t, "set", file, "--set", "TITLE=X", "-o", missing)
	if code != 2 {
		t.Fatalf("missing -o parent: exit = %d, want 2", code)
	}
	if !strings.Contains(errb, "does not exist") {
		t.Errorf("stderr = %q, want it to mention the directory does not exist", errb)
	}
	if strings.Contains(out, "plan") {
		t.Errorf("the usage error should fire before the plan prints; stdout:\n%s", out)
	}

	// A parent that exists but is a regular file (not a directory) is also rejected.
	asFile := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(asFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errb, code = runCLI(t, "set", file, "--set", "TITLE=X", "-o", filepath.Join(asFile, "out.flac"))
	if code != 2 || !strings.Contains(errb, "not a directory") {
		t.Errorf("file-as-parent: code %d, stderr %q; want exit 2 'not a directory'", code, errb)
	}
}

// TestAddCoverMissingFileContext checks a missing cover file is reported with
// "cover image: <path>: <reason>" context (exit 6) and that the message reads cleanly
// - the path named once, no leaked Go "open" verb - so the I/O class is preserved
// while the wording stays user-facing (M4).
func TestAddCoverMissingFileContext(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	missing := filepath.Join(t.TempDir(), "cover.png")
	_, errb, code := runCLI(t, "set", file, "--add-cover", missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	want := "cover image: " + missing + ": no such file or directory"
	if !strings.Contains(errb, want) {
		t.Errorf("stderr should carry the clean cover message %q:\n%s", want, errb)
	}
	// The path is named once (by pictureLoadError, which drops the *fs.PathError's
	// repeated path), not twice.
	if strings.Count(errb, missing) != 1 {
		t.Errorf("cover path should appear once: %q", errb)
	}
	// Go's "open" verb must not leak into the user-facing message.
	if strings.Contains(errb, "open "+missing) {
		t.Errorf("stderr leaked Go's \"open\" verb: %q", errb)
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
	jd := decodeJSONOne[jsonDocument](t, out)
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
	jd := decodeJSONOne[jsonDocument](t, out)
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
		jd := decodeJSONOne[jsonDocument](t, j)
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
	jd := decodeJSONOne[jsonDocument](t, j)
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
	arr := decodeJSONList[jsonReport](t, out)
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
	// The text record header reads "<stdin>", not the bare "-" argument.
	if !strings.Contains(out, "<stdin>") {
		t.Errorf("dump - header should read <stdin>:\n%s", out)
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
	if code != 4 { // no-audio is a LintError -> invalid-data (exit 4), matching verify (F4)
		t.Fatalf("exit = %d, want 4\n%s", code, out)
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
// one-element array whose entry carries file + error (consistent with
// dump/verify/lint), not the bare terminal {schemaVersion,error} envelope.
func TestSetJSONErrorIsPerFileObject(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, _, code := runCLI(t, "--json", "set", missing, "--set", "TITLE=X")
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	res := decodeJSONOne[jsonSetResult](t, out)
	if res.File != missing {
		t.Errorf("file = %q, want %q", res.File, missing)
	}
	if res.Error == nil || res.Error.Code != "not-found" {
		t.Errorf("error = %+v, want code not-found", res.Error)
	}
}

// TestSetRecursiveNoFiles checks a --recursive walk that matches no audio files
// aligns set with its dry-run twin plan: a "no audio files found" note on stderr and
// exit 0 (not a usage error), so the two agree on the empty-walk outcome, with []
// (not null) under --json for both (E1).
func TestSetRecursiveNoFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("no audio here"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errb, code := runCLI(t, "set", "--recursive", dir, "--set", "TITLE=X")
	if code != 0 {
		t.Fatalf("set empty-walk exit = %d, want 0 (aligned with plan)", code)
	}
	if n := strings.Count(errb, "no audio files found"); n != 1 {
		t.Errorf("expected the no-files note exactly once, got %d: %q", n, errb)
	}
	// Under --json, both set and plan emit [] (not null) for the empty walk and exit 0.
	for _, sub := range []string{"set", "plan"} {
		out, _, c := runCLI(t, "--json", sub, "--recursive", dir, "--set", "TITLE=X")
		if c != 0 {
			t.Fatalf("%s --json empty-walk exit = %d, want 0", sub, c)
		}
		if strings.TrimSpace(out) != "[]" {
			t.Errorf("%s --json output = %q, want [] (not null)", sub, strings.TrimSpace(out))
		}
	}
}

// TestLintFixRecursiveNoFiles checks that lint --fix treats a --recursive walk
// matching no audio files as an error (exit 2) rather than a silent success - while
// read-only lint of the same empty walk stays exit 0. Unlike set (which aligns with
// its dry-run twin plan at exit 0, E1), lint --fix has no such twin, so it keeps the
// mutating-command guard.
func TestLintFixRecursiveNoFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("no audio here"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errb, code := runCLI(t, "lint", "--fix", "--recursive", dir)
	if code != 2 {
		t.Fatalf("lint --fix exit = %d, want 2 (usage)", code)
	}
	if n := strings.Count(errb, "no audio files found"); n != 1 {
		t.Errorf("expected the no-files message exactly once, got %d: %q", n, errb)
	}
	if _, _, readCode := runCLI(t, "lint", "--recursive", dir); readCode != 0 {
		t.Errorf("read-only lint exit = %d, want 0", readCode)
	}
}

// TestResolvePaddingFlag (U4): the flag-to-policy resolver maps --padding /
// --no-padding to a write option plus whether a flag was given, and rejects misuse,
// independent of any file.
func TestResolvePaddingFlag(t *testing.T) {
	t.Parallel()
	// Neither flag set: no option (default policy untouched), no flag given.
	if opt, given, err := resolvePaddingFlag("", false); opt != nil || given || err != nil {
		t.Errorf("no flags: opt=%v given=%v err=%v, want nil,false,nil", opt, given, err)
	}
	// Valid forms produce an option and report a flag was given.
	for _, c := range []struct {
		padding   string
		noPadding bool
		desc      string
	}{
		{"16384", false, "--padding 16384"},
		{"", true, "--no-padding"},
		// "--padding 0" is the no-padding synonym: a parsed 0 is valid, not misuse. The
		// WriteOption closures are not directly comparable, so the behavioral equivalence
		// to --no-padding is proven by TestPaddingZeroShrinksLikeNoPadding.
		{"0", false, "--padding 0"},
		{"200000", false, "--padding 200000 (floor sets Min=Target)"},
		// U4: --padding and --no-padding combine cleanly when --padding is any spelling
		// of zero (they then agree), rather than being rejected by a string "!= 0" test.
		{"0", true, "--padding 0 --no-padding"},
		{"00", true, "--padding 00 --no-padding"},
		{" 0 ", true, "--padding ' 0 ' --no-padding"},
	} {
		if opt, given, err := resolvePaddingFlag(c.padding, c.noPadding); opt == nil || !given || err != nil {
			t.Errorf("%s: opt=%v given=%v err=%v, want option,true,nil", c.desc, opt, given, err)
		}
	}
	// Misuse is a usage error: a *positive* padding alongside --no-padding (they
	// contradict), a negative count, a non-integer, and an absurd byte count above the
	// sanity cap (B1's floor makes a huge value reachable from a plain edit, so it must
	// be rejected, not allocated).
	// "   " is a degenerate but explicit value (not the unset "" sentinel), so it is a
	// bad byte count, not silently the default.
	for _, c := range []struct {
		padding   string
		noPadding bool
	}{{"16384", true}, {"-1", false}, {"abc", false}, {"99999999999", false}, {"   ", false}} {
		if _, _, err := resolvePaddingFlag(c.padding, c.noPadding); err == nil || !isUsageError(err) {
			t.Errorf("resolvePaddingFlag(%q, %v) err = %v, want usage error", c.padding, c.noPadding, err)
		}
	}
}

// TestPaddingNoPadding (U3): --no-padding drops the padding the default reserves,
// and the default plan advertises the controls.
func TestPaddingNoPadding(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	def, _, code := runCLI(t, "plan", file, "--set", "TITLE=Pad")
	if code != 0 {
		t.Fatalf("plan exit = %d", code)
	}
	if !strings.Contains(def, "padding:") {
		t.Fatalf("default plan should show a padding line; got:\n%s", def)
	}
	if !strings.Contains(def, "--padding") || !strings.Contains(def, "--no-padding") {
		t.Errorf("padding line should advertise its controls; got:\n%s", def)
	}
	out, _, code := runCLI(t, "plan", file, "--set", "TITLE=Pad", "--no-padding")
	if code != 0 {
		t.Fatalf("plan --no-padding exit = %d", code)
	}
	// FLAC has a padding concept, so --no-padding now confirms it positively
	// ("padding: none") rather than omitting the line (U7).
	if !strings.Contains(out, "padding: none") {
		t.Errorf("--no-padding should confirm 'padding: none'; got:\n%s", out)
	}
}

// TestPaddingZeroShrinksLikeNoPadding (U1): "--padding 0" means no padding, the
// same as --no-padding. It must drop the default-reserved padding and produce a
// file the same size as the --no-padding write - not keep the existing region in
// place, which is the ReuseInPlace behavior a positive --padding floor uses.
func TestPaddingZeroShrinksLikeNoPadding(t *testing.T) {
	t.Parallel()
	sizeAfter := func(extra ...string) int64 {
		file := copyFixture(t, sampleFLAC)
		args := append([]string{"set", file, "--set", "TITLE=Zero"}, extra...)
		if _, errb, code := runCLI(t, args...); code != 0 {
			t.Fatalf("set %v exit = %d: %s", extra, code, errb)
		}
		fi, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	def := sizeAfter()                  // default 8 KiB padding
	none := sizeAfter("--no-padding")   // padding stripped
	zero := sizeAfter("--padding", "0") // U1: must behave like --no-padding
	if zero >= def {
		t.Errorf("--padding 0 size %d should be smaller than the default-padded %d", zero, def)
	}
	if zero != none {
		t.Errorf("--padding 0 size %d should equal the --no-padding size %d", zero, none)
	}
}

// TestPaddingPresetPrecedence (U3): an explicit --padding overrides the preset's
// padding policy, so "--preset minimal --padding N" reserves padding even though
// minimal alone writes none.
func TestPaddingPresetPrecedence(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	bare, _, _ := runCLI(t, "plan", file, "--set", "TITLE=Pad", "--preset", "minimal")
	if !strings.Contains(bare, "padding: none") {
		t.Errorf("--preset minimal alone should confirm 'padding: none' (U7); got:\n%s", bare)
	}
	over, _, code := runCLI(t, "plan", file, "--set", "TITLE=Pad", "--preset", "minimal", "--padding", "16384")
	if code != 0 {
		t.Fatalf("plan exit = %d", code)
	}
	// The override writes a real region, so a byte value (not "padding: none").
	if !strings.Contains(over, "padding:") || strings.Contains(over, "padding: none") {
		t.Errorf("--padding should override the preset's zero padding; got:\n%s", over)
	}
}

// TestPaddingFlagValidation (U3): combining the flags, or a negative/non-integer
// value, is a usage error (exit 2) through the CLI.
func TestPaddingFlagValidation(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	for _, args := range [][]string{
		{"plan", file, "--padding", "1024", "--no-padding"},
		{"plan", file, "--padding", "-1"},
		{"plan", file, "--padding", "abc"},
		{"plan", file, "--padding", "99999999999"}, // above the 64 MiB sanity cap
	} {
		if _, _, code := runCLI(t, args...); code != 2 {
			t.Errorf("args %v exit = %d, want 2 (usage)", args, code)
		}
	}
}

// TestPaddingFloorGrowsRegion (B1): --padding N is a floor, not just a target -
// even an edit that fits the existing (small) padding must grow the region to at
// least N rather than silently reusing the smaller leftover.
func TestPaddingFloorGrowsRegion(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	planPadding := func(args ...string) int64 {
		out, _, code := runCLI(t, append([]string{"--json", "plan", file}, args...)...)
		if code != 0 {
			t.Fatalf("plan exit = %d for %v", code, args)
		}
		return decodeJSONList[jsonReport](t, out)[0].PaddingAfter
	}
	// The fixture reuses its small (~8 KB) region by default.
	if def := planPadding("--set", "TITLE=X"); def > 100000 {
		t.Fatalf("fixture default padding = %d, expected the small reused region", def)
	}
	// With the floor, padding grows to the requested 200000 instead of reusing the
	// smaller region (the B1 bug, where --padding was silently ignored on reuse).
	if floor := planPadding("--set", "TITLE=X", "--padding", "200000"); floor < 200000 {
		t.Errorf("--padding 200000 PaddingAfter = %d, want >= 200000 (floor)", floor)
	}
}

// TestMalformedValueNotes (M1): a malformed numeric or date value is noted on
// stderr, the write still succeeds, and under --json the note is suppressed.
func TestMalformedValueNotes(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", file, "--set", "TRACKNUMBER=abc", "--set", "RECORDINGDATE=banana")
	if code != 0 {
		t.Fatalf("set exit = %d (a note must not fail the write); stderr:\n%s", code, errb)
	}
	if !strings.Contains(errb, "TRACKNUMBER=abc does not look like a number") {
		t.Errorf("expected a numeric note; stderr:\n%s", errb)
	}
	if !strings.Contains(errb, "RECORDINGDATE=banana is not YYYY") {
		t.Errorf("expected a date note; stderr:\n%s", errb)
	}
	// --json suppresses the note (it would corrupt the machine stream).
	out, jerr, _ := runCLI(t, "--json", "plan", file, "--set", "TRACKNUMBER=abc")
	if strings.Contains(jerr, "does not look like a number") {
		t.Errorf("note should be suppressed under --json; stderr:\n%s", jerr)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("--json output should be a JSON array; got:\n%s", out)
	}
}

// TestMalformedValueNotesTolerant (M1): values ParseNumPair / ValidPartialDate
// accept - whitespace, "n/total", a leading sign, partial dates - are not flagged.
func TestMalformedValueNotesTolerant(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", file,
		"--set", "TRACKNUMBER= 3 ",
		"--set", "DISCNUMBER=1/2",
		"--set", "PLAYCOUNT=-1",
		"--set", "RECORDINGDATE=2021-06")
	if code != 0 {
		t.Fatalf("set exit = %d; stderr:\n%s", code, errb)
	}
	if strings.Contains(errb, "does not look like a number") || strings.Contains(errb, "is not YYYY") {
		t.Errorf("ParseNumPair-tolerant values should not be flagged; stderr:\n%s", errb)
	}
}

// TestValueNotesDeferredUntilFiles (#4): the invocation-level value note must not
// print on a run that acts on no real file - otherwise it falsely claims a value was
// "written as-is" when nothing was written.
func TestValueNotesDeferredUntilFiles(t *testing.T) {
	t.Parallel()
	// A directory without --recursive is now a per-element usage error (exit 2), not a
	// whole-batch abort, but it is still not an actionable input - so the value note
	// must stay silent (anyInputExists skips a path with a recorded pre-flight error).
	_, errb, code := runCLI(t, "set", t.TempDir(), "--set", "TRACKNUMBER=abc")
	if code != 2 {
		t.Fatalf("directory exit = %d, want 2", code)
	}
	if strings.Contains(errb, "does not look like a number") {
		t.Errorf("value note must not print on a directory-only run:\n%s", errb)
	}
	// An empty --recursive walk now aligns with plan: exit 0 with a "nothing to do"
	// advisory, not a usage error (E1). The value note still must not print, since no
	// file was acted on.
	_, errb, code = runCLI(t, "set", t.TempDir(), "--recursive", "--set", "TRACKNUMBER=abc")
	if code != 0 {
		t.Fatalf("empty-walk exit = %d, want 0", code)
	}
	if strings.Contains(errb, "does not look like a number") {
		t.Errorf("value note must not print on an empty walk:\n%s", errb)
	}
}

// TestEmptyValueNote checks the advisory for --set KEY=. At this point no target file
// has been inspected, so the message can only say that some formats may drop the empty
// value. It must also avoid pairing that note with the malformed-value warning.
func TestEmptyValueNote(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", file, "--set", "TITLE=")
	if code != 0 {
		t.Fatalf("set exit = %d; stderr:\n%s", code, errb)
	}
	if !strings.Contains(errb, "TITLE= writes an empty value") || !strings.Contains(errb, "--clear TITLE") {
		t.Errorf("expected an empty-value note suggesting --clear; stderr:\n%s", errb)
	}
	if !strings.Contains(errb, "drop") {
		t.Errorf("empty-value note should state the drop possibility, not assert retention; stderr:\n%s", errb)
	}
}

// TestWhitespaceNumericNote checks that whitespace-only numeric input follows the same
// trim rule as the writer: it becomes empty and uses the empty-value note, not the
// malformed-value note.
func TestWhitespaceNumericNote(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", file, "--set", "TRACKNUMBER=   ")
	if code != 0 {
		t.Fatalf("set exit = %d; stderr:\n%s", code, errb)
	}
	if strings.Contains(errb, "kept as text") {
		t.Errorf("whitespace-only numeric must not take the malformed-value note (it is trimmed to empty); stderr:\n%s", errb)
	}
	if !strings.Contains(errb, "TRACKNUMBER= writes an empty value") {
		t.Errorf("whitespace-only numeric should take the empty-value note; stderr:\n%s", errb)
	}
}

// TestDumpSanitizesEndToEnd (R1): a tag value carrying an ESC/CR survives in the
// file but is escaped on dump - no raw control byte reaches the terminal.
func TestDumpSanitizesEndToEnd(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", file, "--set", "TITLE=x\x1b[31my\rz"); code != 0 {
		t.Fatalf("set exit = %d", code)
	}
	out, _, code := runCLI(t, "dump", file)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if strings.ContainsAny(out, "\x1b\r") {
		t.Errorf("dump leaked a raw control byte:\n%q", out)
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf("dump should show the escaped form:\n%s", out)
	}
}

// TestDiffSanitized (D1/R1): the diff command's change preview escapes control
// bytes too, since it now shares tag.Change.String() with the write-plan preview.
func TestDiffSanitized(t *testing.T) {
	t.Parallel()
	a := copyFixture(t, sampleFLAC)
	b := copyFixture(t, sampleFLAC)
	if _, _, code := runCLI(t, "set", b, "--set", "TITLE=clean\x1bX"); code != 0 {
		t.Fatalf("set exit = %d", code)
	}
	out, _, _ := runCLI(t, "diff", a, b) // differing files exit 1; the diff is on stdout
	if strings.Contains(out, "\x1b") {
		t.Errorf("diff leaked a raw ESC:\n%q", out)
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf("diff should show the escaped title change:\n%s", out)
	}
}

// makeAudioTree writes two FLAC fixtures (one nested) plus a non-audio file into a
// fresh temp dir, returning the dir. It backs the --recursive read-command tests.
func makeAudioTree(t *testing.T) string {
	t.Helper()
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
	return dir
}

// TestDumpRecursive walks a directory tree and dumps every audio file found,
// skipping non-audio files via the extension filter.
func TestDumpRecursive(t *testing.T) {
	t.Parallel()
	dir := makeAudioTree(t)
	out, _, code := runCLI(t, "--json", "dump", "--recursive", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	docs := decodeJSONList[jsonDocument](t, out)
	if len(docs) != 2 {
		t.Fatalf("dumped %d files, want 2 (two FLACs; notes.txt skipped)", len(docs))
	}
}

// TestVerifyRecursive walks a tree and computes an essence digest for each file.
func TestVerifyRecursive(t *testing.T) {
	t.Parallel()
	dir := makeAudioTree(t)
	out, _, code := runCLI(t, "--json", "verify", "--recursive", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	vs := decodeJSONList[jsonVerify](t, out)
	if len(vs) != 2 {
		t.Fatalf("verified %d files, want 2", len(vs))
	}
	for _, v := range vs {
		if v.Essence == "" {
			t.Errorf("missing essence digest for %s", v.File)
		}
	}
}

// TestLintRecursive walks a tree and lints every audio file found. The exit code
// is left unchecked because the fixtures may carry warning-level findings (exit 1),
// which is orthogonal to the recursion under test.
func TestLintRecursive(t *testing.T) {
	t.Parallel()
	dir := makeAudioTree(t)
	out, _, _ := runCLI(t, "--json", "lint", "--recursive", dir)
	ls := decodeJSONList[jsonLint](t, out)
	if len(ls) != 2 {
		t.Fatalf("linted %d files, want 2", len(ls))
	}
}

// TestDumpRecursiveNoFiles: a directory with no audio files notes it on stderr and
// still emits an empty JSON array (not null), the shared no-files behavior.
func TestDumpRecursiveNoFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("no audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runCLI(t, "--json", "dump", "--recursive", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(errb, "no audio files found") {
		t.Errorf("expected a no-files note on stderr, got: %q", errb)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("JSON output = %q, want [] (not null)", strings.TrimSpace(out))
	}
}

// TestSetUnknownKeyNote: an unknown --set key is written as a custom field with a
// one-line stderr note (the run still succeeds, exit 0), followed by a single
// trailing hint (M2) pointing at the keys command - emitted once even for several
// unknown keys.
func TestSetUnknownKeyNote(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	_, errb, code := runCLI(t, "set", f, "--set", "TITEL=typo", "--set", "ARTST=who")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errb)
	}
	if !strings.Contains(errb, "TITEL is not a known key") || !strings.Contains(errb, "ARTST is not a known key") {
		t.Errorf("expected per-key unknown-key notes on stderr, got: %q", errb)
	}
	// The discovery hint appears exactly once, after the per-key lines.
	if n := strings.Count(errb, "waxlabel keys"); n != 1 {
		t.Errorf("keys hint should appear exactly once for multiple unknown keys, got %d:\n%s", n, errb)
	}
	j, _, _ := runCLI(t, "--json", "dump", f)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "TITEL"); len(got) != 1 || got[0] != "typo" {
		t.Errorf("the custom field should still be written: TITEL = %v", got)
	}
}

// TestSetStrictUnknownKeyFails: --strict turns an unknown key into a usage error
// (exit 2) before any file is touched.
func TestSetStrictUnknownKeyFails(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	_, _, code := runCLI(t, "set", f, "--set", "TITEL=typo", "--strict")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
	j, _, _ := runCLI(t, "--json", "dump", f)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "TITEL"); got != nil {
		t.Errorf("strict run touched the file: TITEL = %v, want nothing", got)
	}
}

// TestSetUnknownKeyJSONClean: notes never pollute the --json stream (stdout stays a
// clean array, stderr carries no note).
func TestSetUnknownKeyJSONClean(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	out, errb, code := runCLI(t, "--json", "set", f, "--set", "TITEL=x")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.Contains(errb, "note:") {
		t.Errorf("a note leaked to stderr under --json: %q", errb)
	}
	if n := len(decodeJSONList[jsonSetResult](t, out)); n != 1 {
		t.Errorf("JSON array len = %d, want 1", n)
	}
}

// TestPlanSingleValuedMultiNote: pushing a single-valued key past one value
// surfaces it as a plan-report warning (in stdout, not a separate stderr note);
// the preview still prints and the run succeeds (exit 0).
func TestPlanSingleValuedMultiNote(t *testing.T) {
	t.Parallel()
	out, errb, code := runCLI(t, "plan", sampleFLAC, "--add", "ENCODER=a", "--add", "ENCODER=b")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errb)
	}
	if !strings.Contains(out, "single-valued-multi") || !strings.Contains(out, "ENCODER is single-valued") {
		t.Errorf("expected single-valued-multi warning in the report, got stdout: %q", out)
	}
	// The signal now lives on the report; it is no longer printed twice as a stderr note.
	if strings.Contains(errb, "note: ENCODER is single-valued") {
		t.Errorf("single-valued signal should not also be a stderr note: %q", errb)
	}
}

// TestSetCustomMultiValueNoSingleValuedNote: a custom key explicitly given several
// values gets the unknown-key note but not the single-valued-multi note - a custom
// field legitimately holds a list (the values read back in full).
func TestSetCustomMultiValueNoSingleValuedNote(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, notagsFLAC)
	_, errb, code := runCLI(t, "set", f, "--add", "MY_CUSTOM=a", "--add", "MY_CUSTOM=b")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errb)
	}
	if !strings.Contains(errb, "MY_CUSTOM is not a known key") {
		t.Errorf("expected the unknown-key note, got: %q", errb)
	}
	if strings.Contains(errb, "single-valued") {
		t.Errorf("a custom key should not trigger the single-valued note: %q", errb)
	}
	j, _, _ := runCLI(t, "--json", "dump", f)
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "MY_CUSTOM"); len(got) != 2 {
		t.Errorf("custom field should hold both values: MY_CUSTOM = %v", got)
	}
}

// TestSetStrictSingleValuedMultiFails: --strict fails a file whose edit pushes a
// single-valued key past one value (exit 2).
func TestSetStrictSingleValuedMultiFails(t *testing.T) {
	t.Parallel()
	f := copyFixture(t, sampleFLAC)
	_, _, code := runCLI(t, "set", f, "--add", "ENCODER=a", "--add", "ENCODER=b", "--strict")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", code)
	}
}

// TestSetSingleValuedMultiPerFileWarning: across a --recursive walk each offending
// file carries the single-valued-multi signal in its own plan report (one per
// file), and it is never emitted as a separate stderr note.
func TestSetSingleValuedMultiPerFileWarning(t *testing.T) {
	t.Parallel()
	dir := makeAudioTree(t) // two FLAC fixtures, each already carrying an ENCODER
	out, errb, _ := runCLI(t, "set", "--recursive", dir, "--add", "ENCODER=a", "--add", "ENCODER=b")
	if n := strings.Count(out, "single-valued-multi"); n != 2 {
		t.Errorf("single-valued-multi report warning appeared %d times, want 1 per file (2)", n)
	}
	if strings.Contains(errb, "note: ENCODER is single-valued") {
		t.Errorf("single-valued signal should not be a stderr note: %q", errb)
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
	jd := decodeJSONOne[jsonDocument](t, j)
	if got := tagValues(jd, "TITLE"); len(got) != 1 || got[0] != "FromStdin" {
		t.Errorf("TITLE = %v, want [FromStdin]", got)
	}
}

// TestCopyIntoOggReportsChaptersNotModeled exercises the transfer-report render
// end to end: copying a chaptered source onto an Ogg destination (no chapter
// support) must report the drop with the destination-focused reason
// "destination format does not store chapters", never leaking the source-side
// Representation string ("unsupported", "not modeled") into the user's report.
func TestCopyIntoOggReportsChaptersNotModeled(t *testing.T) {
	t.Parallel()
	src := filepath.Join("..", "..", "testdata", "chapters.mka")
	dst := copyFixture(t, filepath.Join("..", "..", "testdata", "sample.ogg"))
	out, errb, code := runCLI(t, "copy", src, dst)
	if code != 0 {
		t.Fatalf("copy exit = %d: %s", code, errb)
	}
	if !strings.Contains(out, "destination format does not store chapters") {
		t.Errorf("ogg chapter drop should read 'destination format does not store chapters':\n%s", out)
	}
	if strings.Contains(out, "unsupported") {
		t.Errorf("source-side Representation jargon leaked into the report:\n%s", out)
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

// TestEmptyFileExitClass (M3) pins that an empty file is uniformly an
// unsupported-format failure (exit 3) regardless of its extension: an empty file
// carries no signature, so its format cannot be identified - the .flac extension
// must not steer it into the FLAC parser and a different (invalid-data) class.
func TestEmptyFileExitClass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"zero.flac", "zero.bin"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		out, _, code := runCLI(t, "--json", "dump", path)
		if code != 3 {
			t.Errorf("%s exit = %d, want 3 (unsupported-format)", name, code)
		}
		// The classified error names the empty-file cause so the failure is actionable.
		jr := decodeJSONOne[jsonDocument](t, out)
		if jr.Error == nil || jr.Error.Code != "unsupported-format" {
			t.Errorf("%s error = %+v, want code unsupported-format", name, jr.Error)
		}
		if jr.Error != nil && !strings.Contains(jr.Error.Message, "empty file") {
			t.Errorf("%s message = %q, want it to mention the empty file", name, jr.Error.Message)
		}
	}
}

// TestPlanJSONErrorIsPerFileObject pins plan's single-file --json failure shape:
// like set, a one-element array whose entry carries the classified per-file error
// (plan is a per-file command), not the bare terminal {schemaVersion,error}
// envelope - that envelope is reserved for command-resolution failures, covered by
// TestJSONErrorRoutingOnEarlyAbort.
func TestPlanJSONErrorIsPerFileObject(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.flac")
	out, _, code := runCLI(t, "--json", "plan", missing)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	jr := decodeJSONOne[jsonReport](t, out)
	if jr.File != missing {
		t.Errorf("file = %q, want %q", jr.File, missing)
	}
	if jr.Error == nil || jr.Error.Code != "not-found" {
		t.Errorf("error = %+v, want code not-found", jr.Error)
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
	docs := decodeJSONList[jsonDocument](t, out)
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
// resolution (before it binds the persistent flag). The envelope shape follows the
// resolved command: an unknown command stays a bare object, while a bad flag on a
// list command (dump) is wrapped in that command's documented one-element array (E2).
func TestJSONErrorRoutingOnEarlyAbort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args []string
		list bool // a list command wraps its pre-flight error in a one-element array
	}{
		{[]string{"--json", "frobnicate", "x"}, false},           // unknown command -> object
		{[]string{"dump", "--nope", "--json", sampleFLAC}, true}, // bad flag on dump -> array
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			out, errb, code := runCLI(t, tc.args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			var body jsonErrBody
			if tc.list {
				body = decodeJSONOne[jsonError](t, out).Error // asserts a single-element array
			} else {
				var je jsonError
				if err := json.Unmarshal([]byte(out), &je); err != nil {
					t.Fatalf("stdout is not a JSON object envelope: %v\nstdout=%q stderr=%q", err, out, errb)
				}
				body = je.Error
			}
			if body.Code != "usage" {
				t.Errorf("error code = %q, want usage", body.Code)
			}
		})
	}
}

// TestSetShowsPlanBeforeFailedWrite checks that the plan preview is printed even
// when the write fails, matching the help ("the plan is printed before the
// outcome"). The trigger is an IN-PLACE edit of a file in an existing-but-unwritable
// directory: the file stays readable, so the parse and plan succeed, and only the
// atomic in-place write's temp create then fails (io). An -o write to an unwritable dir
// is instead caught by the up-front writability probe before the plan renders, so this
// uses the in-place path, which has no such probe (TestTempCreateErrorNamesDir covers
// the -o probe's exit code and message).
func TestSetShowsPlanBeforeFailedWrite(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not prevent the write")
	}
	roDir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real file in the dir, then make the dir read-only: the file stays readable (parse
	// and plan succeed) while a temp create for the in-place atomic write fails.
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(roDir, "in.flac")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o755) })
	out, _, code := runCLI(t, "set", file, "--set", "TITLE=X")
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

// TestErrClassRankCoversEveryErrorClass (B1) pins the invariant worseError relies
// on: every error class classifyError can produce has an entry in errClassRank. A
// missing entry would silently fall to rank 0 - below the generic "error" (10) - so
// in a multi-file run that class would lose the aggregate exit code to any other
// failure. The check is bidirectional, so the rank map and the classified vocabulary
// cannot drift apart: if you add a class to classifyError, add it to errClassRank and
// to the samples here.
func TestErrClassRankCoversEveryErrorClass(t *testing.T) {
	t.Parallel()
	samples := []error{
		&usageError{msg: "bad usage"},
		waxerr.ErrInvalidKey,
		waxerr.ErrNeedsFile,
		waxerr.ErrUnsupportedFormat,
		waxerr.ErrUnsupportedTag,
		waxerr.ErrChainedStream,
		waxerr.ErrUnalignedStream,
		waxerr.ErrSourceChanged,
		waxerr.ErrInvalidData,
		waxerr.ErrNoTags,
		&fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist},             // not-found
		&fs.PathError{Op: "open", Path: "x", Err: errors.New("disk failure")}, // io
		context.Canceled,
		context.DeadlineExceeded,
		errors.New("some unclassified failure"), // error
	}
	seen := map[string]bool{}
	for _, err := range samples {
		code := classifyError(err).code
		seen[code] = true
		if _, ranked := errClassRank[code]; !ranked {
			t.Errorf("classifyError(%v) code %q has no errClassRank entry; worseError would sink it to 0", err, code)
		}
	}
	for code := range errClassRank {
		if !seen[code] {
			t.Errorf("errClassRank has %q, which no sampled error produces; add a sample or remove the rank", code)
		}
	}
	// Lock the headline B1 rationale: a corrupt file outranks a wrong path, which
	// outranks a bad invocation.
	if !(errClassRank["invalid-data"] > errClassRank["not-found"] && errClassRank["not-found"] > errClassRank["usage"]) {
		t.Errorf("precedence broken: want invalid-data(%d) > not-found(%d) > usage(%d)",
			errClassRank["invalid-data"], errClassRank["not-found"], errClassRank["usage"])
	}
}
