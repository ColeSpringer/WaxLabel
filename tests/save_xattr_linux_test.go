package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

const testXattr = "user.waxlabel.test"

// setXattr sets name on path, skipping the test where the filesystem has no such attributes.
func setXattr(t *testing.T, path, name string, value []byte) {
	t.Helper()
	if err := syscall.Setxattr(path, name, value, 0); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) {
			t.Skipf("filesystem does not support %s: %v", name, err)
		}
		t.Fatal(err)
	}
}

// getXattr reads name from path; ok is false when it is not set.
func getXattr(t *testing.T, path, name string) ([]byte, bool) {
	t.Helper()
	buf := make([]byte, 4096)
	n, err := syscall.Getxattr(path, name, buf)
	if errors.Is(err, syscall.ENODATA) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n], true
}

// accessACL is a POSIX access ACL granting uid read access beside the owner's rw.
func accessACL(uid uint32) []byte {
	const undefined = 0xFFFFFFFF
	b := binary.LittleEndian.AppendUint32(nil, 2) // version
	for _, e := range []struct {
		tag, perm uint16
		id        uint32
	}{{0x01, 6, undefined}, {0x02, 4, uid}, {0x04, 4, undefined}, {0x10, 4, undefined}, {0x20, 4, undefined}} {
		b = binary.LittleEndian.AppendUint16(b, e.tag)
		b = binary.LittleEndian.AppendUint16(b, e.perm)
		b = binary.LittleEndian.AppendUint32(b, e.id)
	}
	return b
}

func saveTitleTo(t *testing.T, doc *wl.Document, dst wl.Destination) {
	t.Helper()
	plan, err := doc.Edit().Set(tag.Title, "meta").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
}

// TestSaveKeepsReplacedFileAttributes: a rewrite keeps the replaced file's user attributes and
// access ACL; a new file does not inherit the source's.
func TestSaveKeepsReplacedFileAttributes(t *testing.T) {
	value := []byte("kept")
	t.Run("SaveBack", func(t *testing.T) {
		path := copyToTemp(t, sampleFLAC)
		setXattr(t, path, testXattr, value)
		saveTitleTo(t, mustParseFile(t, path), wl.SaveBack())
		if got, ok := getXattr(t, path, testXattr); !ok || !bytes.Equal(got, value) {
			t.Errorf("%s after SaveBack = %q (set %v), want %q", testXattr, got, ok, value)
		}
	})
	t.Run("SaveAsFile onto an existing file", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "existing.flac")
		if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		setXattr(t, target, testXattr, value)
		saveTitleTo(t, mustParseFile(t, sampleFLAC), wl.SaveAsFile(target))
		if got, ok := getXattr(t, target, testXattr); !ok || !bytes.Equal(got, value) {
			t.Errorf("%s after SaveAsFile = %q (set %v), want %q", testXattr, got, ok, value)
		}
	})
	t.Run("new file", func(t *testing.T) {
		src := copyToTemp(t, sampleFLAC)
		setXattr(t, src, testXattr, value)
		out := filepath.Join(t.TempDir(), "new.flac")
		saveTitleTo(t, mustParseFile(t, src), wl.SaveAsFile(out))
		if got, ok := getXattr(t, out, testXattr); ok {
			t.Errorf("a new file carries %s = %q from the source", testXattr, got)
		}
	})
	t.Run("read-only file", func(t *testing.T) {
		path := copyToTemp(t, sampleFLAC)
		setXattr(t, path, testXattr, value)
		if err := os.Chmod(path, 0o444); err != nil {
			t.Fatal(err)
		}
		saveTitleTo(t, mustParseFile(t, path), wl.SaveBack())
		if got, ok := getXattr(t, path, testXattr); !ok || !bytes.Equal(got, value) {
			t.Errorf("%s after saving a read-only file = %q (set %v), want %q", testXattr, got, ok, value)
		}
	})
	t.Run("no ACL stays none", func(t *testing.T) {
		path := copyToTemp(t, sampleFLAC)
		// New files in this directory now inherit an ACL; the file being replaced has none.
		setXattr(t, filepath.Dir(path), "system.posix_acl_default", accessACL(65534))
		saveTitleTo(t, mustParseFile(t, path), wl.SaveBack())
		if got, ok := getXattr(t, path, "system.posix_acl_access"); ok {
			t.Errorf("saved file gained an inherited access ACL %x", got)
		}
	})
	t.Run("access ACL", func(t *testing.T) {
		path := copyToTemp(t, sampleFLAC)
		setXattr(t, path, "system.posix_acl_access", accessACL(65534))
		want, _ := getXattr(t, path, "system.posix_acl_access")
		saveTitleTo(t, mustParseFile(t, path), wl.SaveBack())
		if got, ok := getXattr(t, path, "system.posix_acl_access"); !ok || !bytes.Equal(got, want) {
			t.Errorf("access ACL after SaveBack = %x (set %v), want %x", got, ok, want)
		}
	})
}

// TestSaveKeepsOwner: run as root, a rewrite keeps the replaced file's owner and group.
func TestSaveKeepsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing a file's owner needs root")
	}
	path := copyToTemp(t, sampleFLAC)
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	saveTitleTo(t, mustParseFile(t, path), wl.SaveBack())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st := info.Sys().(*syscall.Stat_t); st.Uid != 65534 || st.Gid != 65534 {
		t.Errorf("owner = %d:%d, want 65534:65534", st.Uid, st.Gid)
	}
}

// TestSaveKeepsGroup: a rewrite keeps the replaced file's group when the caller belongs to it,
// though a new file would take the caller's primary group.
func TestSaveKeepsGroup(t *testing.T) {
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	gid := -1
	for _, g := range groups {
		if g != os.Getegid() {
			gid = g
			break
		}
	}
	if gid < 0 {
		t.Skip("the caller belongs to no group besides its primary one")
	}
	path := copyToTemp(t, sampleFLAC)
	if err := os.Chown(path, -1, gid); err != nil {
		t.Fatal(err)
	}
	saveTitleTo(t, mustParseFile(t, path), wl.SaveBack())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Sys().(*syscall.Stat_t).Gid; int(got) != gid {
		t.Errorf("group = %d, want the replaced file's %d", got, gid)
	}
}
