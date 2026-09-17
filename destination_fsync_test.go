package waxlabel

import (
	"path/filepath"
	"testing"
)

// TestFsyncDirNeverFailsACommittedWrite: post-rename dir sync must not fail a
// committed write (old Windows POSIX shape returned ACCESS_DENIED).
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
