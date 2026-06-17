//go:build unix

package waxlabel

import (
	"os"
	"syscall"
)

// sysInodeDevice returns a file's inode and device numbers from the Unix stat
// result, strengthening the save-back identity check (so a file replaced by a
// same-size, same-mtime copy is still detected). Platforms without a
// syscall.Stat_t (Windows, plan9, wasm) use the !unix stub, which returns zeros;
// Identity.Matches treats a zero inode as "unavailable" and falls back to
// size/mtime plus the structural fingerprint.
func sysInodeDevice(info os.FileInfo) (inode, device uint64) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino), uint64(st.Dev)
	}
	return 0, 0
}
