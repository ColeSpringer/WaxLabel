package bits

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"math"
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

func (r eofAtEnd) Size() int64 { return r.n }

func (r eofAtEnd) ReadAt(p []byte, off int64) (int, error) {
	k, err := endsAt(r).ReadAt(p, off)
	if err == nil && off+int64(k) == r.n {
		err = io.EOF
	}
	return k, err
}

// shortNoError breaks the io.ReaderAt contract: half of every read, and no error.
type shortNoError struct{}

func (shortNoError) ReadAt(p []byte, _ int64) (int, error) { return len(p) / 2, nil }

// TestFullReadWithEOF: ReadFullAt, ReadSlice and a Cursor accept a read that fills the buffer and
// reports io.EOF with it; a short read fails, even one that reports no error.
func TestFullReadWithEOF(t *testing.T) {
	p := make([]byte, 4)
	if err := ReadFullAt(eofAtEnd{8}, p, 4); err != nil {
		t.Errorf("ReadFullAt of the last 4 bytes: %v, want nil", err)
	}
	if err := ReadFullAt(endsAt{6}, p, 4); !errors.Is(err, io.EOF) {
		t.Errorf("ReadFullAt past the end: %v, want io.EOF", err)
	}
	if err := ReadFullAt(shortNoError{}, p, 0); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("ReadFullAt short without an error: %v, want io.ErrUnexpectedEOF", err)
	}
	if b, err := ReadSlice(eofAtEnd{8}, 4, 4, 1<<20); err != nil || len(b) != 4 {
		t.Errorf("ReadSlice of the last 4 bytes = (%d bytes, %v), want 4 bytes", len(b), err)
	}
	if _, err := ReadSlice(shortNoError{}, 0, 4, 1<<20); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("ReadSlice short without an error: %v, want ErrInvalidData", err)
	}
	c := NewCursorAt(eofAtEnd{8}, 4, 4, 1<<20)
	if c.U32BE(); c.Err() != nil {
		t.Errorf("Cursor read of the last 4 bytes: %v, want nil", c.Err())
	}
}

// sizedFault is a source of size bytes, zero-filled, whose reads at or past from fail with err, or,
// when short is set, stop there and report io.EOF, as a source that lost its tail does.
type sizedFault struct {
	size, from int64
	err        error
	short      bool
}

func (s sizedFault) Size() int64 { return s.size }

func (s sizedFault) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) <= s.from {
		clear(p)
		return len(p), nil
	}
	if s.short {
		n := max(int(s.from-off), 0)
		clear(p[:n])
		return n, io.EOF
	}
	return 0, s.err
}

// deviceError is a read failing as a disk does.
var deviceError = &fs.PathError{Op: "read", Path: "media", Err: syscall.EIO}

// readFaults are the two ways a read inside a file fails: a device error, which keeps its cause, and
// a short read, the file ending before the bytes asked for, which is invalid data.
var readFaults = []struct {
	name    string
	src     sizedFault
	cause   error
	invalid bool
}{
	{"a device error", sizedFault{size: 100, from: 50, err: deviceError}, syscall.EIO, false},
	{"a short read", sizedFault{size: 100, from: 50, short: true}, io.ErrUnexpectedEOF, true},
}

// Every read a parser makes fails by one rule: a device error keeps its own cause, so it classifies
// as I/O, and is a fault a parse returns however it tolerates others; a short read is invalid data;
// a read over the limit is refused, and is fatal too.
func TestReadFailureClasses(t *testing.T) {
	for _, c := range readFaults {
		_, err := ReadSlice(c.src, 40, 20, 1<<20)
		if !errors.Is(err, c.cause) || errors.Is(err, waxerr.ErrInvalidData) != c.invalid {
			t.Errorf("ReadSlice, %s: %v; want %v, invalid data %v", c.name, err, c.cause, c.invalid)
		}
		if IsFault(err) == c.invalid || (Fatal(err) != nil) == c.invalid {
			t.Errorf("ReadSlice, %s: fault %v, fatal %v; want both %v", c.name, IsFault(err), Fatal(err), !c.invalid)
		}
		cur := NewCursorAt(c.src, 40, 60, 1<<20)
		if cur.Bytes(20); !errors.Is(cur.Err(), c.cause) || errors.Is(cur.Err(), waxerr.ErrInvalidData) != c.invalid {
			t.Errorf("Cursor.Bytes, %s: %v; want %v, invalid data %v", c.name, cur.Err(), c.cause, c.invalid)
		}
	}
	if _, err := ReadSlice(sizedFault{size: 100, from: 50, err: deviceError}, 40, 20, 10); !errors.Is(err, waxerr.ErrSizeTooLarge) || Fatal(err) == nil || IsFault(err) {
		t.Errorf("ReadSlice over the limit: %v; want ErrSizeTooLarge, fatal and no fault", err)
	}
}

