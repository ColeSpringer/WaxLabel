package bits

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"syscall"
	"testing"

	"github.com/colespringer/waxlabel/waxerr"
)

func TestDepthGuard(t *testing.T) {
	d := NewDepth(3)
	for i := range 3 {
		if err := d.Enter(); err != nil {
			t.Fatalf("level %d: unexpected err %v", i, err)
		}
	}
	if err := d.Enter(); !errors.Is(err, waxerr.ErrTooDeep) {
		t.Errorf("4th Enter err = %v, want ErrTooDeep", err)
	}
	d.Leave()
	if err := d.Enter(); err != nil {
		t.Errorf("after Leave, Enter err = %v, want nil", err)
	}
}

func TestReadSliceBounds(t *testing.T) {
	r := bytes.NewReader([]byte("0123456789"))

	got, err := ReadSlice(r, 2, 3, 1<<20)
	if err != nil || string(got) != "234" {
		t.Errorf("ReadSlice = %q, %v", got, err)
	}

	if _, err := ReadSlice(r, 0, 5, 4); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("over-limit err = %v, want ErrSizeTooLarge", err)
	}
	if _, err := ReadSlice(r, 8, 5, 1<<20); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("past-EOF err = %v, want ErrInvalidData", err)
	}
	// Non-positive limit rejected (would be unbounded allocation).
	for _, lim := range []int64{0, -1} {
		if _, err := ReadSlice(r, 0, 3, lim); !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("limit %d err = %v, want ErrInvalidData (a real ceiling is required)", lim, err)
		}
	}
}

func TestHasherTapMatchesDirectHash(t *testing.T) {
	src := bytes.NewReader([]byte("AAAABBBBCCCCDDDD"))
	cfg := []byte("config")

	want := sha256.New()
	want.Write(cfg)
	want.Write([]byte("BBBBCCCC"))

	h := NewHasher([][2]int64{{4, 12}})
	h.Mix(cfg)
	segs := []Segment{Copy(0, 16)}
	if _, err := Write(context.Background(), discardWriter{}, src, segs, h); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(h.Sum(), want.Sum(nil)) {
		t.Error("tapped hash does not match the direct hash of the claimed region")
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestWriteCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := bytes.NewReader(make([]byte, 1<<18))
	_, err := Write(ctx, discardWriter{}, src, []Segment{Copy(0, 1<<18)}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Write with cancelled ctx err = %v, want context.Canceled", err)
	}
}

func TestSHA256Concatenation(t *testing.T) {
	a := SHA256([]byte("foo"), []byte("bar"))
	b := SHA256([]byte("foobar"))
	if a != b {
		t.Error("SHA256 over chunks should equal SHA256 of the concatenation")
	}
}

// eioReader fails every read with a filesystem I/O error.
type eioReader struct{}

func (eioReader) ReadAt([]byte, int64) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: "src", Err: syscall.EIO}
}

// endsAt reads n bytes of zeros, then reports the end of input.
type endsAt struct{ n int64 }

func (r endsAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.n {
		return 0, io.EOF
	}
	k := min(int64(len(p)), r.n-off)
	clear(p[:k])
	if k < int64(len(p)) {
		return int(k), io.EOF
	}
	return int(k), nil
}

// TestWriteReadErrors: a failing read keeps its cause (an I/O error is not corrupt data); a source
// shorter than the plan is invalid data; a full read that also reports EOF is not an error.
func TestWriteReadErrors(t *testing.T) {
	_, err := Write(context.Background(), discardWriter{}, eioReader{}, []Segment{Copy(0, 8)}, nil)
	if _, ok := errors.AsType[*fs.PathError](err); !ok || errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("EIO read: err = %v, want the *fs.PathError and not ErrInvalidData", err)
	}
	_, err = Write(context.Background(), discardWriter{}, endsAt{4}, []Segment{Copy(0, 8)}, nil)
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("short source: err = %v, want ErrInvalidData", err)
	}
	if n, err := Write(context.Background(), discardWriter{}, eofAtEnd{8}, []Segment{Copy(0, 8)}, nil); err != nil || n != 8 {
		t.Errorf("full read with EOF: (%d, %v), want (8, nil)", n, err)
	}
}

// eofAtEnd returns io.EOF with a full read that ends the input, as io.ReaderAt allows.
type eofAtEnd struct{ n int64 }

func (r eofAtEnd) ReadAt(p []byte, off int64) (int, error) {
	k, err := endsAt(r).ReadAt(p, off)
	if err == nil && off+int64(k) == r.n {
		err = io.EOF
	}
	return k, err
}
