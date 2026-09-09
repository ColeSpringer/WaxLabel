package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requireUnreadableDir skips where chmod 000 does not deny reads (Windows, root).
func requireUnreadableDir(t *testing.T) {
	t.Helper()
	requireUnwritableDir(t)
}

// TestRecursiveWalkReportsUnreadableDirectory: a subtree the walk cannot read is an io
// error entry (exit 6) beside the files it did read, not a silent omission, in both output
// modes and for the bespoke set loop.
func TestRecursiveWalkReportsUnreadableDirectory(t *testing.T) {
	requireUnreadableDir(t)
	root := t.TempDir()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok.flac"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	nope := filepath.Join(root, "nope")
	if err := os.Mkdir(nope, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nope, "hidden.flac"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nope, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(nope, 0o755) })

	out, _, code := runCLI(t, "--json", "dump", "--recursive", root)
	if code != 6 {
		t.Fatalf("exit = %d, want 6", code)
	}
	docs := decodeJSONList[jsonDocument](t, out)
	var sawIO, sawOK bool
	for _, d := range docs {
		switch {
		case d.Error != nil && d.Error.Code == "io" && strings.HasSuffix(d.File, "nope"):
			sawIO = true
		case d.Error == nil:
			sawOK = true
		}
	}
	if !sawIO || !sawOK || len(docs) != 2 {
		t.Errorf("entries = %d, io=%v ok=%v:\n%s", len(docs), sawIO, sawOK, out)
	}

	_, errb, code := runCLI(t, "dump", "--recursive", root)
	if code != 6 || !strings.Contains(errb, "nope") || !strings.Contains(errb, "permission denied") {
		t.Errorf("human mode: exit %d stderr %q", code, errb)
	}
	// The line already names the path, so the reason must not repeat it.
	if strings.Count(errb, nope) != 1 {
		t.Errorf("the path should appear once, not once per wrapper: %q", errb)
	}
	if _, errb, code := runCLI(t, "set", "--recursive", root, "--set", "TITLE=T", "-q"); code != 6 || !strings.Contains(errb, "nope") {
		t.Errorf("set: exit %d stderr %q", code, errb)
	}

	// The unreadable directory is reported, but nobody asked to write it, so it must not
	// count toward -o's single-input rule and refuse a legitimate one-file run.
	outO, errO, codeO := runCLI(t, "set", "--recursive", root, "--set", "TITLE=T", "-o", filepath.Join(t.TempDir(), "out.flac"))
	if codeO != 6 {
		t.Errorf("set -o: exit %d, want 6 for the unreadable directory\n%s%s", codeO, outO, errO)
	}
	if strings.Contains(errO, "exactly one input") {
		t.Errorf("-o counted the unreadable directory as an input: %q", errO)
	}
}
