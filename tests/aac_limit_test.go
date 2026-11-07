package waxlabel_test

import (
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// The frame walk and the SBR probe read windows of frames. A limit below a window refuses the file
// rather than shrinking the window, which would read HE-AAC as its core coder; a limit that holds
// the windows reads the played geometry.
func TestAACSmallAllocLimitRefuses(t *testing.T) {
	data := readFixture(t, heaacAAC)
	for _, limit := range []int64{64, 1024, 4096} {
		_, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: limit}))
		if !errors.Is(err, waxerr.ErrSizeTooLarge) {
			t.Errorf("MaxAllocBytes %d: err = %v, want declared size too large", limit, err)
		}
	}
	for _, limit := range []int64{64 << 10, 1 << 20} {
		doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: limit}))
		if err != nil {
			t.Fatalf("MaxAllocBytes %d: %v", limit, err)
		}
		if tr := doc.Properties().Tracks[0]; tr.SampleRate != 44100 || tr.CodecProfile != "HE-AAC" {
			t.Errorf("MaxAllocBytes %d: %d Hz %q, want 44100 HE-AAC", limit, tr.SampleRate, tr.CodecProfile)
		}
	}
}
