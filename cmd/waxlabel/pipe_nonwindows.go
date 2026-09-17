//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// isBrokenPipe reports a write to a closed output pipe (EPIPE). main catches
// SIGPIPE so the write returns the errno. waxlabel writes only stdout/stderr.
func isBrokenPipe(err error) bool { return errors.Is(err, syscall.EPIPE) }
