package main

import (
	"os"
	"strings"
	"testing"
)

// TestSetRenameFailureHidesTempName: open handle blocks rename; error names target, not temp file.
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
