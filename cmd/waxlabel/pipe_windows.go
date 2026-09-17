//go:build windows

package main

import (
	"errors"
	"syscall"
)

// errnoNoData is ERROR_NO_DATA from a write to a closing pipe. syscall does not
// export it (unlike ERROR_BROKEN_PIPE).
const errnoNoData = syscall.Errno(232)

// isBrokenPipe reports a write to a closed output pipe. syscall.EPIPE is synthetic
// on Windows and never returned; without the real errnos `dump | head` exits 6
// instead of 0. EPIPE stays matched so a synthesized one classifies the same.
// WSAECONNRESET is unreachable (no net import).
func isBrokenPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ERROR_BROKEN_PIPE) ||
		errors.Is(err, errnoNoData)
}
