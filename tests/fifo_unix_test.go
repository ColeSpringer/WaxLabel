//go:build unix

package waxlabel_test

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestParseFileFifoBackstop (#1, A1) pins the library backstop directly: ParseFile
// on a FIFO must return promptly with an invalid-data error rather than blocking in
// os.Open (which waits for a writer on a FIFO's read end and ignores the context).
// This is the guard that stops the hang for every caller, independent of the CLI's
// own friendlier rejection layered on top.
func TestParseFileFifoBackstop(t *testing.T) {
	t.Parallel()
	fifo := filepath.Join(t.TempDir(), "pipe.flac")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := wl.ParseFile(context.Background(), fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("ParseFile(fifo) error = %v, want ErrInvalidData", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("ParseFile(fifo) hung: os.Open blocked on the FIFO before the regular-file guard")
	}
}
