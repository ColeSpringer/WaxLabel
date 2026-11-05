package core

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBytesSourceReadAt(t *testing.T) {
	src := BytesSource([]byte("abc"))

	// Zero-length ReadAt at EOF succeeds (like os.File).
	if n, err := src.ReadAt([]byte{}, 3); n != 0 || err != nil {
		t.Errorf("zero-length ReadAt at EOF = (%d, %v), want (0, nil)", n, err)
	}
	if _, err := src.ReadAt(make([]byte, 1), 3); err != io.EOF {
		t.Errorf("read at EOF = %v, want io.EOF", err)
	}
	p := make([]byte, 4)
	if n, err := src.ReadAt(p, 1); n != 2 || err != io.EOF || string(p[:2]) != "bc" {
		t.Errorf("partial ReadAt = (%d, %v, %q)", n, err, p[:n])
	}
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
	// A change time differs only when both sides know it; a derived write ignores it.
	ct := base
	ct.ChangeTimeUnixNano = 9
	moved := ct
	moved.ChangeTimeUnixNano = 10
	if ok, _ := ct.Matches(moved); ok {
		t.Error("change time change should not match")
	}
	if ok, _ := ct.MatchesContent(moved); !ok {
		t.Error("MatchesContent should ignore the change time")
	}
	if ok, _ := ct.Matches(base); !ok {
		t.Error("an unknown change time should not block a match")
	}
}

// zeros is an endless reader of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// sizedFile adapts an *os.File to ReaderAtSized.
type sizedFile struct {
	*os.File
	size int64
}

func (f sizedFile) Size() int64 { return f.size }

// TestFingerprintStreamsLargeRegions: a metadata region larger than any allocation limit still
// fingerprints, as the hash of its bytes.
func TestFingerprintStreamsLargeRegions(t *testing.T) {
	const head, audio = 300 << 20, 4096
	f, err := os.Create(filepath.Join(t.TempDir(), "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(head + audio); err != nil {
		t.Fatal(err)
	}
	fp, ok := Fingerprint(sizedFile{f, head + audio}, &Media{AudioStart: head, AudioEnd: head + audio})
	if !ok {
		t.Fatal("no fingerprint for a 300 MiB metadata region")
	}
	h := sha256.New()
	if _, err := io.CopyN(h, zeros{}, head); err != nil {
		t.Fatal(err)
	}
	if want := [32]byte(h.Sum(nil)); fp != want {
		t.Errorf("fingerprint = %x, want the SHA-256 of the region %x", fp, want)
	}
}

// TestReadFingerprintShortSource: a source that ends inside a region gives no fingerprint and no
// error; that is a changed file, not a failed read.
func TestReadFingerprintShortSource(t *testing.T) {
	_, ok, err := ReadFingerprint(BytesSource(make([]byte, 10)), &Media{AudioStart: 20, AudioEnd: 30})
	if ok || err != nil {
		t.Errorf("ReadFingerprint = (ok %v, %v), want (false, nil)", ok, err)
	}
}
