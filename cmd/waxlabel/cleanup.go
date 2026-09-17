package main

import "sync"

// A second interrupt forces os.Exit and skips deferred cleanups. Temp files register
// removal here so the signal goroutine can drain them first. Entries drop as each
// command cleans up, so the registry holds only in-flight temps.
//
// Best-effort on Windows (delete needs the handle closed; drain cannot force that
// mid-write). POSIX unlinks an open file regardless.
var (
	cleanupMu  sync.Mutex
	cleanupID  uint64
	cleanupFns = map[uint64]func(){}
)

// registerCleanup records fn and returns a deregister func. Nil fn is a no-op.
// Safe for concurrent use.
func registerCleanup(fn func()) (deregister func()) {
	if fn == nil {
		return func() {}
	}
	cleanupMu.Lock()
	id := cleanupID
	cleanupID++
	cleanupFns[id] = fn
	cleanupMu.Unlock()
	return func() {
		cleanupMu.Lock()
		delete(cleanupFns, id)
		cleanupMu.Unlock()
	}
}

// runCleanups runs and clears every still-registered cleanup. Called by the signal
// goroutine just before os.Exit. Idempotent; iterates a snapshot so concurrent
// register cannot race the loop.
func runCleanups() {
	cleanupMu.Lock()
	fns := cleanupFns
	cleanupFns = map[uint64]func(){}
	cleanupMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}
