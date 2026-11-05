package flac

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// TestSerializeMetadataOmitsEmptyPadding: Target 0 emits no PADDING block.
func TestSerializeMetadataOmitsEmptyPadding(t *testing.T) {
	blocks := []block{{code: blkVorbisComment, body: renderVorbisComment("v", nil)}}
	commentLen := 4 + len(blocks[0].body)

	out, padSize, clamped := serializeMetadata(blocks, &doc{}, core.PaddingPolicy{Target: 0})
	if clamped {
		t.Error("a zero padding target must not report clamped")
	}
	if padSize != 0 {
		t.Errorf("padSize = %d, want 0", padSize)
	}
	if len(out) != commentLen || out[0] != 0x80|blkVorbisComment {
		t.Errorf("rendered % x, want only the VORBIS_COMMENT block, flagged last", out)
	}

	// Positive target still appends one PADDING block.
	out, padSize, _ = serializeMetadata(blocks, &doc{}, core.PaddingPolicy{Target: 100})
	if padSize != 100 || len(out) != commentLen+4+100 || out[commentLen] != 0x80|blkPadding {
		t.Errorf("positive target: padSize=%d, %d bytes rendered, want 100 with a trailing PADDING block", padSize, len(out))
	}
}

// TestPaddingBytesMatchesReuse checks that PaddingBytes reports what a reuse rewrite
// leaves: it drops every source PADDING block and refills the region, so k blocks
// collapse into one and k-1 four-byte headers become payload; a plain sum of the bodies
// under-reports by 4*(k-1).
func TestPaddingBytesMatchesReuse(t *testing.T) {
	comment := block{code: blkVorbisComment, body: renderVorbisComment("v", nil)}
	for _, n := range []int{0, 1, 2, 5} {
		blocks := []block{comment}
		region := 4 + len(comment.body)
		for range n {
			blocks = append(blocks, block{code: blkPadding, body: make([]byte, 100)})
			region += 4 + 100
		}
		d := &doc{blocks: blocks, audioStart: int64(4 + region)}

		// The reuse path with unchanged content is the in-place write the accessor describes.
		_, padSize, _ := serializeMetadata([]block{comment}, d, core.PaddingPolicy{ReuseInPlace: true})
		if got := d.PaddingBytes(); got != int64(padSize) {
			t.Errorf("%d PADDING block(s): PaddingBytes = %d, reuse writes %d", n, got, padSize)
		}
		if want := int64(100*n + 4*max(0, n-1)); n > 0 && d.PaddingBytes() != want {
			t.Errorf("%d PADDING block(s): PaddingBytes = %d, want %d (bodies plus the reclaimed headers)",
				n, d.PaddingBytes(), want)
		}
	}
}
