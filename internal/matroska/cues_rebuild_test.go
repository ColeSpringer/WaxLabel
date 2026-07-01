package matroska

import (
	"bytes"
	"context"
	"os"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
)

// Cue child IDs used to build narrow-slot Cues fixtures. The shared test helpers
// emit canonical element sizes, so these tests construct 2-byte CueClusterPosition
// slots directly against the real encoder.
const (
	idCueTime   = 0xB3
	idCueTrack  = 0xF7
	idCueRelPos = 0xF0
)

// cueTrackPosBytes builds one CueTrackPositions with CueTrack before the position
// and CueRelativePosition after it, so rebuild tests verify pre/post children
// survive in order.
func cueTrackPosBytes(track int, pos uint64, width int) []byte {
	body := encElement(idCueTrack, uintData(uint64(track)))
	body = append(body, encElement(idCueClusterPos, uintDataWidth(pos, width))...)
	body = append(body, uintElement(idCueRelPos, 7)...)
	return encElement(idCueTrackPos, body)
}

// cuePointBytes builds one CuePoint: a CueTime (its "prefix") then its tracks.
func cuePointBytes(timeVal uint64, tracks ...[]byte) []byte {
	body := uintElement(idCueTime, timeVal)
	for _, tr := range tracks {
		body = append(body, tr...)
	}
	return encElement(idCuePoint, body)
}

