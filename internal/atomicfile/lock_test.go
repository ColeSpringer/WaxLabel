//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package atomicfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func openTwice(t *testing.T) (a, b *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// TestLockSerializesDescriptors: a second descriptor waits while the first holds the lock and
// acquires it once released.
func TestLockSerializesDescriptors(t *testing.T) {
	a, b := openTwice(t)
	unlock, err := Lock(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, b, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock while held: err = %v, want context.DeadlineExceeded", err)
	}
	unlock()
	unlockB, err := Lock(context.Background(), b, nil)
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	unlockB()
}

// TestLockWithoutLockSupportProceeds: a filesystem that cannot lock (NFS without lockd, some
// FUSE) leaves the save unserialized rather than failing it.
func TestLockWithoutLockSupportProceeds(t *testing.T) {
	t.Cleanup(func() { flock = syscall.Flock })
	a, _ := openTwice(t)
	for _, errno := range []syscall.Errno{syscall.ENOLCK, syscall.ENOTSUP, syscall.EOPNOTSUPP} {
		flock = func(int, int) error { return errno }
		unlock, err := Lock(context.Background(), a, nil)
		if err != nil {
			t.Errorf("%v: Lock err = %v, want nil", errno, err)
		} else {
			unlock()
		}
	}
}

// TestLockReportsAWaitOnce: a wait on another descriptor's lock is reported once, however long it
// polls; an uncontended lock reports nothing.
func TestLockReportsAWaitOnce(t *testing.T) {
	lockNoticeAfter = 0
	t.Cleanup(func() { lockNoticeAfter = time.Second })
	a, b := openTwice(t)
	waits := 0
	waiting := func() { waits++ }

	unlock, err := Lock(context.Background(), a, waiting)
	if err != nil {
		t.Fatal(err)
	}
	if waits != 0 {
		t.Errorf("uncontended lock reported %d waits", waits)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, b, waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock while held: err = %v, want context.DeadlineExceeded", err)
	}
	if waits != 1 {
		t.Errorf("waits reported = %d, want 1", waits)
	}
	unlock()
}
