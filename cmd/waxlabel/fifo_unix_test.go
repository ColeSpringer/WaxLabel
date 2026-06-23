//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mkfifo creates a named pipe for the non-regular-file tests, skipping the test if
// the platform or filesystem refuses one.
func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
}

// runCLIBounded runs the CLI in a goroutine and fails the test if it does not
// finish within d. It is the no-hang assertion for the FIFO tests: os.Open on a
// FIFO's read end blocks until a writer appears and is not context-cancellable, so
// only the stat-first guard (not a ctx timeout) can prevent the block - a regression
// would hang here, which this converts into a prompt failure instead of the package
// timeout. The streams are read only after the goroutine returns (delivered over the
// channel), so the timeout path never races the still-blocked goroutine's buffers.
func runCLIBounded(t *testing.T, d time.Duration, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	type result struct {
		out, errb string
		code      int
	}
	done := make(chan result, 1)
	go func() {
		var out, errb bytes.Buffer
		c := dispatch(context.Background(), args, strings.NewReader(""), &out, &errb)
		done <- result{out.String(), errb.String(), c}
	}()
	select {
	case r := <-done:
		return r.out, r.errb, r.code
	case <-time.After(d):
		t.Fatalf("CLI hung (no return within %s): args=%v", d, args)
		return "", "", 0
	}
}

// TestFifoInputRejectedFast (#1, A1): a FIFO handed to a read command no longer
// hangs (opening a FIFO's read end blocks until a writer appears). A directly-named
// FIFO is rejected fast as a usage error (exit 2) both with and without --recursive,
// with no writer ever attached.
func TestFifoInputRejectedFast(t *testing.T) {
	t.Parallel()
	fifo := filepath.Join(t.TempDir(), "pipe.flac")
	mkfifo(t, fifo)

	t.Run("direct", func(t *testing.T) {
		_, errb, code := runCLIBounded(t, 10*time.Second, "dump", fifo)
		if code != 2 {
			t.Fatalf("exit = %d, want 2 (usage); stderr=%q", code, errb)
		}
		if !strings.Contains(errb, "not a regular file") {
			t.Errorf("stderr should explain the non-regular file: %q", errb)
		}
	})
	t.Run("recursive-direct", func(t *testing.T) {
		if _, _, code := runCLIBounded(t, 10*time.Second, "dump", "--recursive", fifo); code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
	})
}

// TestFifoInWalkedTreeSkipped (A1): a FIFO discovered inside a walked directory is
// skipped like a non-audio file (not a usage error), so a stale pipe cannot wedge a
// batch - while the real audio files in the same tree are still processed, and the
// walk never blocks on the pipe.
func TestFifoInWalkedTreeSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mkfifo(t, filepath.Join(dir, "pipe.flac"))
	real := filepath.Join(dir, "real.flac")
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runCLIBounded(t, 10*time.Second, "--json", "dump", "--recursive", dir)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, errb)
	}
	docs := decodeJSONList[jsonDocument](t, out)
	if len(docs) != 1 {
		t.Fatalf("walk dumped %d files, want 1 (the FIFO must be skipped, real.flac kept)", len(docs))
	}
	if docs[0].Error != nil {
		t.Errorf("real.flac should parse: %+v", docs[0].Error)
	}
}

// TestFifoRejectedByNonExpandingCommands (#3, A1): caps and diff parse operands
// directly (no expandPaths), yet still reject a FIFO fast as a usage error (exit 2)
// with no hang - the checkRegularInputs guard, not just the library backstop.
func TestFifoRejectedByNonExpandingCommands(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.flac")
	mkfifo(t, fifo)
	good := filepath.Join(dir, "good.flac")
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCLIBounded(t, 10*time.Second, "caps", fifo); code != 2 {
		t.Errorf("caps fifo exit = %d, want 2", code)
	}
	if _, _, code := runCLIBounded(t, 10*time.Second, "diff", fifo, good); code != 2 {
		t.Errorf("diff fifo exit = %d, want 2", code)
	}
}

// TestCopyFifoHintOmitsDash verifies that copy's non-regular-file hint does not
// suggest piping a stream in with "-", because copy rejects stdin. It points at a
// regular file path instead. The stdin-reading commands keep the "-" hint.
func TestCopyFifoHintOmitsDash(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.flac")
	mkfifo(t, fifo)
	good := filepath.Join(dir, "good.flac")
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, errb, code := runCLIBounded(t, 10*time.Second, "copy", fifo, good)
	if code != 2 {
		t.Fatalf("copy fifo exit = %d, want 2; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "not a regular file") {
		t.Errorf("copy fifo stderr should explain the non-regular file: %q", errb)
	}
	// The hint must point at a regular file path, not the "-" stream copy rejects.
	if strings.Contains(errb, "pipe a stream") {
		t.Errorf("copy hint must not suggest piping with '-', which copy rejects: %q", errb)
	}
	if !strings.Contains(errb, "regular file path") {
		t.Errorf("copy hint should point at a regular file path: %q", errb)
	}
}
