//go:build !unix

package waxlabel

import "os"

// sysInodeDevice has no inode/device to report on platforms that expose no
// syscall.Stat_t (Windows, plan9, wasm). Identity.Matches treats a zero inode as
// "unavailable" and relies on size, mtime, and the structural fingerprint
// instead. See source_unix.go for the populated version.
func sysInodeDevice(_ os.FileInfo) (inode, device uint64) {
	return 0, 0
}
