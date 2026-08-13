//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// isBrokenPipe reports whether err is a write to a closed output pipe. Off Windows that
// is exactly EPIPE, which the write returns because main catches SIGPIPE. waxlabel
// writes only to stdout and stderr, so an EPIPE is never a network peer.
func isBrokenPipe(err error) bool { return errors.Is(err, syscall.EPIPE) }
