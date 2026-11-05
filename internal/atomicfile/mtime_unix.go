//go:build unix

package atomicfile

import (
	"os"
	"runtime"
	"syscall"
	"time"
)

// SetModTime sets name's modification time and leaves its access time, for any date the
// platform's timespec holds (os.Chtimes stops at the int64-nanosecond range, 1678 to 2262).
func SetModTime(name string, mtime time.Time) error {
	var ts [2]syscall.Timespec
	setTimespec(&ts[0].Sec, &ts[0].Nsec, 0, utimeOmit)
	if !setTimespec(&ts[1].Sec, &ts[1].Nsec, mtime.Unix(), int64(mtime.Nanosecond())) {
		return &os.PathError{Op: "chtimes", Path: name, Err: syscall.ERANGE}
	}
	if err := syscall.UtimesNano(name, ts[:]); err != nil {
		return &os.PathError{Op: "chtimes", Path: name, Err: err}
	}
	return nil
}

// setTimespec stores sec and nsec in timespec fields of either width; false when sec does not fit.
func setTimespec[S, N ~int32 | ~int64](sec *S, nsec *N, s, n int64) bool {
	*sec, *nsec = S(s), N(n)
	return int64(*sec) == s
}

// utimeOmit is UTIME_OMIT, the nanosecond value that leaves a timestamp unchanged.
var utimeOmit = func() int64 {
	switch runtime.GOOS {
	case "linux", "android", "netbsd":
		return 1<<30 - 2
	case "openbsd":
		return -1
	case "aix":
		return -3
	default: // darwin, ios, freebsd, dragonfly, solaris, illumos
		return -2
	}
}()
