//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// noticeWriter collects writes and closes seen once they contain text.
type noticeWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	text string
	seen chan struct{}
	once sync.Once
}

func (w *noticeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if strings.Contains(w.buf.String(), w.text) {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

// TestSetNotesWaitingOnALock: set says when another process's lock holds up the save, and saves once
// the lock is released.
func TestSetNotesWaitingOnALock(t *testing.T) {
	f := copyFixture(t, sampleFLAC)
	holder, err := os.Open(f)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Skipf("filesystem cannot lock: %v", err)
	}
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			_ = syscall.Flock(int(holder.Fd()), syscall.LOCK_UN)
		}
	}
	defer unlock()

	errw := &noticeWriter{text: "waiting", seen: make(chan struct{})}
	var out bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- dispatch(context.Background(), []string{"set", f, "--set", "TITLE=x"}, strings.NewReader(""), &out, errw)
	}()
	select {
	case <-errw.seen:
	case <-time.After(10 * time.Second):
		unlock()
		<-done
		t.Fatalf("no waiting note; stderr: %s", errw.buf.String())
	}
	unlock()
	if code := <-done; code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errw.buf.String())
	}
	doc, err := wl.ParseFile(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Fields().Title; got != "x" {
		t.Errorf("TITLE after the save = %q, want %q", got, "x")
	}
}
