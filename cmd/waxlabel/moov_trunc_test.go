package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetRejectsTruncatedMoov is the CLI end-to-end check: a truncated MP4 whose trailing
// moov was clamped to EOF used to `dump` at exit 0 (reporting "tags: (none)") and let `set` write a
// ~2x-size, self-unreadable file at exit 0. Both paths must now fail loudly (exit 4) and leave the
// input byte-identical, so the silent corruption can no longer be reported as success.
func TestSetRejectsTruncatedMoov(t *testing.T) {
	full, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sample.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	// 9144 bytes cuts into the trailing moov, leaving an unusable trailing gap.
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
	// The rejected write must commit nothing: the input stays byte-identical (not the old 2x-size,
	// self-unreadable output).
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, trunc) {
		t.Errorf("set rejected the file but modified it: %d bytes -> %d bytes", len(trunc), len(after))
	}
}
