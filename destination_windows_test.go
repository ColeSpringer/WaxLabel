//go:build windows

package waxlabel

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestRetryableRenameError pins which failures earn a retry. Anything but a transient
// handle must surface at once instead of after the full backoff.
func TestRetryableRenameError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"ERROR_ACCESS_DENIED", syscall.ERROR_ACCESS_DENIED, true},
		{"ERROR_SHARING_VIOLATION", errnoSharingViolation, true},
		{"ERROR_FILE_NOT_FOUND", syscall.ERROR_FILE_NOT_FOUND, false},
		// os.Rename wraps in a *os.LinkError.
		{"wrapped ERROR_ACCESS_DENIED", &os.LinkError{Err: syscall.ERROR_ACCESS_DENIED}, true},
		{"wrapped ERROR_FILE_NOT_FOUND", &os.LinkError{Err: syscall.ERROR_FILE_NOT_FOUND}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableRenameError(tc.err); got != tc.want {
				t.Errorf("retryableRenameError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRenameReplaceSurfacesRealFailures: a rename that can never succeed still returns
// its error rather than being retried into silence.
func TestRenameReplaceSurfacesRealFailures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := renameReplace(filepath.Join(dir, "missing.tmp"), filepath.Join(dir, "out")); err == nil {
		t.Error("renameReplace of a missing source = nil, want an error")
	}
}

// TestClearTargetReadOnlyRoundTrips: the attribute is dropped for the rename and put
// back by restore, and a writable or missing target is left alone.
func TestClearTargetReadOnlyRoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	ro := filepath.Join(dir, "ro.flac")
	if err := os.WriteFile(ro, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o666) })

	restore := clearTargetReadOnly(ro, mustStat(t, ro))
	if perm := mustPerm(t, ro); perm&0o200 == 0 {
		t.Errorf("target still read-only after clear: mode = %o", perm)
	}
	restore()
	if perm := mustPerm(t, ro); perm&0o200 != 0 {
		t.Errorf("restore did not put the attribute back: mode = %o", perm)
	}

	// A writable target is left alone, a missing one must not panic.
	rw := filepath.Join(dir, "rw.flac")
	if err := os.WriteFile(rw, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	clearTargetReadOnly(rw, mustStat(t, rw))()
	if perm := mustPerm(t, rw); perm&0o200 == 0 {
		t.Errorf("a writable target was made read-only: mode = %o", perm)
	}
	clearTargetReadOnly(filepath.Join(dir, "gone.flac"), nil)() // writeAtomic passes nil
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func mustPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	return mustStat(t, path).Mode().Perm()
}