// A walk's read of bytes it expects inside the file: a range inside it reads, one that runs past its
// end finds nothing, and a read over the limit is refused. A failure inside the file fails as every
// read does: a device error keeps its cause, a short read is invalid data.
func TestReadWithin(t *testing.T) {
	r := bytes.NewReader([]byte("0123456789"))
	if b, err := ReadWithin(r, 2, 3, 1<<20); err != nil || string(b) != "234" {
		t.Errorf("inside: %q, %v; want 234", b, err)
	}
	if b, err := ReadWithin(r, 8, 5, 1<<20); err != nil || b != nil {
		t.Errorf("past the end: %q, %v; want nothing and no error", b, err)
	}
	if _, err := ReadWithin(r, 0, 5, 4); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("over the limit: %v; want ErrSizeTooLarge", err)
	}
	if b, err := ReadWithin(eofAtEnd{8}, 4, 4, 1<<20); err != nil || len(b) != 4 {
		t.Errorf("the last 4 bytes with io.EOF: %d bytes, %v; want 4 bytes", len(b), err)
	}
	for _, c := range readFaults {
		b, err := ReadWithin(c.src, 40, 20, 1<<20)
		if !errors.Is(err, c.cause) || errors.Is(err, waxerr.ErrInvalidData) != c.invalid || b != nil {
			t.Errorf("%s inside the file: %d bytes, %v; want %v, invalid data %v", c.name, len(b), err, c.cause, c.invalid)
		}
		p := make([]byte, 20)
		if ok, err := FillWithin(c.src, p, 40); ok || !errors.Is(err, c.cause) || errors.Is(err, waxerr.ErrInvalidData) != c.invalid {
			t.Errorf("filling with %s inside the file: %v, %v; want %v, invalid data %v", c.name, ok, err, c.cause, c.invalid)
		}
	}
	if _, err := ReadWithin(sizedFault{size: 100, from: 50, err: deviceError}, 40, 20, 1<<20); !errors.As(err, new(*fs.PathError)) {
		t.Errorf("a device error: %v; want the path error reachable", err)
	}
	p := make([]byte, 4)
	if ok, err := FillWithin(r, p, 3); !ok || err != nil || string(p) != "3456" {
		t.Errorf("filling inside: %v, %v, %q; want 3456", ok, err, p)
	}
	if ok, err := FillWithin(r, p, 8); ok || err != nil {
		t.Errorf("filling past the end: %v, %v; want false and no error", ok, err)
	}
}

// refusesOffsets is a source of size zero bytes that refuses a read at a negative offset, or one
// whose end passes math.MaxInt64, as os.File does; it counts the reads it is asked to make.
type refusesOffsets struct {
	size  int64
	reads int
}

func (s *refusesOffsets) Size() int64 { return s.size }

func (s *refusesOffsets) ReadAt(p []byte, off int64) (int, error) {
	s.reads++
	switch {
	case off < 0:
		return 0, &fs.PathError{Op: "readat", Path: "media", Err: errors.New("negative offset")}
	case off > math.MaxInt64-int64(len(p)):
		return 0, &fs.PathError{Op: "read", Path: "media", Err: syscall.EINVAL}
	case off >= s.size:
		return 0, io.EOF
	}
	n := copy(p, make([]byte, s.size-off))
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// A read at an offset no file holds is never made, so a file's refusal of it is not taken for a
// device error: a negative offset, or an end past math.MaxInt64, is a short read. FillWithin reads
// nothing past the end of its source either, and such bytes stay absent.
func TestReadOffsetsOutsideAFile(t *testing.T) {
	short := func(err error) bool {
		return errors.Is(err, waxerr.ErrInvalidData) && errors.Is(err, io.ErrUnexpectedEOF) && !IsFault(err)
	}
	for _, off := range []int64{-16, math.MinInt64, math.MaxInt64 - 1, math.MaxInt64} {
		src := &refusesOffsets{size: 100}
		if _, err := ReadSlice(src, off, 2, 1<<20); !short(err) || src.reads != 0 {
			t.Errorf("ReadSlice at %d: %v after %d reads; want a short read, not made", off, err, src.reads)
		}
	}
	src := &refusesOffsets{size: 100}
	if cur := NewCursorAt(src, -16, 32, 1<<20); cur.Bytes(2) != nil || !short(cur.Err()) || src.reads != 0 {
		t.Errorf("Cursor.Bytes at -16: %v after %d reads; want a short read, not made", cur.Err(), src.reads)
	}
	if ok, err := FillWithin(src, make([]byte, 2), -16); ok || !short(err) || src.reads != 0 {
		t.Errorf("FillWithin at -16: %v, %v after %d reads; want a short read, not made", ok, err, src.reads)
	}
	for _, off := range []int64{99, math.MaxInt64 - 1, math.MaxInt64} {
		if ok, err := FillWithin(src, make([]byte, 2), off); ok || err != nil || src.reads != 0 {
			t.Errorf("FillWithin at %d: %v, %v after %d reads; want absent, not read", off, ok, err, src.reads)
		}
	}
}
