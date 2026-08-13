package main

import (
	"os"
	"strings"
	"testing"
)

// TestSetRenameFailureHidesTempName drives the real Windows failure this write path exists
// for: a handle open on the target blocks MoveFileEx, so the commit fails after the retry
// backoff. The user must see the file they named and the reason, not the temp file the rename
// was from. os.Open takes no FILE_SHARE_DELETE, which is why the library releases its own
// source handle before the rename.
func TestSetRenameFailureHidesTempName(t *testing.T) {
	file := copyFixture(t, sampleFLAC)
	held, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	_, errb, code := runCLI(t, "set", file, "--set", "TITLE=X")
	if code == 0 {
		t.Skipf("the rename succeeded despite an open handle; nothing to assert\n%s", errb)
	}
	if strings.Contains(errb, ".waxlabel-") {
		t.Errorf("the internal temp name leaked into stderr: %q", errb)
	}
	if !strings.Contains(errb, file) {
		t.Errorf("stderr should name the file that could not be replaced: %q", errb)
	}
	if code != 6 {
		t.Errorf("exit = %d, want 6 (local I/O)", code)
	}
}
