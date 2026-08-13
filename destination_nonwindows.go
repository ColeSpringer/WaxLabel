//go:build !windows

package waxlabel

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// fsyncDir flushes a directory entry so the rename is durable. Best-effort: a
// directory that will not open is skipped, as is one whose Sync reports the operation
// unsupported (plan9, some FUSE and network mounts; see dirSyncUnsupported). Neither may
// fail a write whose bytes are already in place. ENOSPC and EIO still surface.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !dirSyncUnsupported(err) {
		return fmt.Errorf("directory fsync: %w", err)
	}
	return nil
}

// dirSyncUnsupported reports whether a directory Sync failure means the platform has no such
// step rather than a lost write. errors.ErrUnsupported covers ENOSYS/ENOTSUP/EOPNOTSUPP.
// EINVAL joins them because fsync defines it as the operation being impossible on that
// descriptor, which is how some FUSE and network mounts answer and how macOS reports an
// F_FULLFSYNC it cannot do. Safe here and nowhere else: the call takes no argument but a
// descriptor this package just opened, so EINVAL cannot mean a bad one. A lost write reports
// ENOSPC, EIO, or EDQUOT, which all still surface.
//
// On macOS this means no directory sync at all, not a weaker one, since Go falls back from
// F_FULLFSYNC only on ENOTSUP. Acceptable for a step that never fails a committed write.
func dirSyncUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.EINVAL)
}

// renameReplace atomically replaces target with tmpName. The Windows build wraps this
// in a bounded retry for a transient third-party handle.
func renameReplace(tmpName, target string) error { return os.Rename(tmpName, target) }

// clearTargetReadOnly does nothing here: a POSIX rename needs a writable directory,
// not a writable target. Windows disagrees, which is why the hook exists.
func clearTargetReadOnly(string, os.FileInfo) func() { return func() {} }
