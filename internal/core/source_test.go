package core

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/waxerr"
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
	fp, ok, err := ReadFingerprint(sizedFile{f, head + audio}, &Media{AudioStart: head, AudioEnd: head + audio})
	if !ok || err != nil {
		t.Fatalf("no fingerprint for a 300 MiB metadata region: %v", err)
	}
	h := sha256.New()
	if _, err := io.CopyN(h, zeros{}, head); err != nil {
		t.Fatal(err)
	}
	if want := [32]byte(h.Sum(nil)); fp != want {
		t.Errorf("fingerprint = %x, want the SHA-256 of the region %x", fp, want)
	}
}

// TestReadFingerprintShortSource: a source that serves fewer bytes than its size inside a region,
// whether its read says io.EOF or io.ErrUnexpectedEOF, is invalid data, and a device error is the
// error; SetIdentity returns either with no fingerprint, so the parse fails closed. A source with
// nothing to hash has no fingerprint and no error.
func TestReadFingerprintShortSource(t *testing.T) {
	m := &Media{AudioStart: 20, AudioEnd: 30}
	short := sizedAs{BytesSource(make([]byte, 10)), 40}
	for name, src := range map[string]ReaderAtSized{
		"io.EOF":              short,
		"io.ErrUnexpectedEOF": unexpectedEOF{short},
	} {
		if _, ok, err := ReadFingerprint(src, m); ok || !errors.Is(err, waxerr.ErrInvalidData) || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%s: ReadFingerprint = (ok %v, %v), want invalid data from the short read", name, ok, err)
		}
		if err := SetIdentity(src, m); !errors.Is(err, waxerr.ErrInvalidData) || m.Identity.HasFinger {
			t.Errorf("%s: SetIdentity = %v with a fingerprint %v, want invalid data and none", name, err, m.Identity.HasFinger)
		}
	}
	if _, ok, err := ReadFingerprint(BytesSource(nil), m); ok || err != nil {
		t.Errorf("an empty source: ReadFingerprint = (ok %v, %v), want (false, nil)", ok, err)
	}
	// Bytes the media places past the end of the source are not the source's to serve.
	if _, ok, err := ReadFingerprint(BytesSource(make([]byte, 10)), m); !ok || err != nil {
		t.Errorf("an audio start past the end: ReadFingerprint = (ok %v, %v), want the bytes there fingerprinted", ok, err)
	}
	faulty := &testfile.Faulty{Src: BytesSource(make([]byte, 40)), Fails: testfile.Covering(5)}
	if _, ok, err := ReadFingerprint(faulty, m); ok || !errors.Is(err, syscall.EIO) {
		t.Errorf("a device error: ReadFingerprint = (ok %v, %v), want the read's own failure", ok, err)
	}
	if err := SetIdentity(faulty, m); !errors.Is(err, syscall.EIO) || m.Identity.HasFinger {
		t.Errorf("a device error: SetIdentity = %v with a fingerprint %v, want the read's own failure and none", err, m.Identity.HasFinger)
	}
}

// sizedAs is a source that declares size bytes, more than it serves.
type sizedAs struct {
	ReaderAtSized
	size int64
}

func (s sizedAs) Size() int64 { return s.size }

// unexpectedEOF reports a read that ends early as io.ErrUnexpectedEOF, as some sources do.
type unexpectedEOF struct{ ReaderAtSized }

func (s unexpectedEOF) ReadAt(p []byte, off int64) (int, error) {
	n, err := s.ReaderAtSized.ReadAt(p, off)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
