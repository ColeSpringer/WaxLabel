//go:build !windows

package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
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

// link is the hard-link call; tests replace it.
var link = os.Link

// commit runs check, then moves tmp to target. Nothing is left behind here; leftover is for
// platforms that swap through a backup.
func commit(tmp, target string, _ os.FileInfo, noReplace bool, check func() error) (leftover, err error) {
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	if err := Replace(tmp, target, noReplace); err != nil {
		return nil, &renameError{target: target, err: err}
	}
	return nil, nil
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// keepWritable is a no-op where a read-only file can be renamed over.
func keepWritable(string, os.FileMode) {}

// Replace renames tmp over target. noReplace refuses an existing target with fs.ErrExist,
// atomically through a hard link where the filesystem has them.
func Replace(tmp, target string, noReplace bool) error {
	if !noReplace {
		return os.Rename(tmp, target)
	}
	err := link(tmp, target)
	if err == nil || errors.Is(err, fs.ErrExist) && sameFile(tmp, target) {
		// Committed. EEXIST naming our own file is an NFS client retrying a LINK that landed.
		_ = os.Remove(tmp) // a leftover second link is only a cleanable temp
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return err
	}
	// No hard links on this filesystem: check, then rename, which a racing writer can still beat.
	if _, serr := os.Lstat(target); serr == nil {
		return &os.LinkError{Op: "rename", Old: tmp, New: target, Err: fs.ErrExist}
	}
	return os.Rename(tmp, target)
}
