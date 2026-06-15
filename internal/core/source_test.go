package core

import (
	"io"
	"testing"
)

func TestBytesSourceReadAt(t *testing.T) {
	src := BytesSource([]byte("abc"))

	// A zero-length read at EOF succeeds (matches os.File, not bytes.Reader).
	if n, err := src.ReadAt([]byte{}, 3); n != 0 || err != nil {
		t.Errorf("zero-length ReadAt at EOF = (%d, %v), want (0, nil)", n, err)
	}
	// A real read past the end still reports EOF.
	if _, err := src.ReadAt(make([]byte, 1), 3); err != io.EOF {
		t.Errorf("read at EOF = %v, want io.EOF", err)
	}
	// A partial read at the boundary returns the available bytes plus EOF.
	p := make([]byte, 4)
	if n, err := src.ReadAt(p, 1); n != 2 || err != io.EOF || string(p[:2]) != "bc" {
		t.Errorf("partial ReadAt = (%d, %v, %q)", n, err, p[:n])
	}
	// An offset beyond the end is EOF, not a panic.
	if _, err := src.ReadAt([]byte{}, 99); err != io.EOF {
		t.Errorf("offset past end = %v, want io.EOF", err)
	}
	if src.Size() != 3 {
		t.Errorf("Size = %d, want 3", src.Size())
	}
}

func TestIdentityMatches(t *testing.T) {
	base := Identity{Size: 100, ModTimeUnixNano: 5, INode: 7, Device: 1}

	if ok, _ := base.Matches(base); !ok {
		t.Error("identical identities should match")
	}
	if ok, why := base.Matches(Identity{Size: 101, ModTimeUnixNano: 5, INode: 7, Device: 1}); ok || why == "" {
		t.Errorf("size change should not match (why=%q)", why)
	}
	if ok, _ := base.Matches(Identity{Size: 100, ModTimeUnixNano: 6, INode: 7, Device: 1}); ok {
		t.Error("mtime change should not match")
	}
	if ok, _ := base.Matches(Identity{Size: 100, ModTimeUnixNano: 5, INode: 8, Device: 1}); ok {
		t.Error("inode change should not match")
	}
}
