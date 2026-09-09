package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCleanListsAndRemovesStaleTemps: clean lists the leftovers of an interrupted write,
// removes them only with --remove, and by default leaves anything modified within the hour
// (a write in flight) alone unless --all is given.
func TestCleanListsAndRemovesStaleTemps(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old := writeTemp(t, dir, ".waxlabel-670843215.tmp", 2*time.Hour)
	fresh := writeTemp(t, dir, ".waxlabel-1.tmp", time.Minute)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := writeTemp(t, sub, ".waxlabel-writecheck-2.tmp", 3*time.Hour)
	if err := os.WriteFile(filepath.Join(dir, "song.flac"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, code := runCLI(t, "clean", dir)
	if code != 0 || !strings.Contains(out, filepath.Base(old)) || strings.Contains(out, filepath.Base(fresh)) || strings.Contains(out, filepath.Base(nested)) {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "--remove") {
		t.Errorf("dry run should say how to remove:\n%s", out)
	}
	out, _, _ = runCLI(t, "clean", "--all", "--recursive", dir)
	for _, p := range []string{old, fresh, nested} {
		if !strings.Contains(out, filepath.Base(p)) {
			t.Errorf("--all --recursive should list %s:\n%s", p, out)
		}
	}
	out, _, code = runCLI(t, "--json", "clean", "--remove", "--recursive", dir)
	if code != 0 {
		t.Fatalf("remove: exit %d\n%s", code, out)
	}
	entries := decodeJSONList[jsonClean](t, out)
	if len(entries) != 2 || !entries[0].Removed || !entries[1].Removed {
		t.Errorf("json = %+v", entries)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("stale temp not removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh temp must survive without --all")
	}
	if _, err := os.Stat(filepath.Join(dir, "song.flac")); err != nil {
		t.Error("audio must be untouched")
	}
}

// TestCleanRejectsNonDirectoryBeforeRemoving: every operand is validated before anything is
// deleted, so a bad argument cannot leave earlier directories half-cleaned and unreported.
func TestCleanRejectsNonDirectoryBeforeRemoving(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stale := writeTemp(t, dir, ".waxlabel-1.tmp", 2*time.Hour)
	file := filepath.Join(dir, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errb, code := runCLI(t, "clean", "--remove", dir, file)
	if code != 2 || !strings.Contains(errb, "not a directory") {
		t.Errorf("exit %d stderr %q, want a usage error", code, errb)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Error("the usage error must be raised before any file is removed")
	}
}

// TestCleanReportsUnreadableSubtree: a directory the scan cannot open is an error, not an
// empty result, so clean never reports a library clean over a place nobody could look.
func TestCleanReportsUnreadableSubtree(t *testing.T) {
	requireUnwritableDir(t)
	dir := t.TempDir()
	nope := filepath.Join(dir, "nope")
	if err := os.Mkdir(nope, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, nope, ".waxlabel-2.tmp", 2*time.Hour)
	if err := os.Chmod(nope, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(nope, 0o755) })
	// A leftover elsewhere in the tree must still be found and removed: one unreadable
	// album cannot be allowed to abandon everything the scan already collected.
	stale := writeTemp(t, dir, ".waxlabel-4.tmp", 3*24*time.Hour)
	out, errb, code := runCLI(t, "clean", "--recursive", "--remove", dir)
	if code != 6 {
		t.Errorf("exit %d, want 6\n%s%s", code, out, errb)
	}
	if !strings.Contains(out, "removed 1 leftover") {
		t.Errorf("the readable part of the tree must still be cleaned:\n%s", out)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("an unreadable subtree stopped --remove from deleting elsewhere")
	}
	// The error names the directory that could not be opened, not the argument that is
	// perfectly readable.
	if !strings.Contains(errb, nope) {
		t.Errorf("the error should name the unreadable subdirectory: %q", errb)
	}
	// An age is not a playback position: three days must not read as an hour count.
	if !strings.Contains(out, "3 day(s) old") {
		t.Errorf("a multi-day age should read in days:\n%s", out)
	}
}

// TestCleanReportsEmptyTreeAsClean: with nothing found and nothing unreadable, the summary
// is the plain one; the hedged wording is reserved for a scan that could not see everything.
func TestCleanReportsEmptyTreeAsClean(t *testing.T) {
	t.Parallel()
	out, _, code := runCLI(t, "clean", "--recursive", t.TempDir())
	if code != 0 || !strings.Contains(out, "no leftover temp files\n") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// TestCleanFollowsSymlinkedRoot: a music directory reached through a symlink is walked, as
// the audio commands walk it, instead of yielding one node the walk refuses to descend.
func TestCleanFollowsSymlinkedRoot(t *testing.T) {
	t.Parallel()
	real := t.TempDir()
	sub := filepath.Join(real, "album")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, sub, ".waxlabel-3.tmp", 2*time.Hour)
	link := filepath.Join(t.TempDir(), "music")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	out, _, code := runCLI(t, "clean", "--recursive", link)
	if code != 0 || !strings.Contains(out, ".waxlabel-3.tmp") {
		t.Errorf("exit %d, want the leftover under the symlinked root:\n%s", code, out)
	}
	if !strings.Contains(out, link) {
		t.Errorf("the listing should name the path the user passed:\n%s", out)
	}
}
