//go:build unix

package waxlabel

import (
	"os"
	"syscall"
)

// sysInodeDevice returns inode and device for save-back identity. Platforms
// without syscall.Stat_t use the !unix stub (zeros); Identity.Matches then
// falls back to size/mtime plus fingerprint.
func sysInodeDevice(info os.FileInfo) (inode, device uint64) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino), uint64(st.Dev)
	}
	return 0, 0
}
