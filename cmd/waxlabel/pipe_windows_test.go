//go:build windows

package main

import (
	"fmt"
	"syscall"
	"testing"
)

// TestIsBrokenPipeMatchesWindowsErrnos covers the errnos Windows actually returns for a
// write to a closed pipe. EPIPE is inert here, so without these two `dump | head` exits 6
// instead of 0.
func TestIsBrokenPipeMatchesWindowsErrnos(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		errno syscall.Errno
	}{
		{"ERROR_BROKEN_PIPE", syscall.ERROR_BROKEN_PIPE},
		{"ERROR_NO_DATA", errnoNoData}, // what a write to a closing pipe returns
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !isBrokenPipe(tc.errno) {
				t.Errorf("isBrokenPipe(%s) = false, want true", tc.name)
			}
			// Arrives wrapped in a *os.PathError from the write.
			if !isBrokenPipe(fmt.Errorf("write stdout: %w", tc.errno)) {
				t.Errorf("isBrokenPipe(wrapped %s) = false, want true", tc.name)
			}
		})
	}
}

// TestErrnoNoDataValue guards the local constant against a typo: syscall does not export
// ERROR_NO_DATA, so nothing else pins it to 232.
func TestErrnoNoDataValue(t *testing.T) {
	t.Parallel()
	if errnoNoData != 232 {
		t.Errorf("errnoNoData = %d, want 232 (ERROR_NO_DATA)", uintptr(errnoNoData))
	}
}
