//go:build windows

package waxlabel

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// errnoSharingViolation is ERROR_SHARING_VIOLATION (not exported by syscall).
const errnoSharingViolation = syscall.Errno(32)

// Backoff: 5 attempts, 5+10+20+40 = 75ms total.
const (
	renameAttempts     = 5
	renameInitialDelay = 5 * time.Millisecond
)

// fsyncDir is a no-op on Windows (no directory fsync; NTFS journals renames).
func fsyncDir(string) error { return nil }

// renameReplace retries on transient third-party handle holds (Defender/Indexer).
func renameReplace(tmpName, target string) error {
	var err error
	delay := renameInitialDelay
	for attempt := range renameAttempts {
		if err = os.Rename(tmpName, target); err == nil {
			return nil
		}
		if !retryableRenameError(err) {
			return err
		}
		if attempt < renameAttempts-1 {
			time.Sleep(delay)
			delay *= 2
		}
	}
	return err
}

// retryableRenameError is true for transient handle conflicts only.
func retryableRenameError(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, errnoSharingViolation)
}

// clearTargetReadOnly clears FILE_ATTRIBUTE_READONLY so MoveFileEx can replace
// target; restore re-applies it. Best-effort.
func clearTargetReadOnly(target string, info os.FileInfo) func() {
	noop := func() {}
	if info == nil || info.Mode().Perm()&0o200 != 0 {
		return noop
	}
	if os.Chmod(target, 0o666) != nil {
		return noop
	}
	return func() { _ = os.Chmod(target, info.Mode()) }
}
