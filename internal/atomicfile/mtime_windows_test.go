package atomicfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestFiletime: FILETIME counts 100 ns intervals from 1601-01-01 UTC and cannot hold earlier dates.
func TestFiletime(t *testing.T) {
	for _, tc := range []struct {
		t    time.Time
		want int64
	}{
		{time.Date(1601, 1, 1, 0, 0, 0, 100, time.UTC), 1},
		{time.Unix(0, 0), 116444736000000000},
		{time.Date(2300, 1, 1, 0, 0, 0, 1234, time.UTC), (10413792000+11644473600)*10_000_000 + 12},
	} {
		ft, ok := filetime(tc.t)
		if got := int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime); !ok || got != tc.want {
			t.Errorf("filetime(%v) = (%d, %v), want (%d, true)", tc.t, got, ok, tc.want)
		}
	}
	if _, ok := filetime(time.Date(1600, 12, 31, 0, 0, 0, 0, time.UTC)); ok {
		t.Error("filetime accepted a date before 1601")
	}
	if _, ok := filetime(time.Date(1601, 1, 1, 0, 0, 0, 0, time.UTC)); ok {
		t.Error("filetime accepted 0, which SetFileTime ignores")
	}
}

// TestSetModTimeWaitsOutHolds: a stamp that meets another handle (an indexer, a virus scanner)
// is retried rather than failing the save.
func TestSetModTimeWaitsOutHolds(t *testing.T) {
	real := setFileTime
	t.Cleanup(func() { setFileTime = real })
	calls := 0
	setFileTime = func(name string, ft *syscall.Filetime) error {
		if calls++; calls == 1 {
			return errnoSharingViolation
		}
		return real(name, ft)
	}
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := SetModTime(path, mt); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(mt) || calls != 2 {
		t.Errorf("mtime = %v after %d calls, want %v after 2", info.ModTime(), calls, mt)
	}
}
