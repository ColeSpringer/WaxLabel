package bits

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/colespringer/waxlabel/waxerr"
)

// SegmentsReader reads the bytes [Write] would emit for a list of segments, without emitting them.
type SegmentsReader struct {
	src    io.ReaderAt
	segs   []Segment
	starts []int64 // output offset of each segment
	size   int64
	err    error
}

// NewSegmentsReader returns a reader over the output of segs copied from src.
func NewSegmentsReader(src io.ReaderAt, segs []Segment) *SegmentsReader {
	r := &SegmentsReader{src: src, segs: segs, starts: make([]int64, len(segs))}
	for i, s := range segs {
		r.starts[i] = r.size
		switch {
		case s.Literal != nil:
			r.size += int64(len(s.Literal))
		case s.Len < 0:
			r.err = fmt.Errorf("%w: negative copy length %d", waxerr.ErrInvalidData, s.Len)
		default:
			r.size += s.Len
		}
	}
	return r
}

// Size is the output length.
func (r *SegmentsReader) Size() int64 { return r.size }

// ReadAt reads output bytes at off. A source that ends inside a copy segment is invalid data, as
// in [Write].
func (r *SegmentsReader) ReadAt(p []byte, off int64) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if off < 0 {
		return 0, errors.New("bits.SegmentsReader.ReadAt: negative offset")
	}
	if len(p) == 0 {
		return 0, nil // as os.File, even at the end
	}
	if off >= r.size {
		return 0, io.EOF
	}
	// The last segment starting at or before off holds it: an empty one is followed by a segment
	// with the same start.
	i := sort.Search(len(r.starts), func(i int) bool { return r.starts[i] > off }) - 1
	n := 0
	for ; n < len(p) && i < len(r.segs); i++ {
		s, at := r.segs[i], off+int64(n)-r.starts[i]
		if s.Literal != nil {
			n += copy(p[n:], s.Literal[at:])
			continue
		}
		want := int(min(int64(len(p)-n), s.Len-at))
		got, err := r.src.ReadAt(p[n:n+want], s.Off+at)
		n += got
		if got < want {
			if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return n, fmt.Errorf("%w: copy read at %d: source ends early", waxerr.ErrInvalidData, s.Off+at+int64(got))
			}
			return n, fmt.Errorf("copy read at %d: %w", s.Off+at+int64(got), err)
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
