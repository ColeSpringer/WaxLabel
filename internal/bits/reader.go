// Package bits provides bounded byte utilities: a sticky-error [Cursor] over
// [io.ReaderAt], rewrite [Segment]s, Ogg CRC, image sniffing, and hashing.
// [Limits] caps allocations ([waxerr.ErrSizeTooLarge]) and recursion
// ([waxerr.ErrTooDeep]). Nothing panics on malformed input.
package bits

import (
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/colespringer/waxlabel/waxerr"
)

// Limits bounds resource use when parsing untrusted input. Passed to WithLimits, a zero field takes
// that field's default.
type Limits struct {
	// MaxAllocBytes caps one length-prefixed allocation; above it: waxerr.ErrSizeTooLarge. It
	// also caps what an Ogg parse keeps for its audio pages (48 bytes each) and what a rewrite
	// takes to renumber them (88 each).
	MaxAllocBytes int64
	// MaxDepth caps nested-container recursion (EBML, MP4 atoms).
	MaxDepth int
	// MaxElements caps metadata descriptors per pass (RIFF chunks, FLAC/MP4 blocks,
	// ID3 frames, Musepack chapter packets). MaxAllocBytes bounds each read, not descriptor
	// count; minimum-size elements could otherwise OOM. It also caps the header walks across
	// a WavPack or Musepack SV8 stream: past it the walk to the stream's end leaves that end
	// unknown, and the walk to the Musepack chapters reads none. Other audio loops (Ogg
	// pages, clusters) are not capped by it.
	MaxElements int
}

// DefaultLimits are defaults for typical media files.
var DefaultLimits = Limits{
	MaxAllocBytes: 256 << 20, // 256 MiB
	MaxDepth:      64,
	MaxElements:   100000,
}

// Depth limits nesting (Enter/Leave) and optionally element count (Count). Zero
// element cap disables breadth limiting. Zero value is usable.
type Depth struct {
	cur, max       int
	elems, elemMax int
	elemNoun       string
}

// NewDepth returns a guard limited to max nesting levels.
func NewDepth(max int) *Depth { return &Depth{max: max} }

// WithElementCap limits visited elements; max <= 0 disables. noun names them in errors.
func (d *Depth) WithElementCap(max int, noun string) *Depth {
	d.elemMax = max
	d.elemNoun = noun
	return d
}

// Count records one element; returns waxerr.ErrSizeTooLarge at cap. No-op without cap.
func (d *Depth) Count() error {
	if err := CheckElementCap(d.elems, d.elemMax, d.elemNoun); err != nil {
		return err
	}
	d.elems++
	return nil
}

// Enter descends one level; waxerr.ErrTooDeep on exceed. Failed Enter rolls back.
func (d *Depth) Enter() error {
	d.cur++
	if d.max > 0 && d.cur > d.max {
		d.cur--
		return fmt.Errorf("%w: exceeded %d levels", waxerr.ErrTooDeep, d.max)
	}
	return nil
}

// Leave ascends one level.
func (d *Depth) Leave() { d.cur-- }

// CheckElementCap returns waxerr.ErrSizeTooLarge when n >= max (max <= 0 disables).
func CheckElementCap(n, max int, what string) error {
	if max > 0 && n >= max {
		return fmt.Errorf("%w: more than %d %s", waxerr.ErrSizeTooLarge, max, what)
	}
	return nil
}

// Max reports the nesting limit.
func (d *Depth) Max() int { return d.max }

// Cursor is a forward-only, sticky-error reader over a fixed [io.ReaderAt] region.
type Cursor struct {
	r     io.ReaderAt
	pos   int64
	end   int64
	limit int64
	err   error
}

// NewCursor returns a Cursor over [0, size) of r, capping single allocations
// at maxAlloc.
func NewCursor(r io.ReaderAt, size, maxAlloc int64) *Cursor {
	return &Cursor{r: r, end: size, limit: maxAlloc}
}

