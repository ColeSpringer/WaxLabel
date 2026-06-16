// Package bits holds WaxLabel's low-level byte utilities: a bounded,
// sticky-error [Cursor] over an [io.ReaderAt]; a segment-based rewrite
// executor; the non-reflected Ogg CRC; image sniffing; and content hashing.
//
// Everything here treats input as untrusted: allocations are bounded against a
// limit ([waxerr.ErrSizeTooLarge]) and recursion is bounded by [Limits]
// ([waxerr.ErrTooDeep]). No function in this package panics on malformed
// input.
package bits

import (
	"fmt"
	"io"

	"github.com/colespringer/waxlabel/waxerr"
)

// Limits bounds resource use when parsing untrusted input.
type Limits struct {
	// MaxAllocBytes caps any single length-prefixed allocation. A declared
	// size above this yields waxerr.ErrSizeTooLarge instead of an OOM.
	MaxAllocBytes int64
	// MaxDepth caps nested-container recursion (EBML, MP4 atoms). FLAC's flat
	// block list does not nest, but the limit is shared.
	MaxDepth int
}

// DefaultLimits are conservative defaults suitable for typical media files.
var DefaultLimits = Limits{
	MaxAllocBytes: 256 << 20, // 256 MiB — comfortably larger than any cover art
	MaxDepth:      64,
}

// Depth is a recursion-depth guard. The zero value is ready to use.
type Depth struct {
	cur, max int
}

// NewDepth returns a guard limited to max levels.
func NewDepth(max int) *Depth { return &Depth{max: max} }

// Enter descends one level, returning waxerr.ErrTooDeep if the limit is
// exceeded. A failed Enter does not consume a level (it rolls back), so only a
// successful Enter must be paired with Leave.
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

// Cursor is a forward-only reader over a fixed region of an [io.ReaderAt] with
// a sticky error: once a read fails (short read, or a length above the alloc
// limit) every later read is a no-op and [Cursor.Err] reports the cause. This
// lets parsers read a run of fields and check the error once at the end.
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
// exceeds the alloc limit, or waxerr.ErrInvalidData on a short region.
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
	if c.limit > 0 && n > c.limit {
		c.fail(fmt.Errorf("%w: %d bytes exceeds limit %d", waxerr.ErrSizeTooLarge, n, c.limit))
		return nil
	}
	if n > c.Remaining() {
		c.fail(fmt.Errorf("%w: want %d bytes, %d remaining", waxerr.ErrInvalidData, n, c.Remaining()))
		return nil
	}
	buf := make([]byte, n)
	if _, err := c.r.ReadAt(buf, c.pos); err != nil {
		c.fail(fmt.Errorf("%w: read at %d: %v", waxerr.ErrInvalidData, c.pos, err))
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

// PrefixOrNil returns the first n bytes of r, or nil if they cannot be read. It
// is the shared "header region" read used by codecs to build the structural
// source fingerprint: returning nil on error simply weakens the change-detection
// fingerprint rather than failing the whole parse.
func PrefixOrNil(r io.ReaderAt, n, limit int64) []byte {
	b, err := ReadSlice(r, 0, n, limit)
	if err != nil {
		return nil
	}
	return b
}

// ReadSlice reads n bytes at off from r, bounded by limit.
func ReadSlice(r io.ReaderAt, off, n, limit int64) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("%w: negative length %d", waxerr.ErrInvalidData, n)
	}
	if limit > 0 && n > limit {
		return nil, fmt.Errorf("%w: %d bytes exceeds limit %d", waxerr.ErrSizeTooLarge, n, limit)
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("%w: read %d at %d: %v", waxerr.ErrInvalidData, n, off, err)
	}
	return buf, nil
}
