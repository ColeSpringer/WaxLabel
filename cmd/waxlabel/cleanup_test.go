package main

import "testing"

// TestCleanupRegistryRuns covers the M7 forced-exit cleanup registry: registered cleanups run
// once on a drain and are cleared (so a second drain does not re-run them), and a nil cleanup is
// ignored. Not parallel - it drains the package-level registry, which the buffered-stdin path
// also feeds - so it runs in the serial phase when no parallel CLI test can register concurrently.
func TestCleanupRegistryRuns(t *testing.T) {
	var mine int
	registerCleanup(func() { mine++ })
	registerCleanup(func() { mine++ })
	registerCleanup(nil) // ignored, not counted
	runCleanups()        // runs and clears everything registered so far (only counts mine)
	if mine != 2 {
		t.Errorf("registered cleanups ran %d times, want 2", mine)
	}
	runCleanups() // idempotent: the registry was cleared, so mine does not grow
	if mine != 2 {
		t.Errorf("cleanups re-ran after the registry was drained; count = %d, want 2", mine)
	}
}

// TestCleanupRegistryDeregister covers the self-deregistration that keeps the registry holding
// only in-flight temps: a deregistered cleanup does not run on a later drain. This is what stops
// the normal-path cleanups from accumulating across the many in-process command runs the test
// suite drives, and stops one command's drain from touching another concurrent command's entry.
func TestCleanupRegistryDeregister(t *testing.T) {
	var ran int
	dereg := registerCleanup(func() { ran++ })
	registerCleanup(func() { ran++ }) // stays registered
	dereg()                           // remove only the first
	runCleanups()
	if ran != 1 {
		t.Errorf("after deregistering one of two cleanups, runCleanups ran %d, want 1", ran)
	}
}
