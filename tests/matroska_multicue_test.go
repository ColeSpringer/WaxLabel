package waxlabel_test

import (
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// collectSegChildren appends the absolute start of every Segment child with id want.
func collectSegChildren(data []byte, start, end int, want uint64, out *[]int) {
	off := start
	for off < end {
		id, idn, ok := readVint(data, off, true)
		if !ok {
			return
		}
		size, szn, ok := readVint(data, off+idn, false)
		if !ok {
			return
		}
		ds := off + idn + szn
		de := ds + int(size)
		if de > end || de < ds {
			de = end
		}
		if id == want {
			*out = append(*out, off)
		}
		if de <= off {
			return
		}
		off = de
	}
}

// collectCuePositions appends every CueClusterPosition value (segment-relative), in
// document order, descending the Cues tree.
func collectCuePositions(data []byte, start, end int, out *[]int64) {
	off := start
	for off < end {
		id, idn, ok := readVint(data, off, true)
		if !ok {
			return
		}
		size, szn, ok := readVint(data, off+idn, false)
		if !ok {
			return
		}
		ds := off + idn + szn
		de := ds + int(size)
		if de > end || de < ds {
			de = end
		}
		switch id {
		case testfile.IDCues, testfile.IDCuePoint, testfile.IDCueTrackPos:
			collectCuePositions(data, ds, de, out)
		case idCueClusterPos:
			var v int64
			for i := ds; i < de; i++ {
				v = v<<8 | int64(data[i])
			}
			*out = append(*out, v)
		}
		if de <= off {
			return
		}
		off = de
	}
}

// assertAllCuesPointAtClusters: every CueClusterPosition resolves, in order, to its Cluster start.
func assertAllCuesPointAtClusters(t *testing.T, data []byte, wantCross bool) {
	t.Helper()
	_, segData, segEnd, ok := elemRange(data, 0, len(data), testfile.IDSegment, nil)
	if !ok {
		t.Fatal("no Segment in output")
	}
	var clusters []int
	collectSegChildren(data, segData, segEnd, testfile.IDCluster, &clusters)
	var positions []int64
	collectCuePositions(data, segData, segEnd, &positions)

	if len(clusters) == 0 || len(positions) == 0 {
		t.Fatalf("found %d clusters, %d cue positions", len(clusters), len(positions))
	}
	if len(clusters) != len(positions) {
		t.Fatalf("cue count %d != cluster count %d (a cue was lost or duplicated)", len(positions), len(clusters))
	}
	crossed := false
	for i := range positions {
		if positions[i] > 0xFFFF {
			crossed = true
		}
		if int64(segData)+positions[i] != int64(clusters[i]) {
			t.Errorf("cue[%d] = %d (+segDataStart %d = %d) does not point at Cluster[%d] @%d",
				i, positions[i], segData, int64(segData)+positions[i], i, clusters[i])
		}
	}
	if wantCross && !crossed {
		t.Error("no CueClusterPosition crossed the 2-byte boundary; the edit didn't exercise the rebuild")
	}
	if !wantCross && crossed {
		t.Error("a CueClusterPosition crossed the 2-byte boundary; the small edit was meant to stay on the in-place patch path")
	}
}

// A 3-cluster file through the shift pipeline: every cue, not just the first, is repointed to
// its cluster.
func TestMatroskaMultiClusterRebuildsAllCues(t *testing.T) {
	for _, frontCues := range []bool{false, true} {
		topology := "trailing-cues"
		if frontCues {
			topology = "leading-cues"
		}
		edits := []struct {
			name      string
			title     string
			wantCross bool
		}{
			// A short title keeps every cluster offset inside its 2-byte slot, so the
			// positions are patched in place with no width change.
			{"in-place-patch", strings.Repeat("T", 40), false},
			// A 70 KB title pushes every cluster past 65535, forcing all three 2-byte
			// slots to widen to 3 bytes through the minimal-width Cues rebuild.
			{"width-rebuild", strings.Repeat("T", 70000), true},
		}
		for _, ed := range edits {
			t.Run(topology+"/"+ed.name, func(t *testing.T) {
				src := testfile.MultiCluster(frontCues, false)
				// The synthetic source parses and its cues are valid before any edit.
				if got := mustParseBytes(t, src).Fields().Title; got != "x" {
					t.Fatalf("source title = %q, want x", got)
				}

				out, outDoc := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Title, ed.title))
				if outDoc.Fields().Title != ed.title {
					t.Error("returned doc title not set")
				}
				if got := mustParseBytes(t, out).Fields().Title; got != ed.title {
					t.Errorf("reparsed title length = %d, want %d", len(got), len(ed.title))
				}

				assertAllCuesPointAtClusters(t, out, ed.wantCross)
				essenceUnchanged(t, src, out)

				// A second edit on the returned document must still resolve every cue over the
				// coalesced run.
				out2, _ := saveMatroska(t, out, outDoc.Edit().Set(tag.Artist, "A2"))
				assertAllCuesPointAtClusters(t, out2, ed.wantCross)
				essenceUnchanged(t, src, out2)
			})
		}
	}
}
