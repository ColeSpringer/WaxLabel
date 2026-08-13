//go:build !windows

package waxlabel

import (
	"errors"
	"fmt"
	"os"
)

// fsyncDir flushes a directory entry so the rename is durable. Best-effort: a
// directory that will not open is skipped, as is one whose Sync reports the operation
// unsupported (plan9, some FUSE and network mounts). Neither may fail a write whose
// bytes are already in place. ENOSPC and EIO still surface. See docs/deferred-work.md
// for the mount that answers EINVAL instead.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fmt.Errorf("directory fsync: %w", err)
	}
	return nil
}

// renameReplace atomically replaces target with tmpName. The Windows build wraps this
// in a bounded retry for a transient third-party handle.
func renameReplace(tmpName, target string) error { return os.Rename(tmpName, target) }

// clearTargetReadOnly does nothing here: a POSIX rename needs a writable directory,
// not a writable target. Windows disagrees, which is why the hook exists.
func clearTargetReadOnly(string, os.FileInfo) func() { return func() {} }
