//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package waxlabel

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/tag"
)

// TestSaveBackReportsWaitingOnALock: a save that finds another process's lock on the file tells
// the caller it is waiting, then commits once the lock is released.
func TestSaveBackReportsWaitingOnALock(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	doc, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Skipf("filesystem cannot lock: %v", err)
	}
	waits := 0
	release := WithLockWait(func() {
		waits++
		_ = syscall.Flock(int(holder.Fd()), syscall.LOCK_UN)
	})
	plan, err := doc.Edit().Set(tag.Title, "after the lock").Prepare(release)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, res, err := plan.Execute(ctx, SaveBack()); err != nil || !res.Committed {
		t.Fatalf("SaveBack: err=%v committed=%v", err, res.Committed)
	}
	if waits != 1 {
		t.Errorf("wait reported %d times, want once", waits)
	}
}

// TestNoOpSaveBackIgnoresALock: a save that writes nothing does not wait on another process's lock.
func TestNoOpSaveBackIgnoresALock(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	doc, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Skipf("filesystem cannot lock: %v", err)
	}
	plan, err := doc.Edit().Prepare(WithLockWait(func() { t.Error("a no-op save reported waiting on the lock") }))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Fatal("setup: an empty edit is not a no-op")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, res, err := plan.Execute(ctx, SaveBack()); err != nil || res.Committed {
		t.Errorf("no-op SaveBack under a held lock: err=%v committed=%v, want (nil, false)", err, res.Committed)
	}
}
