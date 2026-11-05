//go:build windows

package waxlabel_test

import (
	"path/filepath"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// Windows has no POSIX mode: os.Stat reports 0666 for any writable file and 0444 for one carrying
// the read-only attribute.
func TestSaveAsFileNewFileIsWritable(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.flac")
	saveTitle(t, wl.SaveAsFile(out))
	if got := filePerm(t, out); got != 0o666 {
		t.Errorf("new file mode = %o, want 0666", got)
	}
}
