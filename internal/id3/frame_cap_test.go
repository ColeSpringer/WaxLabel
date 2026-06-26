package id3

import (
	"errors"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestCheckSizeFrameCapBoundary is the F2 regression: the write path enforces the same
// element cap the reader uses, at the exact off-by-one boundary. The reader's
// CheckElementCap errors at len >= max on the PRE-append count, so it accepts a tag of
// exactly max frames; CheckSize must therefore accept max (a strict > test) and reject only
// max+1 - a final-count CheckElementCap would wrongly reject a legitimate max-frame tag.
func TestCheckSizeFrameCapBoundary(t *testing.T) {
	max := bits.DefaultLimits.MaxElements
	frames := func(n int) []Frame {
		fs := make([]Frame, n)
		for i := range fs {
			fs[i] = Frame{ID: "TXXX", Body: []byte{0}} // a tiny, valid frame body
		}
		return fs
	}

	if err := CheckSize(4, frames(max), max); err != nil {
		t.Errorf("a %d-frame tag (exactly the cap the reader accepts) must write, got %v", max, err)
	}
	if err := CheckSize(4, frames(max+1), max); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("a %d-frame tag (over the cap) must be rejected with ErrSizeTooLarge, got %v", max+1, err)
	}
	// A zero limit disables the cap (defensive: never reject when no limit is supplied).
	if err := CheckSize(4, frames(max+1), 0); err != nil {
		t.Errorf("maxElements <= 0 disables the cap, got %v", err)
	}
}
