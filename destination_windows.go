//go:build windows

package waxlabel

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// errnoSharingViolation is ERROR_SHARING_VIOLATION. syscall exports
// ERROR_ACCESS_DENIED but not this one.
const errnoSharingViolation = syscall.Errno(32)

// The delay doubles each round: five attempts wait 5+10+20+40 = 75ms. Long enough for
// a scanner's hold on a large file, short enough that a genuine permission failure
// (also ERROR_ACCESS_DENIED) still reports promptly.
const (
	renameAttempts     = 5
	renameInitialDelay = 5 * time.Millisecond
)

// fsyncDir does nothing on Windows: there is no directory-fsync equivalent.
// FlushFileBuffers needs write access a directory handle from os.Open does not carry,
// so the POSIX shape failed every save. NTFS journals the rename anyway.
//
// Split by build tag rather than swallowing the error in the shared function, which
// would drop a genuine ENOSPC or EIO on Linux.
func fsyncDir(string) error { return nil }

// renameReplace replaces target with tmpName, backing off on the errnos a transient
// third-party handle produces. Defender and the Search Indexer hold a just-closed file
// open for a few milliseconds, which surfaces as a MoveFileEx failure. A handle this
// process holds never clears on its own, so it still surfaces after every attempt.
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

// retryableRenameError reports whether a failed rename is worth another attempt.
// Anything but a transient handle must surface at once, not after the full backoff.
func retryableRenameError(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, errnoSharingViolation)
}

// clearTargetReadOnly drops FILE_ATTRIBUTE_READONLY from target so the rename can
// replace it, returning a restore that re-applies it. MoveFileEx refuses a read-only
// target even with no handles open, where a POSIX rename does not care.
//
// info is target's already-observed state, nil when it does not exist; taking it from
// the caller keeps both decisions on one reading. Best-effort: a failed chmod is not
// fatal, since the rename then fails with the real reason.
func clearTargetReadOnly(target string, info os.FileInfo) func() {
	noop := func() {}
	// os.Stat maps the attribute to a mode with no write bits, and os.Chmod maps it back.
	if info == nil || info.Mode().Perm()&0o200 != 0 {
		return noop
	}
	if os.Chmod(target, 0o666) != nil {
		return noop
	}
	// After a successful rename target is the new file, so this also carries the mode over.
	return func() { _ = os.Chmod(target, info.Mode()) }
}
