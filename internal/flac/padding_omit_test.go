package flac

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// TestSerializeMetadataOmitsEmptyPadding checks that a zero padding target
// (--no-padding / --padding 0) on the grow path leaves no PADDING block at all,
// not a useless zero-length one. A FLAC with no PADDING block is valid, and the
// last-block flag lands on the true last block.
func TestSerializeMetadataOmitsEmptyPadding(t *testing.T) {
	blocks := []block{{code: blkVorbisComment, body: renderVorbisComment("v", nil)}}

	out, padSize, all, clamped := serializeMetadata(blocks, &doc{}, core.PaddingPolicy{Target: 0})
	if clamped {
		t.Error("a zero padding target must not report clamped")
	}
	if padSize != 0 {
		t.Errorf("padSize = %d, want 0", padSize)
	}
	for _, b := range all {
		if b.code == blkPadding {
			t.Fatalf("emitted a PADDING block for a zero target: %+v", all)
		}
	}
	if len(all) != 1 || all[0].code != blkVorbisComment {
		t.Fatalf("all = %+v, want only the VORBIS_COMMENT block", all)
	}
	// With no padding block appended, the last-block flag must land on the sole
	// VORBIS_COMMENT block (header byte high bit set).
	if len(out) == 0 || out[0] != 0x80|blkVorbisComment {
		t.Errorf("first/last block header = 0x%02x, want 0x%02x (last-block flag on VORBIS_COMMENT)", out[0], 0x80|blkVorbisComment)
	}

	// Control: a positive target still appends exactly one PADDING block on the grow path.
	_, padSize2, all2, _ := serializeMetadata(blocks, &doc{}, core.PaddingPolicy{Target: 100})
	if padSize2 != 100 || all2[len(all2)-1].code != blkPadding {
		t.Errorf("positive target: padSize=%d lastCode=%d, want 100 with a trailing PADDING block", padSize2, all2[len(all2)-1].code)
	}
}

// TestPaddingBytesMatchesReuse: a rewrite drops every source PADDING block and refills the
// region, so k blocks collapse into one and k-1 four-byte headers become usable payload.
// PaddingBytes must report what that rewrite will leave - the number plan prints - not a
// plain sum of the source bodies, which under-reports by exactly 4*(k-1).
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

		// The reuse path with an unchanged content size is exactly the in-place write the
		// read accessor claims to describe.
		_, padSize, _, _ := serializeMetadata([]block{comment}, d, core.PaddingPolicy{ReuseInPlace: true})
		if got := d.PaddingBytes(); got != int64(padSize) {
			t.Errorf("%d PADDING block(s): PaddingBytes = %d, reuse writes %d", n, got, padSize)
		}
		if want := int64(100*n + 4*max(0, n-1)); n > 0 && d.PaddingBytes() != want {
			t.Errorf("%d PADDING block(s): PaddingBytes = %d, want %d (bodies plus the reclaimed headers)",
				n, d.PaddingBytes(), want)
		}
	}
}
