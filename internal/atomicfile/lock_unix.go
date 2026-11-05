//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package atomicfile

import (
	"context"
	"os"
	"syscall"
	"time"
)

// flock is the lock call; tests replace it.
var flock = syscall.Flock

// lockNoticeAfter is how long Lock waits before it reports the wait.
var lockNoticeAfter = time.Second

// Lock takes an exclusive advisory lock on f, polling until ctx is done while another descriptor
// holds it; waiting, when set, runs once after lockNoticeAfter of that. A filesystem that cannot
// lock (NFS without lockd, some FUSE) leaves f unlocked.
func Lock(ctx context.Context, f *os.File, waiting func()) (unlock func(), err error) {
	noop := func() {}
	rc, err := f.SyscallConn()
	if err != nil {
		return noop, nil
	}
	delay := time.Millisecond
	var since time.Time
	for {
		var lerr error
		if err := rc.Control(func(fd uintptr) { lerr = flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB) }); err != nil {
			return noop, nil
		}
		switch lerr {
		case nil:
			return func() { rc.Control(func(fd uintptr) { flock(int(fd), syscall.LOCK_UN) }) }, nil
		case syscall.EINTR:
			continue
		case syscall.EWOULDBLOCK:
		default:
			return noop, nil
		}
		if since.IsZero() {
			since = time.Now()
		}
		if waiting != nil && time.Since(since) >= lockNoticeAfter {
			waiting()
			waiting = nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, 50*time.Millisecond)
	}
}
