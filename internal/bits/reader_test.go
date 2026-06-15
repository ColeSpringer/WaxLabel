package bits

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/colespringer/waxlabel/waxerr"
)

func TestCursorReads(t *testing.T) {
	data := []byte{
		0x12, 0x34, // u16be = 0x1234
		0x00, 0x00, 0x05, // u24be = 5
		0x00, 0x00, 0x00, 0x07, // u32be = 7
		0x09, 0x00, 0x00, 0x00, // u32le = 9
		'h', 'i',
	}
	c := NewCursor(bytes.NewReader(data), int64(len(data)), 1<<20)
	if got := c.U16BE(); got != 0x1234 {
		t.Errorf("U16BE = 0x%x", got)
	}
	if got := c.U24BE(); got != 5 {
		t.Errorf("U24BE = %d", got)
	}
	if got := c.U32BE(); got != 7 {
		t.Errorf("U32BE = %d", got)
	}
	if got := c.U32LE(); got != 9 {
		t.Errorf("U32LE = %d", got)
	}
	if got := string(c.Bytes(2)); got != "hi" {
		t.Errorf("Bytes = %q", got)
	}
	if err := c.Err(); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.Remaining() != 0 {
		t.Errorf("Remaining = %d, want 0", c.Remaining())
	}
}

func TestCursorShortReadSticky(t *testing.T) {
	c := NewCursor(bytes.NewReader([]byte{0x01}), 1, 1<<20)
	_ = c.U32BE() // wants 4 bytes, only 1 available
	if c.Err() == nil {
		t.Fatal("expected error on short read")
	}
	// Subsequent reads are no-ops returning zero, error stays sticky.
	if got := c.Byte(); got != 0 {
		t.Errorf("post-error Byte = %d, want 0", got)
	}
	if !errors.Is(c.Err(), waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want ErrInvalidData", c.Err())
	}
}

func TestCursorAllocLimit(t *testing.T) {
	data := make([]byte, 100)
	c := NewCursor(bytes.NewReader(data), int64(len(data)), 8)
	_ = c.Bytes(50) // exceeds limit of 8
	if !errors.Is(c.Err(), waxerr.ErrSizeTooLarge) {
		t.Errorf("err = %v, want ErrSizeTooLarge", c.Err())
	}
}

func TestWriteSegments(t *testing.T) {
	src := bytes.NewReader([]byte("0123456789"))
	segs := []Segment{
		Lit([]byte("HEADER")),
		Copy(2, 3),        // "234"
		Lit([]byte("--")), // separator
		Copy(8, 2),        // "89"
	}
	if got := OutputLen(segs); got != int64(6+3+2+2) {
		t.Errorf("OutputLen = %d", got)
	}
	var out bytes.Buffer
	n, err := Write(context.Background(), &out, src, segs, nil)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := "HEADER234--89"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
	if n != int64(len(want)) {
		t.Errorf("written = %d, want %d", n, len(want))
	}
}

// fakeTap records bytes copied from a claimed source range, byte-precisely.
type fakeTap struct {
	lo, hi int64
	buf    bytes.Buffer
}

func (f *fakeTap) Observe(srcOff int64, p []byte) {
	end := srcOff + int64(len(p))
	lo, hi := max(srcOff, f.lo), min(end, f.hi)
	if lo < hi {
		f.buf.Write(p[lo-srcOff : hi-srcOff])
	}
}

func TestWriteSegmentsTap(t *testing.T) {
	src := bytes.NewReader([]byte("0123456789"))
	// Claim [5,8); a single whole-file copy must contribute only "567".
	tap := &fakeTap{lo: 5, hi: 8}
	segs := []Segment{Copy(0, 10)}
	var out bytes.Buffer
	if _, err := Write(context.Background(), &out, src, segs, tap); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if tap.buf.String() != "567" {
		t.Errorf("tapped %q, want %q", tap.buf.String(), "567")
	}
}
