package bits

import (
	"context"
	"fmt"
	"io"

	"github.com/colespringer/waxlabel/waxerr"
)

// Segment is literal bytes or a source copy range. Rewrites replace metadata while
// copying audio verbatim and streaming to disk.
type Segment struct {
	// Literal, when non-nil, is emitted as-is and Off/Len are ignored.
	Literal []byte
	// Off and Len name a range in the source to copy when Literal is nil.
	Off int64
	Len int64
}

// Lit returns a literal segment.
func Lit(b []byte) Segment { return Segment{Literal: b} }

// Copy returns a copy-from-source segment.
func Copy(off, length int64) Segment { return Segment{Off: off, Len: length} }

// OutputLen returns the total number of bytes the segments will emit.
func OutputLen(segs []Segment) int64 {
	var n int64
	for _, s := range segs {
		if s.Literal != nil {
			n += int64(len(s.Literal))
		} else {
			n += s.Len
		}
	}
	return n
}

// Tap observes copied (not literal) source bytes during a rewrite, by offset.
type Tap interface {
	// Observe receives bytes starting at srcOff. p is reused; copy if retained.
	Observe(srcOff int64, p []byte)
}

// Write streams segs to dst from src. tap sees each copied run. Checks ctx between chunks.
func Write(ctx context.Context, dst io.Writer, src io.ReaderAt, segs []Segment, tap Tap) (int64, error) {
	buf := make([]byte, 1<<16)
	var total int64
	for _, s := range segs {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if s.Literal != nil {
			n, err := dst.Write(s.Literal)
			total += int64(n)
			if err != nil {
				return total, err
			}
			continue
		}
		if s.Len < 0 {
			return total, fmt.Errorf("%w: negative copy length %d", waxerr.ErrInvalidData, s.Len)
		}
		off, remaining := s.Off, s.Len
		for remaining > 0 {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			chunk := int64(len(buf))
			if remaining < chunk {
				chunk = remaining
			}
			if _, err := src.ReadAt(buf[:chunk], off); err != nil {
				return total, fmt.Errorf("%w: copy read at %d: %v", waxerr.ErrInvalidData, off, err)
			}
			if tap != nil {
				tap.Observe(off, buf[:chunk])
			}
			n, err := dst.Write(buf[:chunk])
			total += int64(n)
			if err != nil {
				return total, err
			}
			off += chunk
			remaining -= chunk
		}
	}
	return total, nil
}
