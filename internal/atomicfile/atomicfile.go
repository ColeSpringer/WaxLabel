// Package atomicfile writes a file by renaming a synced temp over the target, so readers see
// the old file or the new one and never a partial write.
package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// TempPrefix and TempSuffix name the temps Write creates beside the target.
const (
	TempPrefix = ".waxlabel-"
	TempSuffix = ".tmp"
)

// OpenFilesBlockRename is true where a file cannot be renamed over, or renamed, while a handle
// to it is open (Windows). Callers release such handles before the commit.
const OpenFilesBlockRename = runtime.GOOS == "windows"

// Options tunes [Write].
type Options struct {
	// Verify reads the synced temp before its metadata is set.
	Verify func(*os.File) error
	// PreCommit is the last check before the rename.
	PreCommit func() error
	// ModTime, when set, stamps the written file's modification time.
	ModTime *time.Time
	// NoReplace fails with fs.ErrExist instead of replacing a file at the target, including one
	// that appears while the write runs.
	NoReplace bool
	// KeepOpen keeps the temp open across the rename where [OpenFilesBlockRename] allows, so the
	// returned info is the written file's own stat after the rename.
	KeepOpen bool
}

// Write writes path through a temp in the target's directory: write, fsync, Verify, the replaced
// file's metadata ([CopyMeta]), ModTime, PreCommit, rename, directory fsync. committed is true once
// the rename succeeded; a later step's failure is returned with it. info describes the written
// file.
func Write(path string, write func(*os.File) error, o Options) (committed bool, info os.FileInfo, err error) {
	target := ResolveTarget(path)
	dir := filepath.Dir(target)
	existing, statErr := os.Stat(target)
	if statErr != nil {
		existing = nil
	} else if o.NoReplace {
		return false, nil, &renameError{target: target, err: &os.LinkError{Op: "replace", New: target, Err: fs.ErrExist}}
	}
	// A new file takes the default mode less the umask; a replacement starts private and takes
	// the replaced file's mode before the rename.
	perm := os.FileMode(0o666)
	if existing != nil {
		perm = 0o600
	}
	tmp, err := createTemp(dir, perm)
	if err != nil {
		return false, nil, &tempCreateError{dir: dir, err: err}
	}
	tmpName := tmp.Name()
	open := true
	closeTmp := func() error {
		if !open {
			return nil
		}
		open = false
		return tmp.Close()
	}
	defer func() {
		if !committed {
			closeTmp()
			os.Remove(tmpName)
		}
	}()

	if err := write(tmp); err != nil {
		return false, nil, err
	}
	if err := tmp.Sync(); err != nil {
		return false, nil, err
	}
	if o.Verify != nil {
		if err := o.Verify(tmp); err != nil {
			return false, nil, err
		}
	}
	keep := o.KeepOpen && !OpenFilesBlockRename
	if !keep {
		if err := closeTmp(); err != nil {
			return false, nil, err
		}
	}

	if existing != nil {
		CopyMeta(target, tmpName, existing)
		keepWritable(tmpName, existing.Mode())
	}
	if o.ModTime != nil {
		if err := setModTime(tmpName, *o.ModTime); err != nil {
			return false, nil, err
		}
	}

	leftover, err := commit(tmpName, target, existing, o.NoReplace, o.PreCommit)
	if err != nil {
		return false, nil, err
	}
	committed = true

	var statErrAfter error
	if keep {
		info, statErrAfter = tmp.Stat()
		statErrAfter = errors.Join(statErrAfter, closeTmp())
	} else {
		info, statErrAfter = os.Stat(target)
	}
	return true, info, errors.Join(leftover, statErrAfter, fsyncDir(dir))
}

// createTemp creates a new temp in dir with perm, before the umask.
func createTemp(dir string, perm os.FileMode) (*os.File, error) {
	for range 10000 {
		name := filepath.Join(dir, TempPrefix+strconv.FormatUint(rand.Uint64(), 36)+TempSuffix)
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
	return nil, &fs.PathError{Op: "createtemp", Path: filepath.Join(dir, TempPrefix+"*"+TempSuffix), Err: fs.ErrExist}
}

// CopyMeta gives to the owner and mode of from, which info describes, and on Linux its user.*
// attributes and access ACL, as an in-place rewrite would keep them. Best-effort, like the chmod
// a filesystem without modes ignores. The mode comes last: a chown can clear set-id bits, and a
// read-only mode would refuse the user.* writes.
func CopyMeta(from, to string, info os.FileInfo) {
	copyOwner(to, info)
	copyXattrs(from, to)
	_ = os.Chmod(to, info.Mode())
}

// setModTime is the stamp call; tests replace it.
var setModTime = SetModTime

// IsTargetExists reports whether err is a [Options.NoReplace] refusal: a file was at the target.
func IsTargetExists(err error) bool {
	re, ok := errors.AsType[*renameError](err)
	return ok && errors.Is(re.err, fs.ErrExist)
}

// ResolveTarget is the path a write renames over: path with symlinks resolved.
func ResolveTarget(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// tempCreateError names the destination directory, not the internal temp pattern.
type tempCreateError struct {
	dir string
	err error // usually *os.PathError
}

// NewTempCreateError is the error Write returns when it cannot create its temp in dir.
func NewTempCreateError(dir string, err error) error { return &tempCreateError{dir: dir, err: err} }

func (e *tempCreateError) Error() string {
	reason := e.err.Error()
	if pe, ok := e.err.(*os.PathError); ok {
		reason = pe.Err.Error() // the bare cause, without the random temp name
	}
	return fmt.Sprintf("create temp file in %s: %s", e.dir, reason)
}

func (e *tempCreateError) Unwrap() error { return e.err }

// renameError names the target; unwraps LinkError for I/O classification.
type renameError struct {
	target string
	err    error // usually *os.LinkError
}

func (e *renameError) Error() string {
	reason := e.err.Error()
	if le, ok := e.err.(*os.LinkError); ok {
		reason = le.Err.Error() // the bare cause, without the random temp name
	}
	return fmt.Sprintf("replace %s: %s", e.target, reason)
}

func (e *renameError) Unwrap() error { return e.err }
