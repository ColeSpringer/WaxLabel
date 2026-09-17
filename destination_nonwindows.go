//go:build !windows

package waxlabel

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// fsyncDir flushes the directory for rename durability. Best-effort; unsupported
// Sync is skipped (see dirSyncUnsupported). ENOSPC/EIO still surface.
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

// dirSyncUnsupported reports Sync failures that mean "no such step" (ErrUnsupported,
// EINVAL on this just-opened dir FD). Lost writes (ENOSPC/EIO/EDQUOT) still surface.
func dirSyncUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.EINVAL)
}

// renameReplace atomically replaces target with tmpName.
func renameReplace(tmpName, target string) error { return os.Rename(tmpName, target) }

// clearTargetReadOnly is a no-op on POSIX (rename needs a writable directory only).
func clearTargetReadOnly(string, os.FileInfo) func() { return func() {} }
