//go:build !unix

package atomicfile

import "os"

// copyOwner is a no-op where files have no POSIX owner.
func copyOwner(string, os.FileInfo) {}
