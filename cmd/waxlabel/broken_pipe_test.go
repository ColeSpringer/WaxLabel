package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// epipeWriter fails every write with EPIPE, simulating a downstream reader that closed the
// output pipe. The sanitizing writer writes through immediately (holding back only an incomplete
// UTF-8 tail, and JSON is ASCII), so the EPIPE propagates up to dispatch as it would from a real
// closed stdout - without the async SIGPIPE goroutine setting a cancel cause.
type epipeWriter struct{}

func (epipeWriter) Write(p []byte) (int, error) { return 0, syscall.EPIPE }

// TestClassifyBrokenPipe pins the classification directly: errBrokenPipe maps to exit 0, code
// "broken-pipe", and an empty message so renderError stays silent. This is the leaf the two
// end-to-end tests below depend on.
func TestClassifyBrokenPipe(t *testing.T) {
	t.Parallel()
	c := classifyError(errBrokenPipe)
	if c.exitCode != 0 || c.code != "broken-pipe" || c.message != "" {
		t.Errorf("classifyError(errBrokenPipe) = {exit %d, code %q, msg %q}, want {0, broken-pipe, \"\"}",
			c.exitCode, c.code, c.message)
	}
}

// TestBrokenPipeExitsZeroSilently is the L7 regression: a run whose context was cancelled by a
// closed output pipe (SIGPIPE, cancel cause errBrokenPipe) exits 0 with nothing on stderr - the
// Unix convention for `waxlabel dump --recursive DIR | head` - rather than the 130 a real Ctrl-C
// yields. The cancel cause is the only thing distinguishing the two; both leave the parse
// returning context.Canceled (checkContext). A cancelled context makes ParseFile fail up front,
// which is exactly the state a mid-stream SIGPIPE leaves for the files not yet reached.
func TestBrokenPipeExitsZeroSilently(t *testing.T) {
	t.Parallel()
	// A directory of two files, so the multi-file loop is exercised: neither must emit a
	// "canceled" line, and the aggregate must be exit 0.
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

// TestRealCancelStillExits130 is the companion guard: a genuine interrupt (cancel with the
// default context.Canceled cause, not errBrokenPipe) still exits 130 and reports the cancel, so
// the broken-pipe carve-out does not swallow a real Ctrl-C.
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

// TestBrokenPipeSyncEPIPEExitsZero guards the JSON/single-result race: dump --json writes its
// whole array in one terminal emitJSONList call, so a closed pipe surfaces as a SYNCHRONOUS EPIPE
// with no chance for the async SIGPIPE goroutine to set the cancel cause first. dispatch must
// still map that raw EPIPE to a silent exit 0 (not exit 6 "io"), because WaxLabel only writes to
// stdout/stderr. The context is uncancelled here, exactly modelling that race.
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

// TestBrokenPipeJSONPreservesRealError guards the rank-5 contract: when the terminal JSON write
// EPIPEs but a genuine per-file error was recorded (a junk file among the inputs), that error's
// exit class (unsupported-format, exit 3) must win over broken-pipe, not be discarded to exit 0/6.
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