// NewCursorAt returns a Cursor over [off, off+length) of r.
func NewCursorAt(r io.ReaderAt, off, length, maxAlloc int64) *Cursor {
	return &Cursor{r: r, pos: off, end: off + length, limit: maxAlloc}
}

// Pos reports the current absolute offset.
func (c *Cursor) Pos() int64 { return c.pos }

// Remaining reports the bytes left before the region end.
func (c *Cursor) Remaining() int64 {
	if c.pos > c.end {
		return 0
	}
	return c.end - c.pos
}

// Err returns the first error encountered, or nil.
func (c *Cursor) Err() error { return c.err }

func (c *Cursor) fail(err error) {
	if c.err == nil {
		c.err = err
	}
}

// Skip advances n bytes without reading them.
func (c *Cursor) Skip(n int64) {
	if c.err != nil {
		return
	}
	if n < 0 || n > c.Remaining() {
		c.fail(fmt.Errorf("%w: skip %d with %d remaining", waxerr.ErrInvalidData, n, c.Remaining()))
		return
	}
	c.pos += n
}

// Bytes reads exactly n bytes. It fails with waxerr.ErrSizeTooLarge if n
// exceeds the alloc limit, or waxerr.ErrInvalidData on a short region; a read
// that fails fails as [ReadSlice] says.
func (c *Cursor) Bytes(n int64) []byte {
	if c.err != nil {
		return nil
	}
	if n < 0 {
		c.fail(fmt.Errorf("%w: negative length %d", waxerr.ErrInvalidData, n))
		return nil
	}
	if n == 0 {
		return nil
	}
	if err := CheckAlloc(n, c.limit); err != nil {
		c.fail(err)
		return nil
	}
	if n > c.Remaining() {
		c.fail(fmt.Errorf("%w: want %d bytes, %d remaining", waxerr.ErrInvalidData, n, c.Remaining()))
		return nil
	}
	buf := make([]byte, n)
	if err := ReadFullAt(c.r, buf, c.pos); err != nil {
		c.fail(readFailure(n, c.pos, err))
		return nil
	}
	c.pos += n
	return buf
}

// Byte reads a single byte.
func (c *Cursor) Byte() byte {
	b := c.Bytes(1)
	if b == nil {
		return 0
	}
	return b[0]
}

// U16BE reads a big-endian uint16.
func (c *Cursor) U16BE() uint16 {
	b := c.Bytes(2)
	if b == nil {
		return 0
	}
	return uint16(b[0])<<8 | uint16(b[1])
}

// U24BE reads a big-endian 24-bit value into a uint32.
func (c *Cursor) U24BE() uint32 {
	b := c.Bytes(3)
	if b == nil {
		return 0
	}
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
}

