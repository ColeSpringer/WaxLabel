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

// runCLIBounded runs the CLI in a goroutine and fails if it does not finish within d.
// FIFO read opens block until a writer appears and ignore context cancel; only stat-first
// rejection avoids the hang. Streams are read after the goroutine returns so a timeout
// does not race blocked I/O buffers.
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

// TestFifoInputRejectedFast: a directly named FIFO is a usage error (exit 2) with or
// without --recursive, without ever opening the read end (which would block).
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

// TestFifoInWalkedTreeSkipped: a FIFO under a walked tree is skipped like a non-audio
// file (not a usage error). Real audio still processes; the walk never opens the pipe.
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

// TestFifoInBatchIsPerElementError: a FIFO between good files is a per-element usage
// error, not a batch abort. The pipe is never opened; good files still process.
func TestFifoInBatchIsPerElementError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.flac")
	mkfifo(t, fifo)
	good1 := filepath.Join(dir, "good1.flac")
	good2 := filepath.Join(dir, "good2.flac")
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{good1, good2} {
		if err := os.WriteFile(g, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, _, code := runCLIBounded(t, 10*time.Second, "--json", "dump", good1, fifo, good2)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (the FIFO's usage class)", code)
	}
	docs := decodeJSONList[jsonDocument](t, out)
	if len(docs) != 3 {
		t.Fatalf("%d elements, want 3 (good, fifo, good)\n%s", len(docs), out)
	}
	if docs[0].Error != nil || docs[2].Error != nil {
		t.Errorf("good files should parse; got [0]=%+v [2]=%+v", docs[0].Error, docs[2].Error)
	}
	if docs[1].Error == nil || docs[1].Error.Code != "usage" {
		t.Errorf("the FIFO element should be a usage error, got %+v", docs[1].Error)
	}
	if docs[1].File != fifo {
		t.Errorf("FIFO element file = %q, want %q", docs[1].File, fifo)
	}
}

// TestFifoRejectedByNonExpandingCommands: caps and diff (no expandPaths) still reject
// a FIFO via checkRegularInputs before any blocking open.
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

// TestCopyFifoHintOmitsDash: copy's non-regular-file hint must not suggest "-"; copy
// rejects stdin. Stdin-reading commands keep the "-" hint.
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
	// Hint must name a regular file path, not "-".
	if strings.Contains(errb, "pipe a stream") {
		t.Errorf("copy hint must not suggest piping with '-', which copy rejects: %q", errb)
	}
	if !strings.Contains(errb, "regular file path") {
		t.Errorf("copy hint should point at a regular file path: %q", errb)
	}
}
