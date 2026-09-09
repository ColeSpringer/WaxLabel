package waxlabel_test

import (
	"context"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// TestAACParsesUnderSmallAllocLimit: the SBR probe reads a window of frames, but a caller
// with a small MaxAllocBytes must still get a parsed file. The probe shrinks to the limit
// instead of failing the read, so the geometry may fall back to the core coder's while the
// parse itself always succeeds.
func TestAACParsesUnderSmallAllocLimit(t *testing.T) {
	data := readFixture(t, heaacAAC)
	for _, limit := range []int64{1024, 4096, 65536} {
		doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: limit}))
		if err != nil {
			t.Fatalf("MaxAllocBytes %d: %v", limit, err)
		}
		if tr := doc.Properties().Tracks[0]; tr.SampleRate == 0 {
			t.Errorf("MaxAllocBytes %d: no sample rate", limit)
		}
	}
	// With room for the probe the played geometry still comes back.
	doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: 1 << 20}))
	if err != nil {
		t.Fatal(err)
	}
	if tr := doc.Properties().Tracks[0]; tr.SampleRate != 44100 || tr.CodecProfile != "HE-AAC" {
		t.Errorf("with room to probe: %d Hz %q, want 44100 HE-AAC", tr.SampleRate, tr.CodecProfile)
	}
}
