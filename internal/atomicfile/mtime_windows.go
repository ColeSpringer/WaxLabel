//go:build windows

package atomicfile

import (
	"math"
	"os"
	"syscall"
	"time"
)

// SetModTime sets name's modification time and leaves its access time, for any date FILETIME
// holds. A failed stamp fails the save, so an indexer or virus scanner holding the new file is
// waited out as the rename does.
func SetModTime(name string, mtime time.Time) error {
	ft, ok := filetime(mtime)
	if !ok {
		return &os.PathError{Op: "chtimes", Path: name, Err: syscall.EINVAL}
	}
	if err := retryTransient(func() error { return setFileTime(name, &ft) }); err != nil {
		return &os.PathError{Op: "chtimes", Path: name, Err: err}
	}
	return nil
}

// setFileTime is the stamp itself; tests replace it.
var setFileTime = func(name string, ft *syscall.Filetime) error {
	p, err := syscall.UTF16PtrFromString(extendedPath(name))
	if err != nil {
		return err
	}
	h, err := syscall.CreateFile(p, syscall.FILE_WRITE_ATTRIBUTES, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	return syscall.SetFileTime(h, nil, nil, ft)
}

// filetime is t in 100 ns intervals since 1601-01-01 UTC. False before 1601 and at 0, which
// SetFileTime reads as "leave unchanged".
func filetime(t time.Time) (syscall.Filetime, bool) {
	const secondsTo1970 = 11644473600
	sec := t.Unix() + secondsTo1970
	if sec < 0 || sec >= math.MaxInt64/10_000_000 {
		return syscall.Filetime{}, false
	}
	n := sec*10_000_000 + int64(t.Nanosecond())/100
	if n == 0 {
		return syscall.Filetime{}, false
	}
	return syscall.Filetime{LowDateTime: uint32(n), HighDateTime: uint32(n >> 32)}, true
}
