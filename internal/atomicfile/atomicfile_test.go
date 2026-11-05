package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeString(s string) func(*os.File) error {
	return func(f *os.File) error { _, err := f.WriteString(s); return err }
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > len(TempPrefix) && e.Name()[:len(TempPrefix)] == TempPrefix {
			t.Errorf("temp left behind: %s", e.Name())
		}
	}
}

// TestWriteFailsWhenModTimeFails: a requested modification time that cannot be set fails the
// write before the rename.
func TestWriteFailsWhenModTimeFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	stampErr := errors.New("cannot stamp")
	setModTime = func(string, time.Time) error { return stampErr }
	t.Cleanup(func() { setModTime = SetModTime })

	mt := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	committed, _, err := Write(path, writeString("new"), Options{ModTime: &mt})
	if committed || !errors.Is(err, stampErr) {
		t.Fatalf("Write = (committed %v, %v), want an uncommitted failure wrapping the stamp error", committed, err)
	}
	assertContent(t, path, "old")
	assertNoTemps(t, dir)
}

// TestWriteNoReplaceRefusesExistingTarget: a no-replace write onto an existing file fails with
// fs.ErrExist and changes nothing.
func TestWriteNoReplaceRefusesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	committed, _, err := Write(path, writeString("new"), Options{NoReplace: true})
	if committed || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Write = (committed %v, %v), want an uncommitted fs.ErrExist", committed, err)
	}
	// Write names the resolved target: on Windows, t.TempDir() may be spelled with 8.3 short names.
	target := ResolveTarget(path)
	if n := strings.Count(err.Error(), target); n != 1 {
		t.Errorf("error %q names %q %d times, want once", err, target, n)
	}
	assertContent(t, path, "old")
	assertNoTemps(t, dir)
}

// TestReplaceNoReplace: a no-replace move refuses an existing target and moves onto a free one.
func TestReplaceNoReplace(t *testing.T) {
	dir := t.TempDir()
	put := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	taken := put("taken", "old")
	tmp := put(TempPrefix+"x"+TempSuffix, "new")
	if err := Replace(tmp, taken, true); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Replace onto an existing file: err = %v, want fs.ErrExist", err)
	}
	assertContent(t, taken, "old")
	free := filepath.Join(dir, "free")
	if err := Replace(tmp, free, true); err != nil {
		t.Fatal(err)
	}
	assertContent(t, free, "new")
	assertNoTemps(t, dir)
}