// cuesBytes assembles a Cues element from CuePoints, optionally CRC-guarded.
func cuesBytes(crc bool, points ...[]byte) []byte {
	return masterElement(idCues, bytes.Join(points, nil), crc)
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func parseCues(t *testing.T, raw []byte) *cuesIndex {
	t.Helper()
	ci := cuesFromRaw(raw, 0, bits.NewDepth(64), maxElement)
	if ci == nil {
		t.Fatal("cuesFromRaw returned nil")
	}
	return ci
}

// collectCueUint returns, in document order, the values of every element with id
// want within a Cues byte slice (descending CuePoint/CueTrackPositions).
func collectCueUint(t *testing.T, cues []byte, want uint64) []uint64 {
	t.Helper()
	rs := core.BytesSource(cues)
	var out []uint64
	var walk func(s, e int64)
	walk = func(s, e int64) {
		_ = eachChild(rs, s, e, bits.NewDepth(64), maxElement, func(c element) error {
			if c.id == want {
				out = append(out, readUint(rs, c, maxElement))
			}
			switch c.id {
			case idCues, idCuePoint, idCueTrackPos:
				walk(c.dataStart, c.dataEnd)
			}
			return nil
		})
	}
	walk(0, int64(len(cues)))
	return out
}

// countCueElem counts elements with id want within a Cues byte slice (descending
// CuePoint/CueTrackPositions).
func countCueElem(t *testing.T, cues []byte, want uint64) int {
	t.Helper()
	rs := core.BytesSource(cues)
	n := 0
	var walk func(s, e int64)
	walk = func(s, e int64) {
		_ = eachChild(rs, s, e, bits.NewDepth(64), maxElement, func(c element) error {
			if c.id == want {
				n++
			}
			switch c.id {
			case idCues, idCuePoint, idCueTrackPos:
				walk(c.dataStart, c.dataEnd)
			}
			return nil
		})
	}
	walk(0, int64(len(cues)))
	return n
}

// TestRebuildCuesToleratesVoid covers legal EBML padding at the Cues, CuePoint,
// and CueTrackPositions levels. Rebuild drops Void padding, as SeekHead rebuild
// does, but padding must not make an otherwise faithful Cues unrebuildable.
func TestRebuildCuesToleratesVoid(t *testing.T) {
	void := encElement(idVoid, make([]byte, 8))
	tp := encElement(idCueTrackPos, cat(
		encElement(idCueTrack, uintData(1)),
		void, // padding between CueTrack and the position
		encElement(idCueClusterPos, uintDataWidth(872, 2)),
		uintElement(idCueRelPos, 7),
	))
	point := encElement(idCuePoint, cat(uintElement(idCueTime, 0), tp, void))
	raw := cuesBytes(true, point, void)

	ci := parseCues(t, raw)
	points, ok := buildCuePoints(ci)
	if !ok {
		t.Fatal("Void padding must not make the capture unrebuildable")
	}
	if len(ci.clusters) != 1 || len(points) != 1 || len(points[0].tracks) != 1 {
		t.Fatalf("capture: clusters=%d points=%d", len(ci.clusters), len(points))
	}
	out, _, ok := rebuildCues(ci, directOffsetMap(map[int64]int64{872: 70000}), 0, 0)
	if !ok {
		t.Fatal("rebuildCues refused a Void-padded Cues")
	}
	if got := collectCueUint(t, out, idCueClusterPos); !slices.Equal(got, []uint64{70000}) {
		t.Errorf("positions = %v, want [70000]", got)
	}
	if n := countCueElem(t, out, idVoid); n != 0 {
		t.Errorf("rebuilt Cues still carries %d Void element(s); padding should be dropped", n)
	}
	// The meaningful pre/post children survive the rebuild.
	if got := collectCueUint(t, out, idCueTrack); !slices.Equal(got, []uint64{1}) {
		t.Errorf("CueTrack = %v, want [1]", got)
	}
	if got := collectCueUint(t, out, idCueRelPos); !slices.Equal(got, []uint64{7}) {
		t.Errorf("CueRelativePosition = %v, want [7]", got)
	}
}

// TestRebuildCuesRefusesNonLeadingCRC: a CRC-32 that is not the leading child (so
// hasCRC, which is leading-only, cannot reproduce it) marks the capture unrebuildable
// rather than silently dropping the integrity guard on rebuild.
func TestRebuildCuesRefusesNonLeadingCRC(t *testing.T) {
	crc := []byte{idCRC32 & 0xFF, 0x84, 0, 0, 0, 0} // a well-formed CRC element, mis-placed
	point := encElement(idCuePoint, cat(uintElement(idCueTime, 0), cueTrackPosBytes(1, 872, 2), crc))
	ci := parseCues(t, cuesBytes(false, point))

	if _, ok := buildCuePoints(ci); ok {
		t.Error("a non-leading CRC-32 should make the capture unrebuildable")
	}
	if _, _, ok := rebuildCues(ci, directOffsetMap(map[int64]int64{872: 70000}), 0, 0); ok {
		t.Error("rebuildCues should refuse a non-leading CRC")
	}
}

// TestCuesCaptureFidelity confirms the parse populates the flat fast-path list and
// the lazy buildCuePoints walk produces the rebuild tree, splitting each
// CueTrackPositions at its position.
func TestCuesCaptureFidelity(t *testing.T) {
	raw := cuesBytes(true, cuePointBytes(0, cueTrackPosBytes(1, 872, 2)))
	ci := parseCues(t, raw)

	points, ok := buildCuePoints(ci)
	if !ok {
		t.Fatal("buildCuePoints ok = false for a well-formed Cues")
	}
	if len(ci.clusters) != 1 || ci.clusters[0].target != 872 {
		t.Fatalf("flat clusters = %+v, want one target 872", ci.clusters)
	}
	if len(points) != 1 || len(points[0].tracks) != 1 {
		t.Fatalf("points = %+v, want one point with one track", points)
	}
	if ci.crc == nil {
		t.Error("CRC not captured")
	}
	tp := points[0].tracks[0]
	if !tp.hasPos || tp.target != 872 {
		t.Errorf("track target = %d hasPos=%v, want 872/true", tp.target, tp.hasPos)
	}
	if len(tp.pre) == 0 || len(tp.post) == 0 {
		t.Errorf("pre/post not captured: pre=%d post=%d bytes (CueTrack/CueRelativePosition)", len(tp.pre), len(tp.post))
	}
}

// TestRebuildCuesMultiTrack rebuilds a single CuePoint carrying two
// CueTrackPositions. Both positions cross the 2-byte boundary, and the test checks
// that the verbatim pre/post children survive in order.
func TestRebuildCuesMultiTrack(t *testing.T) {
	raw := cuesBytes(true, cuePointBytes(0,
		cueTrackPosBytes(1, 872, 2),
		cueTrackPosBytes(2, 900, 2)))
	ci := parseCues(t, raw)
	if pts, ok := buildCuePoints(ci); !ok || len(ci.clusters) != 2 {
		t.Fatalf("capture: ok=%v clusters=%d points=%d", ok, len(ci.clusters), len(pts))
	}

	out, _, ok := rebuildCues(ci, directOffsetMap(map[int64]int64{872: 70000, 900: 80000}), 0, 0)
	if !ok {
		t.Fatal("rebuildCues failed")
	}
	if got := collectCueUint(t, out, idCueClusterPos); !slices.Equal(got, []uint64{70000, 80000}) {
		t.Errorf("positions = %v, want [70000 80000]", got)
	}
	if got := collectCueUint(t, out, idCueTrack); !slices.Equal(got, []uint64{1, 2}) {
		t.Errorf("CueTrack (pre) = %v, want [1 2]", got)
	}
	if got := collectCueUint(t, out, idCueRelPos); !slices.Equal(got, []uint64{7, 7}) {
		t.Errorf("CueRelativePosition (post) = %v, want [7 7]", got)
	}
	ci2 := parseCues(t, out)
	if pts, ok := buildCuePoints(ci2); !ok || len(pts) != 1 || len(pts[0].tracks) != 2 {
		t.Errorf("rebuilt re-parse: ok=%v points=%+v", ok, pts)
	}
}

// TestRebuildCuesMultiCluster rebuilds several CuePoints, asserting every position
// is repointed rather than only the first and each CueTime prefix survives.
func TestRebuildCuesMultiCluster(t *testing.T) {
	raw := cuesBytes(true,
		cuePointBytes(0, cueTrackPosBytes(1, 872, 2)),
		cuePointBytes(1000, cueTrackPosBytes(1, 5000, 2)))
	ci := parseCues(t, raw)
	if pts, ok := buildCuePoints(ci); !ok || len(pts) != 2 {
		t.Fatalf("capture: ok=%v points=%d", ok, len(pts))
	}

	out, _, ok := rebuildCues(ci, directOffsetMap(map[int64]int64{872: 70000, 5000: 130000}), 0, 0)
	if !ok {
		t.Fatal("rebuildCues failed")
	}
	if got := collectCueUint(t, out, idCueClusterPos); !slices.Equal(got, []uint64{70000, 130000}) {
		t.Errorf("positions = %v, want [70000 130000]", got)
	}
	if got := collectCueUint(t, out, idCueTime); !slices.Equal(got, []uint64{0, 1000}) {
		t.Errorf("CueTime (prefix) = %v, want [0 1000]", got)
	}
}

// TestRebuildCuesDroppedTarget verifies that a CueTrackPositions whose target is
// absent from oldToNew is omitted, and that a CuePoint with no remaining tracks
// disappears while the surviving entries still rebuild.
func TestRebuildCuesDroppedTarget(t *testing.T) {
	raw := cuesBytes(false,
		cuePointBytes(0, cueTrackPosBytes(1, 872, 2), cueTrackPosBytes(2, 900, 2)),
		cuePointBytes(1000, cueTrackPosBytes(1, 5000, 2)))
	ci := parseCues(t, raw)

	// Keep only cluster 872: track 900 (one track of point 1) and all of point 2 drop.
	out, _, ok := rebuildCues(ci, directOffsetMap(map[int64]int64{872: 70000}), 0, 0)
	if !ok {
		t.Fatal("rebuildCues failed (a partial survivor should still succeed)")
	}
	if got := collectCueUint(t, out, idCueClusterPos); !slices.Equal(got, []uint64{70000}) {
		t.Errorf("positions = %v, want [70000] (dropped targets omitted)", got)
	}
	ci2 := parseCues(t, out)
	if pts, ok := buildCuePoints(ci2); !ok || len(pts) != 1 || len(pts[0].tracks) != 1 {
		t.Errorf("rebuilt = %+v, want one point with one surviving track", pts)
	}
}

// TestRebuildCuesEmptyRefused: when every target is dropped the result would be an
// empty (invalid) Cues, which is refused so the caller surfaces overflowErr.
func TestRebuildCuesEmptyRefused(t *testing.T) {
	ci := parseCues(t, cuesBytes(true, cuePointBytes(0, cueTrackPosBytes(1, 872, 2))))
	if _, _, ok := rebuildCues(ci, directOffsetMap(map[int64]int64{}), 0, 0); ok {
		t.Error("rebuildCues should refuse to emit an empty Cues")
	}
}

// TestRebuildCuesUncaptured: a CueTrackPositions with no CueClusterPosition cannot
// be modeled, so buildCuePoints reports the tree unrebuildable and both rebuildCues
// and a forced shiftIndex.emit refuse rather than emit a wrong index.
func TestRebuildCuesUncaptured(t *testing.T) {
	bad := encElement(idCueTrackPos, encElement(idCueTrack, uintData(1))) // no position
	raw := cuesBytes(true, encElement(idCuePoint, append(uintElement(idCueTime, 0), bad...)))
	ci := parseCues(t, raw)

	if _, ok := buildCuePoints(ci); ok {
		t.Fatal("buildCuePoints should be unrebuildable for a position-less CueTrackPositions")
	}
	if _, _, ok := rebuildCues(ci, directOffsetMap(map[int64]int64{}), 0, 0); ok {
		t.Error("rebuildCues should refuse an uncaptured tree")
	}
	// Exercise the same refusal through the write machinery by forcing the rebuild
	// path and skipping the in-place patch.
	cues := buildShiftIndexes(&writeBase{cues: ci}, -1, 0)[1] // [0]=SeekHead (absent), [1]=Cues
	cues.force = true
	if _, _, _, ok := cues.emit(directOffsetMap(map[int64]int64{}), 0, 0); ok {
		t.Error("a forced shiftIndex.emit should report ok=false for an uncaptured Cues")
	}
}

// TestRebuildCuesCRCPresence: the rebuilt Cues carries a CRC iff the source did
// (mkvmerge writes one; the WebM fixtures do not). The recomputed value's validity
// is covered end-to-end by checkCRCs in the integration test.
func TestRebuildCuesCRCPresence(t *testing.T) {
	for _, crc := range []bool{true, false} {
		ci := parseCues(t, cuesBytes(crc, cuePointBytes(0, cueTrackPosBytes(1, 872, 2))))
		out, _, ok := rebuildCues(ci, directOffsetMap(map[int64]int64{872: 70000}), 0, 0)
		if !ok {
			t.Fatalf("rebuild (crc=%v) failed", crc)
		}
		if got := parseCues(t, out).crc != nil; got != crc {
			t.Errorf("crc=%v: rebuilt CRC presence = %v", crc, got)
		}
	}
}

// TestCuesCaptureDepthTruncated verifies that a depth-limited walk refuses a
// rebuild before it can produce a partial tree. The parse stores ci.maxDepth so
// the lazy walk uses the same budget family as the original capture.
func TestCuesCaptureDepthTruncated(t *testing.T) {
	raw := cuesBytes(true, cuePointBytes(0, cueTrackPosBytes(1, 872, 2)))
	// A budget too small to reach CueClusterPosition (Cues > CuePoint > CueTrackPositions).
	ci := cuesFromRaw(raw, 0, bits.NewDepth(2), maxElement)
	if ci == nil {
		t.Fatal("cuesFromRaw returned nil")
	}
	if _, ok := buildCuePoints(ci); ok {
		t.Error("buildCuePoints should refuse when the depth bound truncates the walk")
	}
	if _, _, ok := rebuildCues(ci, directOffsetMap(map[int64]int64{872: 70000}), 0, 0); ok {
		t.Error("rebuildCues should refuse a depth-truncated capture")
	}
}

// TestShiftConvergence drives resolveShiftLayout with a cover large enough to push
// the single cluster past the 2-byte boundary, forcing both Cues and SeekHead
// rebuilds. The test asserts the fixpoint settles quickly rather than hitting the
// iteration bound and refusing a legal edit.
func TestShiftConvergence(t *testing.T) {
	src, err := os.ReadFile("../../testdata/sample.mka")
	if err != nil {
		t.Fatal(err)
	}
	base, err := parse(context.Background(), core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	edited := base.Clone()
	edited.Pictures = []core.Picture{{Type: core.PicFrontCover, MIME: "image/png", Data: bytes.Repeat([]byte{0xAB}, 200000)}}

	d := edited.Native.(*doc)
	ch := detectChanges(base, edited)
	if !ch.pictures {
		t.Fatal("setup: picture change not detected")
	}
	r, err := renderChanged(d, base, edited, ch, editedKeySet(base.Tags, edited.Tags))
	if err != nil {
		t.Fatalf("renderChanged: %v", err)
	}
	items, seekIdx, cuesIdx := buildShiftItems(d.wb, ch, r)
	if seekIdx < 0 || cuesIdx < 0 {
		t.Fatalf("expected both SeekHead and Cues present (seekIdx=%d cuesIdx=%d)", seekIdx, cuesIdx)
	}

	lay, iters, ok := resolveShiftLayout(d.wb, items, seekIdx, cuesIdx)
	if !ok {
		t.Fatal("resolveShiftLayout did not converge")
	}
	if iters < 2 {
		t.Errorf("iters = %d, expected >=2 (the cover should force a rebuild, not only an in-place patch)", iters)
	}
	if iters > 4 {
		t.Errorf("iters = %d, expected the fixpoint to settle in <=4", iters)
	}
	if lay.size <= int64(len(src)) {
		t.Errorf("layout size %d did not grow past original %d", lay.size, len(src))
	}
}

// synthLargeCues builds a Cues with n CuePoints, each with one CueTrackPositions
// and a 4-byte CueClusterPosition. At this size, per-CuePoint cost dominates.
func synthLargeCues(n int) []byte {
	points := make([][]byte, n)
	for i := range points {
		points[i] = cuePointBytes(uint64(i), cueTrackPosBytes(1, uint64(i)*100+1, 4))
	}
	return cuesBytes(true, points...)
}

// BenchmarkCuesParseAlloc measures the parse cost of capturing a large Cues. The
// intended parse path keeps only the flat cluster-offset list and does not retain
// the nested rebuild tree or verbatim per-child byte copies.
func BenchmarkCuesParseAlloc(b *testing.B) {
	raw := synthLargeCues(20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if cuesFromRaw(raw, 0, bits.NewDepth(64), maxElement) == nil {
			b.Fatal("cuesFromRaw returned nil")
		}
	}
}

// TestCuesParseAllocBudget keeps the parse path from eagerly building the rebuild
// tree. For an N-CuePoint Cues, allocation count should stay near the flat
// clusters walk, currently about 13 allocations per CuePoint, and well below the
// eager tree path at about 34 per CuePoint. The 20-per-CuePoint budget leaves room
// for runtime variation while catching the extra structured walk and verbatim child
// copies.
func TestCuesParseAllocBudget(t *testing.T) {
	const n = 4000
	raw := synthLargeCues(n)
	avg := testing.AllocsPerRun(3, func() {
		if cuesFromRaw(raw, 0, bits.NewDepth(64), maxElement) == nil {
			t.Fatal("cuesFromRaw returned nil")
		}
	})
	if budget := float64(20 * n); avg > budget {
		t.Errorf("parse allocs/run = %.0f for %d CuePoints, want <= %.0f (eager rebuild tree reintroduced?)", avg, n, budget)
	}
}

// Nested CRC-32 elements are valid in CuePoint, CueTrackPositions, and Seek. The
// in-place patch paths only recompute the containing master CRC, so parsing a
// nested CRC must force a rebuild.
//
// validateNestedCRCs walks Cues and SeekHead descendants. For each master with a
// leading CRC-32 child, it recomputes the CRC over the remaining content and
// compares it with the stored element. The returned count lets callers confirm
// that the fixture actually contained CRCs.
func validateNestedCRCs(t *testing.T, raw []byte) int {
	t.Helper()
	rs := core.BytesSource(raw)
	checked := 0
	var walk func(start, end int64)
	walk = func(start, end int64) {
		_ = eachChild(rs, start, end, bits.NewDepth(64), maxElement, func(c element) error {
			switch c.id {
			case idCues, idCuePoint, idCueTrackPos, idSeekHead, idSeek:
				if first, ok := readElement(rs, c.dataStart, c.dataEnd, maxElement); ok &&
					first.id == idCRC32 && first.dataEnd-first.dataStart == 4 {
					stored, _ := bits.ReadSlice(rs, first.start, first.dataEnd-first.start, maxElement)
					content, _ := bits.ReadSlice(rs, first.dataEnd, c.dataEnd-first.dataEnd, maxElement)
					// Compare against the production encoder's rendering so the test's notion of
					// a correct CRC element cannot drift from the write path.
					if want := crcElement(content); !bytes.Equal(stored, want) {
						t.Errorf("stale CRC on element %#x: stored % x, want % x", c.id, stored, want)
					}
					checked++
				}
				walk(c.dataStart, c.dataEnd)
			}
			return nil
		})
	}
	walk(0, int64(len(raw)))
	return checked
}

// TestCuesFromRawDetectsNestedCRC checks that a leading CRC on CuePoint or
// CueTrackPositions sets hasNestedCRC, while a Cues master CRC alone does not.
func TestCuesFromRawDetectsNestedCRC(t *testing.T) {
	pointCRC := encElement(idCuePoint, withCRC(cat(uintElement(idCueTime, 0), cueTrackPosBytes(1, 872, 2))))
	if ci := parseCues(t, cuesBytes(true, pointCRC)); !ci.hasNestedCRC {
		t.Error("a leading CuePoint CRC was not detected as nested")
	}
	tpCRC := encElement(idCueTrackPos, withCRC(cat(
		encElement(idCueTrack, uintData(1)),
		encElement(idCueClusterPos, uintDataWidth(872, 2)),
	)))
	if ci := parseCues(t, cuesBytes(true, encElement(idCuePoint, cat(uintElement(idCueTime, 0), tpCRC)))); !ci.hasNestedCRC {
		t.Error("a leading CueTrackPositions CRC was not detected as nested")
	}
	if ci := parseCues(t, cuesBytes(true, cuePointBytes(0, cueTrackPosBytes(1, 872, 2)))); ci.hasNestedCRC {
		t.Error("a master-only Cues CRC was wrongly flagged nested (would needlessly force rebuild)")
	}
}

// TestSeekFromRawDetectsNestedCRC checks that a per-Seek leading CRC sets
// hasNestedCRC, while a SeekHead master CRC alone does not.
func TestSeekFromRawDetectsNestedCRC(t *testing.T) {
	seekCRC := encElement(idSeek, withCRC(cat(
		encElement(idSeekID, idBytes(idCues)),
		encElement(idSeekPosition, uintDataWidth(100, 8)),
	)))
	if sh := seekFromRaw(masterElement(idSeekHead, seekCRC, true), 0, bits.NewDepth(64), maxElement); sh == nil || !sh.hasNestedCRC {
		t.Fatalf("a per-Seek leading CRC was not detected as nested: %+v", sh)
	}
	plain := seekFromRaw(masterElement(idSeekHead, cat(seekFor(idCues, 100), seekFor(idTags, 200)), true), 0, bits.NewDepth(64), maxElement)
	if plain == nil || plain.hasNestedCRC {
		t.Error("a master-only SeekHead CRC was wrongly flagged nested")
	}
}

// TestBuildShiftIndexesForcesRebuildOnNestedCRC checks that the parse-time
// hasNestedCRC flag latches shiftIndex.force, sending emit to rebuild.
func TestBuildShiftIndexesForcesRebuildOnNestedCRC(t *testing.T) {
	ciPlain := parseCues(t, cuesBytes(true, cuePointBytes(0, cueTrackPosBytes(1, 872, 2))))
	if buildShiftIndexes(&writeBase{cues: ciPlain}, -1, 0)[1].force {
		t.Error("a plain Cues should not force rebuild")
	}
	ciNested := parseCues(t, cuesBytes(true, encElement(idCuePoint, withCRC(cat(uintElement(idCueTime, 0), cueTrackPosBytes(1, 872, 2))))))
	if !buildShiftIndexes(&writeBase{cues: ciNested}, -1, 0)[1].force {
		t.Error("a nested-CRC Cues should force rebuild")
	}
	shNested := seekFromRaw(masterElement(idSeekHead, encElement(idSeek, withCRC(cat(
		encElement(idSeekID, idBytes(idCues)),
		encElement(idSeekPosition, uintDataWidth(100, 8))))), true), 0, bits.NewDepth(64), maxElement)
	if !buildShiftIndexes(&writeBase{seek: shNested}, 0, -1)[0].force {
		t.Error("a nested-CRC SeekHead should force rebuild")
	}
}

// TestRebuildCuesRecomputesNestedCRCs covers a Cues master CRC with nested
// CuePoint and CueTrackPositions CRCs. A forced rebuild should repoint the
// cluster and recompute every CRC element.
func TestRebuildCuesRecomputesNestedCRCs(t *testing.T) {
	tp := encElement(idCueTrackPos, withCRC(cat(
		encElement(idCueTrack, uintData(1)),
		encElement(idCueClusterPos, uintDataWidth(872, 2)),
		uintElement(idCueRelPos, 7),
	)))
	raw := cuesBytes(true, encElement(idCuePoint, withCRC(cat(uintElement(idCueTime, 0), tp))))
	if n := validateNestedCRCs(t, raw); n != 3 {
		t.Fatalf("source validated %d CRCs, want 3 (Cues, CuePoint, CueTrackPositions)", n)
	}

	ci := parseCues(t, raw)
	if !ci.hasNestedCRC {
		t.Fatal("nested CRC not detected")
	}
	out, _, ok := rebuildCues(ci, directOffsetMap(map[int64]int64{872: 70000}), 0, 0)
	if !ok {
		t.Fatal("rebuildCues refused a nested-CRC Cues")
	}
	if got := collectCueUint(t, out, idCueClusterPos); !slices.Equal(got, []uint64{70000}) {
		t.Errorf("position = %v, want [70000]", got)
	}
	if n := validateNestedCRCs(t, out); n != 3 {
		t.Errorf("rebuilt validated %d CRCs, want 3 (master + both nested, all recomputed)", n)
	}
}

// TestRebuildCuesNestedCRCIdempotent checks that a rebuilt Cues still parses as
// having nested CRCs, so the next edit rebuilds again instead of using the
// in-place path. The second rebuild should converge byte-identically.
func TestRebuildCuesNestedCRCIdempotent(t *testing.T) {
	raw := cuesBytes(true, encElement(idCuePoint, withCRC(cat(uintElement(idCueTime, 0), cueTrackPosBytes(1, 872, 2)))))
	out1, _, ok := rebuildCues(parseCues(t, raw), directOffsetMap(map[int64]int64{872: 70000}), 0, 0)
	if !ok {
		t.Fatal("first rebuild failed")
	}
	ci2 := parseCues(t, out1)
	if !ci2.hasNestedCRC {
		t.Fatal("the rebuilt Cues lost its nested CRC flag; a second edit would fast-path a stale CRC")
	}
	out2, _, ok := rebuildCues(ci2, directOffsetMap(map[int64]int64{70000: 70000}), 0, 0)
	if !ok {
		t.Fatal("second rebuild failed")
	}
	if !bytes.Equal(out1, out2) {
		t.Error("rebuild is not idempotent on a nested-CRC Cues")
	}
	if n := validateNestedCRCs(t, out2); n != 2 {
		t.Errorf("re-rebuilt validated %d CRCs, want 2 (Cues master + nested CuePoint)", n)
	}
}

// TestRebuildCuesNestedCRCUnrebuildableRefused checks that a nested CRC on an
// unrebuildable Cues tree forces rebuild, and that emit returns ok=false instead
// of writing an in-place result with stale nested CRCs.
func TestRebuildCuesNestedCRCUnrebuildableRefused(t *testing.T) {
	badPoint := encElement(idCuePoint, withCRC(uintElement(idCueTime, 0))) // no CueTrackPositions
	raw := cuesBytes(true, badPoint, cuePointBytes(1000, cueTrackPosBytes(1, 872, 2)))
	ci := parseCues(t, raw)
	if !ci.hasNestedCRC {
		t.Fatal("nested CRC not detected on the zero-track CuePoint")
	}
	cues := buildShiftIndexes(&writeBase{cues: ci}, -1, 0)[1]
	if !cues.force {
		t.Fatal("nested CRC did not force rebuild")
	}
	if _, _, _, ok := cues.emit(directOffsetMap(map[int64]int64{872: 70000}), 0, 0); ok {
		t.Error("a forced emit on an unrebuildable nested-CRC Cues must refuse (ok=false), not write stale")
	}
}

// TestRebuildSeekHeadDropsPerSeekCRC checks that SeekHead rebuild re-encodes
// each Seek without an optional per-Seek CRC, preserves the entry, and
// recomputes the master CRC.
func TestRebuildSeekHeadDropsPerSeekCRC(t *testing.T) {
	seekCRC := encElement(idSeek, withCRC(cat(
		encElement(idSeekID, idBytes(idCues)),
		encElement(idSeekPosition, uintDataWidth(200, 8)),
	)))
	sh := seekFromRaw(masterElement(idSeekHead, seekCRC, true), 0, bits.NewDepth(64), maxElement)
	if sh == nil || !sh.hasNestedCRC {
		t.Fatalf("per-Seek CRC not detected: %+v", sh)
	}
	out, _, ok := rebuildSeekHead(sh, directOffsetMap(map[int64]int64{200: 200}), 0, 0)
	if !ok {
		t.Fatal("rebuildSeekHead failed")
	}
	sh2 := seekFromRaw(out, 0, bits.NewDepth(64), maxElement)
	if sh2 == nil {
		t.Fatal("re-parse of rebuilt SeekHead failed")
	}
	if sh2.hasNestedCRC {
		t.Error("the rebuilt SeekHead still carries a per-Seek CRC; it should be dropped")
	}
	if len(sh2.entries) != 1 || sh2.entries[0].target != 200 {
		t.Errorf("rebuilt entry = %+v, want one targeting 200", sh2.entries)
	}
	if n := validateNestedCRCs(t, out); n != 1 {
		t.Errorf("rebuilt validated %d CRCs, want 1 (the SeekHead master CRC, recomputed)", n)
	}
}

// TestPatchSeekAbsorbFallsBackOnNestedCRC checks that the absorb fast path
// refuses a SeekHead with a nested per-Seek CRC, while a plain SeekHead still
// patches in place.
func TestPatchSeekAbsorbFallsBackOnNestedCRC(t *testing.T) {
	nested := seekFromRaw(masterElement(idSeekHead, encElement(idSeek, withCRC(cat(
		encElement(idSeekID, idBytes(idCues)),
		encElement(idSeekPosition, uintDataWidth(200, 8))))), true), 0, bits.NewDepth(64), maxElement)
	if nested == nil || !nested.hasNestedCRC {
		t.Fatal("per-Seek CRC not detected")
	}
	if _, ok := patchSeekAbsorb(nested, 0, map[int64]int64{}, map[int64]bool{}); ok {
		t.Error("patchSeekAbsorb should fall back (ok=false) when the SeekHead has a nested per-Seek CRC")
	}
	plain := seekFromRaw(masterElement(idSeekHead, seekFor(idCues, 200), true), 0, bits.NewDepth(64), maxElement)
	if plain == nil {
		t.Fatal("plain SeekHead parse failed")
	}
	if _, ok := patchSeekAbsorb(plain, 0, map[int64]int64{}, map[int64]bool{}); !ok {
		t.Error("patchSeekAbsorb should still succeed in place for a plain SeekHead")
	}
}
