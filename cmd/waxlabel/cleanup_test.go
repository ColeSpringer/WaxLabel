package main

import "testing"

// TestCleanupRegistryRuns: drain runs registered cleanups once, clears the registry, ignores nil.
// Not parallel: package-level registry shared with buffered-stdin path.
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

// TestCleanupRegistryDeregister: deregistered cleanup does not run on later drain.
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
