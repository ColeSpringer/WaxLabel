//go:build windows

package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
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

// retryTransient runs op until it succeeds, fails for good, or runs out of attempts, backing off
// while a third-party handle (Defender, the indexer) holds the file.
func retryTransient(op func() error) error {
	var err error
	delay := renameInitialDelay
	for attempt := range renameAttempts {
		if err = op(); err == nil || !retryableRenameError(err) {
			return err
		}
		if attempt < renameAttempts-1 {
			time.Sleep(delay)
			delay *= 2
		}
	}
	return err
}

// Replace moves tmp to target. An existing target is swapped out with ReplaceFileW, which keeps
// its DACL, attributes, alternate streams and creation time; a new one is a MoveFileEx rename.
// noReplace moves with MoveFileW, which refuses an existing target with fs.ErrExist. Transient
// third-party handle holds are retried.
func Replace(tmp, target string, noReplace bool) error {
	_, _, err := replaceRetrying(tmp, target, nil, noReplace, nil)
	return err
}

// commit runs check before every attempt to move tmp to target: an attempt that waits out another
// handle may be waiting on a competing save, which can commit in the meantime. leftover reports a
// committed swap that could not remove the replaced file's old copy.
func commit(tmp, target string, existing os.FileInfo, noReplace bool, check func() error) (leftover, err error) {
	checkFailed, leftover, err := replaceRetrying(tmp, target, existing, noReplace, check)
	if err != nil && !checkFailed {
		return nil, &renameError{target: target, err: err}
	}
	return leftover, err
}

func replaceRetrying(tmp, target string, existing os.FileInfo, noReplace bool, check func() error) (checkFailed bool, leftover, err error) {
	err = retryTransient(func() error {
		checkFailed = false
		if check != nil {
			if err := check(); err != nil {
				checkFailed = true
				return err
			}
		}
		// Cleared only once the check passed, and put back on whatever then holds the path: our
		// file, or the untouched target when the move failed.
		restore := clearTargetReadOnly(target, existing)
		defer restore()
		var err error
		leftover, err = replaceOnce(tmp, target, noReplace)
		return err
	})
	return checkFailed, leftover, err
}

func replaceOnce(tmp, target string, noReplace bool) (leftover, err error) {
	if noReplace {
		return nil, moveNoReplace(tmp, target)
	}
	if _, err := os.Lstat(target); err == nil {
		leftover, err := replaceFile(tmp, target)
		if err != nil && !retryableRenameError(err) && !errors.As(err, new(*strandedError)) {
			// Some network redirectors (WebDAV, some NAS firmware) refuse ReplaceFileW outright; a
			// plain rename still saves, without the ACL and attribute merge.
			replaceCall("MoveFileEx")
			return nil, os.Rename(tmp, target)
		}
		return leftover, err
	}
	replaceCall("MoveFileEx")
	return nil, os.Rename(tmp, target)
}

// keepWritable leaves the replacement writable: ReplaceFileW refuses a read-only one, and the
// target's read-only attribute returns after the swap (clearTargetReadOnly's restore).
func keepWritable(tmp string, mode os.FileMode) {
	if mode&0o200 == 0 {
		_ = os.Chmod(tmp, mode|0o200)
	}
}

// moveNoReplace renames tmp to target unless target exists.
func moveNoReplace(tmp, target string) error {
	replaceCall("MoveFile")
	from, err := syscall.UTF16PtrFromString(extendedPath(tmp))
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(extendedPath(target))
	if err != nil {
		return err
	}
	if err := syscall.MoveFile(from, to); err != nil {
		return &os.LinkError{Op: "rename", Old: tmp, New: target, Err: err}
	}
	return nil
}

