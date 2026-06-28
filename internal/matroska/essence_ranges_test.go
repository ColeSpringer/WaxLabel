package matroska

import (
	"bytes"
	"context"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// essenceDigest hashes the concatenation of data[r[0]:r[1]] for each range, the same
// byte-precise accumulation the audio-essence digest uses.
func essenceDigest(data []byte, ranges [][2]int64) []byte {
	h := bits.NewHasher(ranges)
	h.Observe(0, data)
	return h.Sum()
}

// interClusterMKA builds a Matroska file whose Tags element sits between two clusters,
// the layout that defeats a single-span essence extent. ARTIST is mid-stream, so editing
// it resizes an element inside [firstCluster, lastCluster).
func interClusterMKA(artist string) []byte {
	tags := encElement(idTags, encElement(idTag, cat(
		encElement(idTargets, uintElement(idTgtTypeVal, 50)),
		encElement(idSimpleTag, cat(
			stringElement(idTagName, "ARTIST"),
			stringElement(idTagString, artist),
		)),
	)))
	return segBytes(cat(mkInfo("Title"), emptyCluster(), tags, emptyCluster()))
}

// TestInterClusterEssenceExcludesMidStreamElement checks that Matroska essence digests hash
// only Cluster runs. A tag-only edit that resizes a level-1 element between clusters should
// keep the digest stable in both the source and rewritten result.
func TestInterClusterEssenceExcludesMidStreamElement(t *testing.T) {
	src := interClusterMKA("Old")
	base := parseMKA(t, src)

	// Parse side: two cluster runs, with the mid-stream Tags excluded between them.
	if len(base.AudioRanges) != 2 {
		t.Fatalf("source AudioRanges = %v, want 2 cluster runs (mid-stream Tags excluded)", base.AudioRanges)
	}
	if base.AudioRanges[0][1] >= base.AudioRanges[1][0] {
		t.Fatalf("cluster runs are not separated by the mid-stream element: %v", base.AudioRanges)
	}

	// A tag-only edit that grows the mid-stream Tags element.
	edited := base.Clone()
	edited.Tags.Set(tag.Artist, "A Much Longer Artist Value Than Before")
	plan, err := Codec{}.Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out := renderPlan(t, src, plan)
	re := parseMKA(t, out)

	// The multi-range essence digest is stable: the clusters were copied verbatim, so
	// hashing only the cluster runs gives the same sum before and after.
	srcDigest := essenceDigest(src, base.AudioRanges)
	outDigest := essenceDigest(out, re.AudioRanges)
	if !bytes.Equal(srcDigest, outDigest) {
		t.Errorf("cluster-run essence digest changed across a tag-only edit:\n src=%x\n out=%x", srcDigest, outDigest)
	}

	// A single-span extent [AudioStart, AudioEnd] includes the resized mid-stream Tags and
	// would not be stable.
	srcSpan := essenceDigest(src, [][2]int64{{base.AudioStart, base.AudioEnd}})
	outSpan := essenceDigest(out, [][2]int64{{re.AudioStart, re.AudioEnd}})
	if bytes.Equal(srcSpan, outSpan) {
		t.Error("single-span extent stayed stable; the inter-cluster element did not change")
	}
}
