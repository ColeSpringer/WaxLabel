package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestMP4OversizedCoverFailsLoudNotTruncated covers the read side: a covr item whose payload
// exceeds the configured alloc limit must fail loudly with ErrSizeTooLarge rather than silently
// truncate to a partial, unreadable cover (the old min(payloadSize, maxMetaChunk) behavior). The
// limit is exercised with a small WithLimits so the test needs only a few KiB, not a 64+ MiB
// payload; under a generous limit the same cover reads back in full, proving nothing is truncated.
func TestMP4OversizedCoverFailsLoudNotTruncated(t *testing.T) {
	bigData := bytes.Repeat([]byte{0x7F}, 8192)
	covr := mp4Atom("covr", mp4Data(14, bigData)) // type 14 = PNG cover
	data := mp4Tagged(mp4Text("\xa9nam", "T"), covr)

	// Too-small limit: the covr read must fail loudly, not return a truncated cover.
	_, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: 4096}))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("oversized covr under a 4 KiB limit: err = %v, want ErrSizeTooLarge", err)
	}

	// Generous (default 256 MiB) limit: the cover reads back in full - no truncation.
	pics := mustParseBytes(t, data).Pictures()
	if len(pics) != 1 {
		t.Fatalf("expected 1 cover, got %d", len(pics))
	}
	if !bytes.Equal(pics[0].Data, bigData) {
		t.Errorf("cover truncated: read %d bytes, want %d", len(pics[0].Data), len(bigData))
	}
}