// ReplaceFileW failures (winerror.h). With a backup name, 1175 and 1176 leave both files where they
// were; 1177 has moved the replaced file to the backup name.
const (
	errUnableToRemoveReplaced    = syscall.Errno(1175)
	errUnableToMoveReplacement   = syscall.Errno(1176)
	errUnableToMoveReplacement2  = syscall.Errno(1177)
	replaceFileIgnoreMergeErrors = 0x2
	replaceFileIgnoreACLErrors   = 0x4
)

var procReplaceFileW = syscall.NewLazyDLL("kernel32.dll").NewProc("ReplaceFileW")

// replaceCall reports which call Replace makes; replaceFileW is the swap itself. Tests replace
// both.
var (
	replaceCall  = func(string) {}
	replaceFileW = callReplaceFile
)

// strandedError is a swap that moved the target aside and could not put it back: the path is
// empty and the original is only at backup. It wraps no errno, so nothing retries it.
type strandedError struct {
	backup            string
	cause, restoreErr error
}

func (e *strandedError) Error() string {
	return fmt.Sprintf("%v; restoring the original failed (%v), it is at %s", e.cause, e.restoreErr, e.backup)
}

// replaceFile swaps tmp in for target. target waits under a backup name until the swap
// completes, so a failed swap can put it back instead of leaving the path empty. The backup name
// is outside IsTempFileName, so clean never deletes what may be the only copy; a committed swap
// removes it, and reports it as leftover when it cannot.
func replaceFile(tmp, target string) (leftover, err error) {
	replaceCall("ReplaceFileW")
	backup := strings.TrimSuffix(tmp, TempSuffix) + ".replaced"
	err = replaceFileW(target, tmp, backup)
	switch {
	case err == nil:
		rerr := retryTransient(func() error {
			if err := os.Remove(backup); !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		})
		if rerr != nil {
			return fmt.Errorf("the replaced file's old copy is left at %s: %w", backup, rerr), nil
		}
		return nil, nil
	case errors.Is(err, errUnableToMoveReplacement2):
		if rerr := os.Rename(backup, target); rerr != nil {
			return nil, &os.LinkError{Op: "replace", Old: tmp, New: target, Err: &strandedError{backup, err, rerr}}
		}
	}
	return nil, &os.LinkError{Op: "replace", Old: tmp, New: target, Err: err}
}

func callReplaceFile(replaced, replacement, backup string) error {
	rp, err := syscall.UTF16PtrFromString(extendedPath(replaced))
	if err != nil {
		return err
	}
	np, err := syscall.UTF16PtrFromString(extendedPath(replacement))
	if err != nil {
		return err
	}
	bp, err := syscall.UTF16PtrFromString(extendedPath(backup))
	if err != nil {
		return err
	}
	ok, _, e := procReplaceFileW.Call(uintptr(unsafe.Pointer(rp)), uintptr(unsafe.Pointer(np)), uintptr(unsafe.Pointer(bp)),
		replaceFileIgnoreMergeErrors|replaceFileIgnoreACLErrors, 0, 0)
	if ok != 0 {
		return nil
	}
	if errno, isErrno := e.(syscall.Errno); isErrno && errno != 0 {
		return errno
	}
	return syscall.EINVAL
}

// extendedPath is name in the \\?\ form the Win32 calls need past MAX_PATH, as os uses
// internally; Windows builds before 10.0.15063 do not lift that limit for the process.
func extendedPath(name string) string {
	if strings.HasPrefix(name, `\\?\`) {
		return name
	}
	abs, err := syscall.FullPath(name)
	if err != nil || len(abs) < 248 {
		return name
	}
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + abs[2:]
	}
	return `\\?\` + abs
}

// retryableRenameError is true for transient handle conflicts only, including ReplaceFileW's
// "could not remove or move" failures, which the backup name leaves undone.
func retryableRenameError(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, errnoSharingViolation) ||
		errors.Is(err, errUnableToRemoveReplaced) || errors.Is(err, errUnableToMoveReplacement) ||
		errors.Is(err, errUnableToMoveReplacement2)
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
