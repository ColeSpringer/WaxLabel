package main

import "sync"

// A second interrupt forces os.Exit, which skips deferred cleanups. Temp files a run creates
// (today the buffered-stdin file) register their removal here so the signal goroutine can drain
// them before that forced exit, so a hard quit does not leak a /tmp/waxlabel-stdin-* file.
//
// registerCleanup returns a deregister func that the normal cleanup path calls, so the registry
// only ever holds temps still in flight; each is dropped the moment its command cleans up. That
// keeps it from growing across the many in-process command runs the test suite drives, and lets
// one command drain its own temp without disturbing another concurrent command's entry, since
// every entry is keyed by id rather than pooled in one slice.
var (
	cleanupMu  sync.Mutex
	cleanupID  uint64
	cleanupFns = map[uint64]func(){}
)

// registerCleanup records fn and returns a func that deregisters it. A nil fn registers nothing
// and returns a no-op deregister. Safe for concurrent use.
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

// runCleanups runs and clears every still-registered cleanup. It is called by the signal
// goroutine just before os.Exit, where deferred calls do not fire; a cleanup a normal-path
// deregister already removed is simply absent. It is idempotent (a second call finds an empty
// registry) and iterates a snapshot so a concurrent register/deregister cannot race the loop.
func runCleanups() {
	cleanupMu.Lock()
	fns := cleanupFns
	cleanupFns = map[uint64]func(){}
	cleanupMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}
