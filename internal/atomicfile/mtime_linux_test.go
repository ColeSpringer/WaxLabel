package atomicfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestSetModTimeLeavesAtime: only the modification time changes.
func TestSetModTimeLeavesAtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	atime := func() syscall.Timespec {
		var st syscall.Stat_t
		if err := syscall.Stat(path, &st); err != nil {
			t.Fatal(err)
		}
		return st.Atim
	}
	before := atime()
	mt := time.Date(2001, 2, 3, 4, 5, 6, 7, time.UTC)
	if err := SetModTime(path, mt); err != nil {
		t.Fatal(err)
	}
	if got := atime(); got != before {
		t.Errorf("atime = %v, want it unchanged at %v", got, before)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(mt) {
		t.Errorf("mtime = %v, want %v", info.ModTime(), mt)
	}
}
