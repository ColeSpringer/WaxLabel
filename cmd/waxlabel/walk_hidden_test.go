package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeEmptyFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestWalkSkipsHiddenDirs verifies that a recursive walk does not descend hidden directories
// (.git, .cache, ...) or pick up hidden files, while still walking normal subdirectories.
func TestWalkSkipsHiddenDirs(t *testing.T) {
	root := t.TempDir()
	writeEmptyFile(t, filepath.Join(root, "a.flac"))
	writeEmptyFile(t, filepath.Join(root, ".secret.flac")) // hidden file -> excluded
	if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEmptyFile(t, filepath.Join(root, ".hidden", "b.flac")) // in a hidden dir -> excluded
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEmptyFile(t, filepath.Join(root, "sub", "c.flac")) // normal subdir -> included

	files, _ := walkAudioFiles(root)
	var bases []string
	for _, f := range files {
		bases = append(bases, filepath.Base(f))
	}
	slices.Sort(bases)
	if !slices.Equal(bases, []string{"a.flac", "c.flac"}) {
		t.Errorf("walk = %v, want [a.flac c.flac] (hidden dir/file excluded)", bases)
	}
	for _, f := range files {
		if strings.Contains(f, ".hidden") || strings.Contains(filepath.Base(f), ".secret") {
			t.Errorf("hidden entry leaked into the walk: %s", f)
		}
	}
}

// TestWalkHonorsExplicitHiddenRoot verifies that a hidden directory named as the walk root
// is still walked; only hidden directories inside that root are pruned.
func TestWalkHonorsExplicitHiddenRoot(t *testing.T) {
	hiddenRoot := filepath.Join(t.TempDir(), ".config")
	if err := os.MkdirAll(hiddenRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEmptyFile(t, filepath.Join(hiddenRoot, "x.flac"))

	files, _ := walkAudioFiles(hiddenRoot)
	if len(files) != 1 || filepath.Base(files[0]) != "x.flac" {
		t.Errorf("walk of explicitly-named hidden root = %v, want [x.flac]", files)
	}
}
