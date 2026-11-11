package flac

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// TestSerializeMetadataOmitsEmptyPadding: Target 0 emits no PADDING block.
func TestSerializeMetadataOmitsEmptyPadding(t *testing.T) {
	blocks := []block{{code: blkVorbisComment, body: renderVorbisComment("v", nil)}}
	commentLen := 4 + len(blocks[0].body)

	out, padSize, clamped, _ := serializeMetadata(blocks, &doc{}, core.PaddingPolicy{Target: 0})
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
	out, padSize, _, _ = serializeMetadata(blocks, &doc{}, core.PaddingPolicy{Target: 100})
	if padSize != 100 || len(out) != commentLen+4+100 || out[commentLen] != 0x80|blkPadding {
		t.Errorf("positive target: padSize=%d, %d bytes rendered, want 100 with a trailing PADDING block", padSize, len(out))
	}
}

// TestPaddingBytesMatchesReuse checks that PaddingBytes reports what a rewrite that keeps the
// whole region leaves (a policy with no Max below it): it drops every source PADDING block and
// refills the region, so k blocks collapse into one and k-1 four-byte headers become payload; a
// plain sum of the bodies under-reports by 4*(k-1).
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

// TestSerializeMetadataCapsReusedPadding: a region with more free space than the policy's Max
// keeps Max of it, under the default policy too; a policy with no Max keeps the region whole.
func TestSerializeMetadataCapsReusedPadding(t *testing.T) {
	comment := block{code: blkVorbisComment, body: renderVorbisComment("v", nil)}
	const region = 4 << 20
	d := &doc{
		blocks:     []block{comment, {code: blkPadding, body: make([]byte, region)}},
		audioStart: int64(4 + 4 + len(comment.body) + 4 + region),
	}
	for name, tc := range map[string]struct {
		pol  core.PaddingPolicy
		want int
	}{
		"the default":      {core.DefaultPadding, 1 << 20},
		"compatible":       {core.PaddingPolicy{Target: 4096, Max: 1 << 20, ReuseInPlace: true}, 1 << 20},
		"an explicit size": {core.PaddingPolicy{Target: 4096, Min: 4096, ReuseInPlace: true}, region},
		"a Max above it":   {core.PaddingPolicy{Target: 4096, Max: 8 << 20, ReuseInPlace: true}, region},
	} {
		_, padSize, clamped, _ := serializeMetadata([]block{comment}, d, tc.pol)
		if padSize != tc.want || clamped {
			t.Errorf("%s: padding = %d (clamped %v), want %d", name, padSize, clamped, tc.want)
		}
	}

	// A Max past what one block holds is kept in two, a block header short of it.
	const big = 40 << 20
	huge := &doc{audioStart: int64(4 + 4 + len(comment.body) + big)}
	out, padSize, clamped, count := serializeMetadata([]block{comment}, huge, core.PaddingPolicy{Target: 4096, Max: 20 << 20, ReuseInPlace: true})
	if padSize != 20<<20-4 || clamped || count != 3 || len(out) != 4+len(comment.body)+20<<20+4 {
		t.Errorf("Max past the block limit: padding = %d in %d blocks, %d bytes (clamped %v)", padSize, count, len(out), clamped)
	}
	// A policy whose Min exceeds its Max keeps Max of the region, as an ID3 front tag does.
	if _, padSize, _, _ := serializeMetadata([]block{comment}, d, core.PaddingPolicy{Target: 100, Min: 4096, Max: 1024, ReuseInPlace: true}); padSize != 1024 {
		t.Errorf("Min above Max: padding = %d, want 1024", padSize)
	}
	// With reuse off the region does not matter.
	if _, padSize, _, _ := serializeMetadata([]block{comment}, d, core.PaddingPolicy{Target: 4096, Max: 1 << 20}); padSize != 4096 {
		t.Errorf("no reuse: padding = %d, want the target", padSize)
	}
}