// U32BE reads a big-endian uint32.
func (c *Cursor) U32BE() uint32 {
	b := c.Bytes(4)
	if b == nil {
		return 0
	}
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// U32LE reads a little-endian uint32 (Vorbis comment lengths).
func (c *Cursor) U32LE() uint32 {
	b := c.Bytes(4)
	if b == nil {
		return 0
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// U64BE reads a big-endian uint64.
func (c *Cursor) U64BE() uint64 {
	b := c.Bytes(8)
	if b == nil {
		return 0
	}
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}

// ReadFullAt fills p from r at off. A full read succeeds whatever error comes with it, since
// io.ReaderAt may return io.EOF with the last bytes; a short read with no error is
// io.ErrUnexpectedEOF. A read at an offset no file holds, below 0 or ending past math.MaxInt64, is
// not made and is io.ErrUnexpectedEOF too, so a file's refusal of it never reads as a device error.
func ReadFullAt(r io.ReaderAt, p []byte, off int64) error {
	if off < 0 || off > math.MaxInt64-int64(len(p)) {
		return io.ErrUnexpectedEOF
	}
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// CheckAlloc refuses an n-byte allocation over limit with waxerr.ErrSizeTooLarge, for a buffer the
// caller fills itself. limit <= 0 disables it, unlike [ReadSlice], which refuses such a limit.
func CheckAlloc(n, limit int64) error {
	if limit > 0 && n > limit {
		return fmt.Errorf("%w: %d bytes exceeds limit %d", waxerr.ErrSizeTooLarge, n, limit)
	}
	return nil
}

// ReadSlice reads n bytes at off. limit must be positive; caller supplies MaxAllocBytes. A read over
// limit is refused with waxerr.ErrSizeTooLarge. A short read, the source ending before the n bytes,
// is waxerr.ErrInvalidData, and so is a read at an offset no file holds, below 0 or ending past
// math.MaxInt64, which is not made ([ReadFullAt]); any other failed read, a device error, is a fault
// ([IsFault]) that keeps its own cause, so it classifies as the I/O failure it is.
func ReadSlice(r io.ReaderAt, off, n, limit int64) ([]byte, error) {
	if err := checkSlice(n, limit); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if n == 0 {
		return buf, nil // zero-length read may still EOF at end
	}
	if err := ReadFullAt(r, buf, off); err != nil {
		return nil, readFailure(n, off, err)
	}
	return buf, nil
}

// readFailure is the error of a read of n bytes at off that failed with err, the one rule for every
// read a parser makes: a short read is waxerr.ErrInvalidData, with io.ErrUnexpectedEOF, and any other
// failure is a fault ([IsFault]) that wraps its own cause alone.
func readFailure(n, off int64, err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: read %d at %d: %w", waxerr.ErrInvalidData, n, off, io.ErrUnexpectedEOF)
	}
	return &fault{n: n, off: off, err: err}
}

// fault is a read that failed for a reason other than the end of the source: a device error.
type fault struct {
	n, off int64
	err    error
}

func (f *fault) Error() string { return fmt.Sprintf("read %d at %d: %v", f.n, f.off, f.err) }

func (f *fault) Unwrap() error { return f.err }

// IsFault reports whether err holds a read of this package that failed for a reason other than the
// end of the source, a device error.
func IsFault(err error) bool {
	_, ok := errors.AsType[*fault](err)
	return ok
}

// Fatal returns err when a parse must fail with it wherever it tolerates a missing or malformed
// structure: a read refused over the allocation limit, or a fault ([IsFault]). Any other error, a
// short read among them, is nil.
func Fatal(err error) error {
	if errors.Is(err, waxerr.ErrSizeTooLarge) || IsFault(err) {
		return err
	}
	return nil
}

// Sized is a source that knows its length.
type Sized interface {
	io.ReaderAt
	Size() int64
}

// ReadWithin reads the n bytes at off of r, which a walk over r expects inside it, as [FillWithin]
// does: nil, with no error, only when they run past the end of r. A read over limit is refused as
// [ReadSlice] refuses one.
func ReadWithin(r Sized, off, n, limit int64) ([]byte, error) {
	if err := checkSlice(n, limit); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if ok, err := FillWithin(r, buf, off); !ok {
		return nil, err
	}
	return buf, nil
}

// FillWithin fills p from r at off with bytes a walk over r expects inside it. Bytes that run past
// the end of r are absent and not read: ok is false with no error. A read that fails inside r fails
// as [ReadSlice] says: a short read, one at a negative offset among them, is invalid data, and a
// device error is a fault.
func FillWithin(r Sized, p []byte, off int64) (ok bool, err error) {
	n := int64(len(p))
	if off > math.MaxInt64-n || off+n > r.Size() {
		return false, nil
	}
	if err := ReadFullAt(r, p, off); err != nil {
		return false, readFailure(n, off, err)
	}
	return true, nil
}

// checkSlice refuses a read of n bytes that [ReadSlice] cannot make: a negative length, no positive
// limit, or more than limit.
func checkSlice(n, limit int64) error {
	if n < 0 {
		return fmt.Errorf("%w: negative length %d", waxerr.ErrInvalidData, n)
	}
	if limit <= 0 {
		return fmt.Errorf("%w: ReadSlice needs a positive allocation limit (got %d)", waxerr.ErrInvalidData, limit)
	}
	return CheckAlloc(n, limit)
}
