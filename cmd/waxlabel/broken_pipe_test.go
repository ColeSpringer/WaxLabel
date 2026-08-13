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

// epipeWriter fails every write with EPIPE, simulating a reader that closed the output
// pipe. The sanitizing writer passes bytes straight through, so the EPIPE reaches dispatch
// as it would from a real closed stdout, with no SIGPIPE goroutine setting a cancel cause.
type epipeWriter struct{}

func (epipeWriter) Write(p []byte) (int, error) { return 0, syscall.EPIPE }

// TestIsBrokenPipeRecognizesEPIPE: EPIPE must classify the same on every platform,
// including Windows, where only a synthesized one can occur. pipe_windows_test.go covers
// the errnos Windows really returns.
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

// TestClassifyBrokenPipe: errBrokenPipe maps to exit 0, code "broken-pipe", and an empty
// message so renderError stays silent. The leaf the end-to-end tests below rely on.
func TestClassifyBrokenPipe(t *testing.T) {
	t.Parallel()
	c := classifyError(errBrokenPipe)
	if c.exitCode != 0 || c.code != "broken-pipe" || c.message != "" {
		t.Errorf("classifyError(errBrokenPipe) = {exit %d, code %q, msg %q}, want {0, broken-pipe, \"\"}",
			c.exitCode, c.code, c.message)
	}
}

// TestBrokenPipeExitsZeroSilently: a run cancelled by a closed output pipe exits 0 and
// silent, not the 130 a real Ctrl-C yields. Both leave the parse returning
// context.Canceled, so the cancel cause is the only thing telling them apart.
func TestBrokenPipeExitsZeroSilently(t *testing.T) {
	t.Parallel()
	// Two files, so the multi-file loop runs.
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

// TestRealCancelStillExits130 is the companion: the broken-pipe carve-out must not swallow
// a real Ctrl-C.
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

// TestBrokenPipeSyncEPIPEExitsZero guards the JSON race: dump --json writes its array in
// one call, so a closed pipe surfaces as a synchronous EPIPE before the async SIGPIPE
// goroutine sets a cause. The uncancelled context here models exactly that.
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

// TestBrokenPipeJSONPreservesRealError: a genuine per-file error outranks broken-pipe, so
// a junk file among the inputs still exits 3.
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
