//go:build !windows

package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestNoReplaceWithoutHardLinks: where the filesystem refuses hard links (FAT, some network
// shares) a no-replace move falls back to checking for the target, then renaming.
func TestNoReplaceWithoutHardLinks(t *testing.T) {
	link = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EPERM} }
	t.Cleanup(func() { link = os.Link })
	dir := t.TempDir()
	put := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	taken := put("taken", "old")
	tmp := put(TempPrefix+"y"+TempSuffix, "new")
	if err := Replace(tmp, taken, true); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Replace onto an existing file: err = %v, want fs.ErrExist", err)
	}
	assertContent(t, taken, "old")
	free := filepath.Join(dir, "free")
	if err := Replace(tmp, free, true); err != nil {
		t.Fatal(err)
	}
	assertContent(t, free, "new")
}

// TestNoReplaceLinkRetriedOverNFS: an NFS client that retransmits a LINK the server already did
// gets EEXIST for a link that is ours; the commit succeeds rather than refusing its own output.
func TestNoReplaceLinkRetriedOverNFS(t *testing.T) {
	link = func(oldname, newname string) error {
		if err := os.Link(oldname, newname); err != nil {
			return err
		}
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EEXIST}
	}
	t.Cleanup(func() { link = os.Link })
	dir := t.TempDir()
	tmp := filepath.Join(dir, TempPrefix+"n"+TempSuffix)
	if err := os.WriteFile(tmp, []byte("ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "out")
	if err := Replace(tmp, target, true); err != nil {
		t.Fatalf("Replace = %v, want success for a link that landed", err)
	}
	assertContent(t, target, "ours")
	assertNoTemps(t, dir)
}
