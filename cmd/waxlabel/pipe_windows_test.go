//go:build windows

package main

import (
	"fmt"
	"syscall"
	"testing"
)

// TestIsBrokenPipeMatchesWindowsErrnos: Windows broken-pipe errnos (EPIPE inert here).
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
			// Usually wrapped in *os.PathError from write.
			if !isBrokenPipe(fmt.Errorf("write stdout: %w", tc.errno)) {
				t.Errorf("isBrokenPipe(wrapped %s) = false, want true", tc.name)
			}
		})
	}
}

// TestErrnoNoDataValue: errnoNoData must be 232 (ERROR_NO_DATA, not exported by syscall).
func TestErrnoNoDataValue(t *testing.T) {
	t.Parallel()
	if errnoNoData != 232 {
		t.Errorf("errnoNoData = %d, want 232 (ERROR_NO_DATA)", uintptr(errnoNoData))
	}
}
