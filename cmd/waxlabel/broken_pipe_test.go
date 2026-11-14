package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// epipeWriter fails every write with EPIPE, like a closed stdout pipe without a SIGPIPE cancel cause.
type epipeWriter struct{}

func (epipeWriter) Write(p []byte) (int, error) { return 0, syscall.EPIPE }

// closedPipeContext is the run context after the reader of stdout went away.
func closedPipeContext() context.Context {
	ctx, _, pipeClosed := newRunContext(context.Background())
	pipeClosed()
	return ctx
}

// interruptedContext is the run context after a SIGINT.
func interruptedContext() context.Context {
	ctx, interrupt, _ := newRunContext(context.Background())
	interrupt()
	return ctx
}

// TestIsBrokenPipeRecognizesEPIPE: EPIPE must classify on every platform. Windows errnos
// are in pipe_windows_test.go.
func TestIsBrokenPipeRecognizesEPIPE(t *testing.T) {
	t.Parallel()
	if !isBrokenPipe(syscall.EPIPE) {
		t.Error("isBrokenPipe(EPIPE) = false, want true")
	}
	if !isBrokenPipe(fmt.Errorf("write stdout: %w", syscall.EPIPE)) {
		t.Error("isBrokenPipe must unwrap a wrapped EPIPE")
	}
	if isBrokenPipe(errors.New("disk on fire")) {
		t.Error("isBrokenPipe(unrelated error) = true, want false")
	}
	if isBrokenPipe(nil) {
		t.Error("isBrokenPipe(nil) = true, want false")
	}
}

// TestClassifyBrokenPipe: errBrokenPipe is exit 0, code broken-pipe, silent message.
func TestClassifyBrokenPipe(t *testing.T) {
	t.Parallel()
	c := classifyError(errBrokenPipe)
	if c.exitCode != 0 || c.code != "broken-pipe" || c.message != "" {
		t.Errorf("classifyError(errBrokenPipe) = {exit %d, code %q, msg %q}, want {0, broken-pipe, \"\"}",
			c.exitCode, c.code, c.message)
	}
}

