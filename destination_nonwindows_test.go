//go:build unix

package waxlabel

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// TestDirSyncUnsupported pins which directory-sync failures fsyncDir skips as "this platform
// has no such step" and which are lost writes that must still surface. The last two cases pin
// that the deliberately narrow predicate cannot drift into "any error".
//
// Tagged unix rather than !windows because plan9 defines EINVAL and EIO but not ENOSYS,
// ENOTSUP, ENOSPC, or EDQUOT.
func TestDirSyncUnsupported(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		errn syscall.Errno
		want bool
	}{
		{"ENOSYS", syscall.ENOSYS, true},
		{"ENOTSUP", syscall.ENOTSUP, true},
		{"EINVAL", syscall.EINVAL, true},
		{"ENOSPC", syscall.ENOSPC, false},
		{"EIO", syscall.EIO, false},
		{"EDQUOT", syscall.EDQUOT, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := &os.PathError{Op: "sync", Err: c.errn}
			if got := dirSyncUnsupported(err); got != c.want {
				t.Errorf("dirSyncUnsupported(%v) = %v, want %v", err, got, c.want)
			}
		})
	}
	if dirSyncUnsupported(nil) {
		t.Error("dirSyncUnsupported(nil) = true, want false")
	}
	if dirSyncUnsupported(errors.New("directory sync failed")) {
		t.Error("a plain error must surface as a failure, not be skipped as unsupported")
	}
}
