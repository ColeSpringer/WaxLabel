//go:build unix

package atomicfile

import (
	"os"
	"syscall"
)

// copyOwner gives to the owner and group info carries, or just the group when only that is
// permitted (a member of the file's group who is not its owner).
func copyOwner(to string, info os.FileInfo) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	if os.Lchown(to, int(st.Uid), int(st.Gid)) != nil {
		_ = os.Lchown(to, -1, int(st.Gid))
	}
}
