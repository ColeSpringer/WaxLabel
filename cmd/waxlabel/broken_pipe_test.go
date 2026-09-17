package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// epipeWriter fails every write with EPIPE, like a closed stdout pipe without a SIGPIPE cancel cause.
type epipeWriter struct{}

func (epipeWriter) Write(p []byte) (int, error) { return 0, syscall.EPIPE }

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

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errBrokenPipe)

	var out, errb bytes.Buffer
	code := dispatch(ctx, []string{"dump", "--recursive", dir}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Errorf("broken-pipe exit = %d, want 0; stderr=%q", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Errorf("a broken pipe must be silent on stderr, got %q", errb.String())
	}
}

// TestRealCancelStillExits130: broken-pipe carve-out must not swallow real Ctrl-C.
func TestRealCancelStillExits130(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(nil) // nil cause -> context.Canceled: a real interrupt, not a broken pipe

	var out, errb bytes.Buffer
	code := dispatch(ctx, []string{"dump", sampleFLAC}, strings.NewReader(""), &out, &errb)
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

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errBrokenPipe)

	var out, errb bytes.Buffer
	code := dispatch(ctx, []string{"lint", "--recursive", dir}, strings.NewReader(""), &out, &errb)
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
