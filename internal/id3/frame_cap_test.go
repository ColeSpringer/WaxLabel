package id3

import (
	"errors"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestClampPadding checks the arithmetic guard for the ID3v2 payload size field:
// frames plus padding must fit the sync-safe 28-bit limit. nonPad includes the 10-byte
// tag header, so the frame bytes are nonPad-10.
func TestClampPadding(t *testing.T) {
	const limit = int64(maxFrameSize)
	// Within the field: padding is returned unchanged.
	if got, clamped := clampPadding(10+1000, 500); got != 500 || clamped {
		t.Errorf("clampPadding(1010, 500) = (%d, %v), want (500, false)", got, clamped)
	}
	// Exactly at the boundary: 1000 frame bytes leaves limit-1000 for padding.
	nonPad := int64(10 + 1000)
	maxPad := limit - 1000
	if got, clamped := clampPadding(nonPad, maxPad); got != maxPad || clamped {
		t.Errorf("clampPadding at the max = (%d, %v), want (%d, false)", got, clamped, maxPad)
	}
	if got, clamped := clampPadding(nonPad, maxPad+1); got != maxPad || !clamped {
		t.Errorf("clampPadding(maxPad+1) = (%d, %v), want (%d, true)", got, clamped, maxPad)
	}
	// An over-limit frame set (which CheckSize rejects upstream) floors padding to 0,
	// never negative.
	if got, clamped := clampPadding(10+limit+5, 100); got != 0 || !clamped {
		t.Errorf("clampPadding(over-limit frames) = (%d, %v), want (0, true)", got, clamped)
	}
}

// TestRenderFrontTagClampsPadding covers the write path: reused padding for a very large
// existing tag is clamped before the report is built, and the rendered size field matches
// the actual payload. It allocates about 256 MB to reach the boundary, so -short skips it.
func TestRenderFrontTagClampsPadding(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates ~256 MB to exercise the 28-bit size-field boundary")
	}
	frames := []Frame{{ID: "TXXX", Body: []byte{0, 'x'}}}
	nonPad := RenderedSize(frames)
	// ReuseInPlace with a region just over the 28-bit limit asks for padding that cannot fit.
	srcTagLen := int64(maxFrameSize) + 1000
	pol := core.PaddingPolicy{ReuseInPlace: true}
	requested := pol.ReuseOrTarget(srcTagLen, nonPad)
	if _, clamped := clampPadding(nonPad, requested); !clamped {
		t.Fatalf("setup: requested padding %d should have exceeded the field", requested)
	}

	ft := RenderFrontTag(NewEmpty(4), 4, frames, RebuildInfo{}, pol, srcTagLen,
		true, true, false, 0, false, 0, false, 0)

	if ft.Padding >= requested {
		t.Errorf("ft.Padding = %d was not clamped below the requested %d", ft.Padding, requested)
	}
	var warned bool
	for _, w := range ft.Warnings {
		if w.Code == core.WarnPaddingClamped {
			warned = true
		}
	}
	if !warned {
		t.Error("expected a WarnPaddingClamped warning")
	}
	// The 28-bit size field must equal the actual payload (frames+padding) and fit the field.
	sz := syncSafe(ft.Bytes[6:10])
	if want := int64(len(ft.Bytes)) - 10; sz != want {
		t.Errorf("28-bit size field = %d, want %d (payload after the 10-byte header)", sz, want)
	}
	if sz > int64(maxFrameSize) {
		t.Errorf("28-bit size field %d overflowed the field max %d", sz, maxFrameSize)
	}
}

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
