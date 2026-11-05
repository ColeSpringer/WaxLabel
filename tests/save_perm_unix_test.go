//go:build unix

package waxlabel_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// saveUnderUmask runs saveTitle with the process umask set to mask, restoring it however the
// save ends.
func saveUnderUmask(t *testing.T, mask int, dst wl.Destination) {
	t.Helper()
	defer syscall.Umask(syscall.Umask(mask))
	saveTitle(t, dst)
}

// TestSaveAsFileHonorsUmask: a new file takes the default mode less the umask, like any other
// tool's; a replaced file keeps its own mode. Not parallel: the umask is process-wide.
func TestSaveAsFileHonorsUmask(t *testing.T) {
	for _, tc := range []struct {
		umask int
		want  os.FileMode
	}{{0o077, 0o600}, {0o022, 0o644}} {
		dir := t.TempDir()
		out := filepath.Join(dir, "out.flac")
		saveUnderUmask(t, tc.umask, wl.SaveAsFile(out))
		if got := filePerm(t, out); got != tc.want {
			t.Errorf("umask %03o: new file mode = %o, want %o", tc.umask, got, tc.want)
		}
	}

	out := filepath.Join(t.TempDir(), "existing.flac")
	if err := os.WriteFile(out, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o640); err != nil {
		t.Fatal(err)
	}
	saveUnderUmask(t, 0o077, wl.SaveAsFile(out))
	if got := filePerm(t, out); got != 0o640 {
		t.Errorf("replaced file mode = %o, want its own 0640", got)
	}
}
