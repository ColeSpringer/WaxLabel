//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package atomicfile

import (
	"context"
	"os"
)

// Lock is a no-op where the platform has no file lock; a save's path re-check is the guard there.
// It never waits, so it never reports waiting.
func Lock(context.Context, *os.File, func()) (unlock func(), err error) { return func() {}, nil }
