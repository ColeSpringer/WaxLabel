//go:build !unix && !windows

package atomicfile

import (
	"os"
	"time"
)

// SetModTime sets name's modification time and leaves its access time.
func SetModTime(name string, mtime time.Time) error { return os.Chtimes(name, time.Time{}, mtime) }
