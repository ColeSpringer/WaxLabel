//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package core

import "os"

// sysStat reports nothing where the stat carries no inode or change time; [Identity] then
// compares files with os.SameFile.
func sysStat(os.FileInfo) (ino, dev uint64, ctimeNs int64) { return 0, 0, 0 }