// TestBrokenPipeExitsZeroSilently: closed pipe exits 0 silent, not 130 like Ctrl-C. Only
// the cancel cause distinguishes them.
func TestBrokenPipeExitsZeroSilently(t *testing.T) {
	t.Parallel()
	// Two files so the multi-file loop runs.
	dir := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.flac", "b.flac"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var out, errb bytes.Buffer
	code := dispatch(closedPipeContext(), []string{"dump", "--recursive", dir}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Errorf("broken-pipe exit = %d, want 0; stderr=%q", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Errorf("a broken pipe must be silent on stderr, got %q", errb.String())
	}
}

// TestRealCancelStillExits130: broken-pipe carve-out must not swallow Ctrl-C.
func TestRealCancelStillExits130(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	code := dispatch(interruptedContext(), []string{"dump", sampleFLAC}, strings.NewReader(""), &out, &errb)
	if code != 130 {
		t.Errorf("real cancel exit = %d, want 130; stderr=%q", code, errb.String())
	}
}

// TestBrokenPipeSyncEPIPEExitsZero: dump --json can hit synchronous EPIPE before SIGPIPE
// sets a cancel cause.
func TestBrokenPipeSyncEPIPEExitsZero(t *testing.T) {
	t.Parallel()
	var errb bytes.Buffer
	code := dispatch(context.Background(), []string{"--json", "dump", sampleFLAC}, strings.NewReader(""), epipeWriter{}, &errb)
	if code != 0 {
		t.Errorf("synchronous EPIPE on the JSON write exit = %d, want 0 (broken pipe); stderr=%q", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Errorf("a broken pipe must be silent, got stderr %q", errb.String())
	}
}

// TestLintBrokenPipeExitsZeroSilently: lint's per-file loop must honor closed pipe like dump.
func TestLintBrokenPipeExitsZeroSilently(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.flac", "b.flac"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var out, errb bytes.Buffer
	code := dispatch(closedPipeContext(), []string{"lint", "--recursive", dir}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Errorf("broken-pipe lint exit = %d, want 0; stderr=%q", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Errorf("a broken pipe must be silent on stderr, got %q", errb.String())
	}
}

// TestLintBrokenPipeKeepsFindingExit: closed pipe on lint JSON must not drop exit 4 to 0.
func TestLintBrokenPipeKeepsFindingExit(t *testing.T) {
	t.Parallel()
	var errb bytes.Buffer
	code := dispatch(context.Background(), []string{"--json", "lint", emptyMP3}, strings.NewReader(""), epipeWriter{}, &errb)
	if code != 4 {
		t.Errorf("EPIPE on the lint JSON write with an error-severity finding exit = %d, want 4", code)
	}
}

// TestBrokenPipeJSONPreservesRealError: per-file errors outrank broken-pipe.
func TestBrokenPipeJSONPreservesRealError(t *testing.T) {
	t.Parallel()
	junk := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(junk, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	code := dispatch(context.Background(), []string{"--json", "dump", sampleFLAC, junk}, strings.NewReader(""), epipeWriter{}, &errb)
	if code != 3 {
		t.Errorf("EPIPE JSON write with a junk file exit = %d, want 3 (unsupported-format outranks broken-pipe)", code)
	}
}

// twoCopies returns two copies of src in one directory.
func twoCopies(t *testing.T, src string) (a, b string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a, b = filepath.Join(dir, "a"+filepath.Ext(src)), filepath.Join(dir, "b"+filepath.Ext(src))
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return a, b
}

func titleOf(t *testing.T, path string) string {
	t.Helper()
	vals := tagValues(dumpJSON(t, path), "TITLE")
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// TestClosedPipeDoesNotCancelWrites: a reader that goes away ends the output, not the work
// the command was asked to do.
func TestClosedPipeDoesNotCancelWrites(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, args ...string) (stderr string, code int) {
		t.Helper()
		var errb bytes.Buffer
		code = dispatch(closedPipeContext(), args, strings.NewReader(""), epipeWriter{}, &errb)
		return errb.String(), code
	}

	t.Run("set", func(t *testing.T) {
		a, b := twoCopies(t, sampleFLAC)
		if errb, code := run(t, "set", a, b, "--set", "TITLE=changed"); code != 0 || errb != "" {
			t.Fatalf("exit %d, stderr %q; want 0 and silence", code, errb)
		}
		for _, p := range []string{a, b} {
			if got := titleOf(t, p); got != "changed" {
				t.Errorf("%s: TITLE = %q, want the edit applied", filepath.Base(p), got)
			}
		}
	})

	t.Run("set from standard input", func(t *testing.T) {
		data, err := os.ReadFile(sampleFLAC)
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "out.flac")
		var errb bytes.Buffer
		code := dispatch(closedPipeContext(), []string{"set", "-", "--set", "TITLE=changed", "-o", out}, bytes.NewReader(data), epipeWriter{}, &errb)
		if code != 0 || errb.Len() != 0 || titleOf(t, out) != "changed" {
			t.Errorf("exit %d, stderr %q; want the stream read and the output written", code, errb.String())
		}
	})

	t.Run("copy", func(t *testing.T) {
		src, dst := twoCopies(t, sampleFLAC)
		if _, _, code := runCLI(t, "set", src, "--set", "TITLE=changed"); code != 0 {
			t.Fatalf("seed exit %d", code)
		}
		if errb, code := run(t, "copy", src, dst); code != 0 || errb != "" {
			t.Fatalf("exit %d, stderr %q; want 0 and silence", code, errb)
		}
		if got := titleOf(t, dst); got != "changed" {
			t.Errorf("destination TITLE = %q, want the copy applied", got)
		}
	})

	t.Run("lint --fix", func(t *testing.T) {
		a, b := twoCopies(t, sampleFLAC) // holds a transcoder stamp
		if errb, code := run(t, "lint", "--fix", a, b); code != 0 || errb != "" {
			t.Fatalf("exit %d, stderr %q; want 0 and silence", code, errb)
		}
		for _, p := range []string{a, b} {
			if vals := tagValues(dumpJSON(t, p), "ENCODER"); len(vals) != 0 {
				t.Errorf("%s: ENCODER = %v, want the stamp cleared", filepath.Base(p), vals)
			}
		}
	})

	t.Run("clean --remove", func(t *testing.T) {
		dir := t.TempDir()
		old := writeTemp(t, dir, ".waxlabel-1.tmp", 2*time.Hour)
		nested := writeTemp(t, dir, ".waxlabel-2.tmp", 2*time.Hour)
		if errb, code := run(t, "clean", "--recursive", "--remove", dir); code != 0 || errb != "" {
			t.Fatalf("exit %d, stderr %q; want 0 and silence", code, errb)
		}
		for _, p := range []string{old, nested} {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s still there (stat err %v)", filepath.Base(p), err)
			}
		}
	})

	t.Run("export-picture", func(t *testing.T) {
		f := copyFixture(t, sampleFLAC)
		if _, errb, code := runCLI(t, "set", f, "--add-cover", writeTempImage(t, "cover.png", testfile.PNG())); code != 0 {
			t.Fatalf("seed exit %d: %s", code, errb)
		}
		out := filepath.Join(t.TempDir(), "out.png")
		if errb, code := run(t, "export-picture", f, "-o", out); code != 0 || errb != "" {
			t.Fatalf("exit %d, stderr %q; want 0 and silence", code, errb)
		}
		if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, testfile.PNG()) {
			t.Errorf("exported picture missing or wrong (err %v, %d bytes)", err, len(got))
		}
	})
}

// TestClosedPipeKeepsWriteFailures: the exit status of a run whose reader went away is the
// one the work earned.
func TestClosedPipeKeepsWriteFailures(t *testing.T) {
	t.Parallel()
	a, _ := twoCopies(t, sampleFLAC)
	missing := filepath.Join(t.TempDir(), "missing.flac")
	for _, mode := range [][]string{nil, {"--json"}} {
		var errb bytes.Buffer
		args := append(slices.Clone(mode), "set", a, missing, "--set", "TITLE="+strings.Join(mode, "")+"x")
		code := dispatch(closedPipeContext(), args, strings.NewReader(""), epipeWriter{}, &errb)
		if code != 6 {
			t.Errorf("%v: exit %d, want 6 (the missing file); stderr %q", mode, code, errb.String())
		}
		if got := titleOf(t, a); got != strings.Join(mode, "")+"x" {
			t.Errorf("%v: TITLE = %q, want the edit applied", mode, got)
		}
		if human := len(mode) == 0; human != strings.Contains(errb.String(), "no such file") {
			t.Errorf("%v: stderr %q; the per-file line belongs to human mode only", mode, errb.String())
		}
	}
	// A synchronous EPIPE with no cancel behind it loses nothing either.
	var errb bytes.Buffer
	code := dispatch(context.Background(), []string{"--json", "set", a, missing, "--set", "TITLE=sync"}, strings.NewReader(""), epipeWriter{}, &errb)
	if code != 6 || titleOf(t, a) != "sync" {
		t.Errorf("EPIPE on the JSON write: exit %d, TITLE %q; want 6 and the edit applied", code, titleOf(t, a))
	}
}

// TestInterruptCancelsWrites: Ctrl-C stops a write, and a read, whether or not the pipe
// closed first.
func TestInterruptCancelsWrites(t *testing.T) {
	t.Parallel()
	both := func() context.Context {
		ctx, interrupt, pipeClosed := newRunContext(context.Background())
		pipeClosed()
		interrupt()
		return ctx
	}
	for name, ctx := range map[string]context.Context{"interrupt": interruptedContext(), "pipe then interrupt": both()} {
		for _, cmd := range []string{"set", "lint", "copy", "dump", "diff"} {
			a, b := twoCopies(t, sampleFLAC)
			before, err := os.ReadFile(a)
			if err != nil {
				t.Fatal(err)
			}
			args := map[string][]string{
				"set":  {"set", a, b, "--set", "TITLE=changed"},
				"lint": {"lint", "--fix", a, b},
				"copy": {"copy", sampleM4B, a},
				"dump": {"dump", a, b},
				"diff": {"diff", a, b},
			}[cmd]
			var out, errb bytes.Buffer
			if code := dispatch(ctx, args, strings.NewReader(""), &out, &errb); code != 130 {
				t.Errorf("%s, %s: exit %d, want 130; stderr %q", name, cmd, code, errb.String())
			}
			// A command of two operands stops at the first: one line, not one per operand.
			if n := strings.Count(errb.String(), "canceled"); (cmd == "copy" || cmd == "diff") && n != 1 {
				t.Errorf("%s, %s: %d canceled lines, want 1: %q", name, cmd, n, errb.String())
			}
			for _, p := range []string{a, b} {
				if after, _ := os.ReadFile(p); !bytes.Equal(after, before) {
					t.Errorf("%s, %s: %s was written", name, cmd, filepath.Base(p))
				}
			}
		}
	}
}

// TestClosedPipeStopsReadsSilently: a listing nobody reads ends without a line per file.
func TestClosedPipeStopsReadsSilently(t *testing.T) {
	t.Parallel()
	a, b := twoCopies(t, sampleFLAC)
	dir := t.TempDir()
	writeTemp(t, dir, ".waxlabel-1.tmp", 2*time.Hour)
	for _, args := range [][]string{
		{"verify", a, b},
		{"plan", a, b, "--set", "TITLE=x"},
		{"caps", a, b},
		{"diff", a, b},
		{"clean", "--recursive", dir},
	} {
		var errb bytes.Buffer
		code := dispatch(closedPipeContext(), args, strings.NewReader(""), epipeWriter{}, &errb)
		if code != 0 || errb.Len() != 0 {
			t.Errorf("%v: exit %d, stderr %q; want 0 and silence", args, code, errb.String())
		}
	}
}

// TestFailedJSONWriteKeepsTheFailure: when the JSON of a failed run cannot be written either,
// the exit status is still the failure's, for every command that reports one in JSON.
func TestFailedJSONWriteKeepsTheFailure(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	for _, c := range []struct {
		name string
		args []string
		want int
	}{
		{"copy onto a read-only file", []string{"copy", sampleFLAC, copyFixture(t, sampleWMA)}, 3},
		{"copy --strict with a dropped item", []string{"copy", "--strict", sampleM4B, copyFixture(t, td("notags.wv"))}, 2},
		{"clean of a missing directory", []string{"clean", "--remove", missing}, 6},
		{"lint --fix of a missing file", []string{"lint", "--fix", missing + ".flac"}, 6},
	} {
		var errb bytes.Buffer
		code := dispatch(context.Background(), append([]string{"--json"}, c.args...), strings.NewReader(""), epipeWriter{}, &errb)
		if code != c.want {
			t.Errorf("%s: exit %d, want %d; stderr %q", c.name, code, c.want, errb.String())
		}
		// The same run with a reader: the failure is in the JSON, so the case is what it says.
		if out, _, code := runCLI(t, append([]string{"--json"}, c.args...)...); code != c.want || !strings.Contains(out, `"error"`) {
			t.Errorf("%s with a reader: exit %d, want %d and an error in the JSON\n%s", c.name, code, c.want, out)
		}
	}

	// diff answers through its exit status, with a reader or without: 1 where the files differ.
	same, other := twoCopies(t, sampleFLAC)
	if _, errb, code := runCLI(t, "set", "-q", other, "--set", "TITLE=Different"); code != 0 {
		t.Fatalf("seed: exit %d: %s", code, errb)
	}
	for want, b := range map[int]string{1: other, 0: same} {
		for _, ctx := range []context.Context{context.Background(), closedPipeContext()} {
			var errb bytes.Buffer
			if code := dispatch(ctx, []string{"--json", "diff", same, b}, strings.NewReader(""), epipeWriter{}, &errb); code != want {
				t.Errorf("diff --json with no reader: exit %d, want %d; stderr %q", code, want, errb.String())
			}
		}
	}
}
