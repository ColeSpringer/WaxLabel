//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestSetStdinOutputHonorsUmask: set - -o writes a new file, so its mode follows the umask like any
// other new file. Not parallel: the umask is process-wide.
func TestSetStdinOutputHonorsUmask(t *testing.T) {
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.flac")
	run := func() (string, int) {
		defer syscall.Umask(syscall.Umask(0o077))
		_, stderr, code := runCLIStdin(t, string(data), "set", "-", "-o", out, "--set", "TITLE=x")
		return stderr, code
	}
	stderr, code := run()
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("output mode = %o, want 0600 under umask 077", perm)
	}
}
