//go:build !unix

package waxlabel

import "os"

// sysInodeDevice returns zeros where syscall.Stat_t is unavailable.
// Identity.Matches then uses size, mtime, and fingerprint. See source_unix.go.
func sysInodeDevice(_ os.FileInfo) (inode, device uint64) {
	return 0, 0
}
