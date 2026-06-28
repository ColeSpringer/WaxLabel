package mp4

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// TestResultUdtaRawPatchesMetaSize verifies resultUdtaRaw's structural invariant:
// after the ilst region inside the cached udta payload grows, the re-walked bytes
// carry a self-consistent meta box whose declared size grew by the same delta. This
// covers future splice paths as well as chained edits on a returned document.
func TestResultUdtaRawPatchesMetaSize(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/sample_chapters.m4b")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	media, err := parse(context.Background(), core.BytesSource(raw), core.ParseOptions{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d, ok := media.Native.(*doc)
	if !ok || d.udta == nil || d.meta == nil || d.ilst == nil || d.udtaRaw == nil {
		t.Fatal("fixture lacks the udta/meta/ilst/udtaRaw the splice path needs")
	}

	ups := d.udta.offset + d.udta.headerLen
	rStart, rEnd := ilstRegionRel(d, ups)
	// Simulate an ilst-grow: keep the original region bytes and append an 8-byte free
	// atom, so the region (and thus the enclosing meta) must grow by 8.
	free := []byte{0, 0, 0, 8, 'f', 'r', 'e', 'e'}
	grown := append(slices.Clone(d.udtaRaw[rStart:rEnd]), free...)
	delta := int64(len(free))

	lay := layout{
		regionStart: ups + rStart,
		regionEnd:   ups + rEnd,
		regionBytes: grown,
		freeOff:     -1,
	}
	out := resultUdtaRaw(d, lay)

	// Re-walk the spliced payload: the meta box must be self-consistent,
	// non-truncated, and still enclose the ilst.
	var meta *node
	nodes := walkUdta(out)
	for i := range nodes {
		if nodes[i].id() == "meta" {
			meta = &nodes[i]
			break
		}
	}
	if meta == nil {
		t.Fatal("no meta box after re-walking the spliced udta payload (stale/inconsistent sizes)")
	}
	if meta.truncated {
		t.Error("meta box re-walked as truncated: its size field overran the payload")
	}
	if meta.size != d.meta.size+delta {
		t.Errorf("meta size = %d, want %d (original %d + splice delta %d)", meta.size, d.meta.size+delta, d.meta.size, delta)
	}
	hasIlst := false
	for _, c := range meta.children {
		if c.id() == "ilst" {
			hasIlst = true
		}
	}
	if !hasIlst {
		t.Error("meta no longer encloses an ilst after the splice")
	}
}
