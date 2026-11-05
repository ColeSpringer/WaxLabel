//go:build aix || dragonfly || linux || openbsd || solaris

package core

import (
	"os"
	"syscall"
	"time"
)

// sysStat returns the inode, device and change time info's stat carries.
func sysStat(info os.FileInfo) (ino, dev uint64, ctimeNs int64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0
	}
	return uint64(st.Ino), uint64(st.Dev), UnixNanoOrZero(time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)))
}
