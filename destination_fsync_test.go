package waxlabel

import (
	"path/filepath"
	"testing"
)

// TestFsyncDirNeverFailsACommittedWrite pins the property writeAtomic depends on: the
// post-rename directory sync must not turn a committed write into a reported failure.
// The shared POSIX shape used to return ERROR_ACCESS_DENIED on Windows, failing every
// successful save.
func TestFsyncDirNeverFailsACommittedWrite(t *testing.T) {
	t.Parallel()
	if err := fsyncDir(t.TempDir()); err != nil {
		t.Errorf("fsyncDir(<existing dir>) = %v, want nil", err)
	}
	missing := filepath.Join(t.TempDir(), "gone")
	if err := fsyncDir(missing); err != nil {
		t.Errorf("fsyncDir(<missing dir>) = %v, want nil", err)
	}
}
