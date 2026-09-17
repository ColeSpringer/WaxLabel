package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetRejectsTruncatedMoov: truncated moov is exit 4 on dump/set; input stays byte-identical.
func TestSetRejectsTruncatedMoov(t *testing.T) {
	full, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sample.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	// 9144-byte cut truncates trailing moov.
	if len(full) <= 9144 {
		t.Fatalf("fixture is %d bytes; the 9144-byte truncation needs a larger moov-trailing file", len(full))
	}
	trunc := full[:9144]
	path := filepath.Join(t.TempDir(), "trunc.m4a")
	if err := os.WriteFile(path, trunc, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := runCLI(t, "dump", path); code != 4 || !strings.Contains(stderr, "moov atom has") {
		t.Errorf("dump truncated moov: code=%d stderr=%q; want exit 4 naming the unusable moov bytes", code, stderr)
	}

	_, stderr, code := runCLI(t, "set", path, "--set", "TITLE=X")
	if code != 4 {
		t.Errorf("set truncated moov: code=%d stderr=%q; want exit 4 (no write)", code, stderr)
	}
	// Rejected set must not modify input.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, trunc) {
		t.Errorf("set rejected the file but modified it: %d bytes -> %d bytes", len(trunc), len(after))
	}
}
