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

// Cue child IDs used to hand-build narrow-slot Cues fixtures. The tests/ synth
// helpers emit only 8-byte VINTs, so a 2-byte CueClusterPosition slot - exactly the
// case a width-crossing edit must widen - can only be constructed here, against the
// real encoder.
const (
	idCueTime   = 0xB3
	idCueTrack  = 0xF7
	idCueRelPos = 0xF0
)

// cueTrackPosBytes builds one CueTrackPositions: a CueTrack (its "pre" child), a
// CueClusterPosition stored at the given byte width, and a trailing
// CueRelativePosition (its "post" child) so the rebuild's verbatim pre/post handling
// is exercised - and proven to survive in order.
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

// TestRebuildCuesToleratesVoid: Void padding (legal EBML, emitted by muxers that
// reserve Cues space) at the Cues, CuePoint, and CueTrackPositions levels must not
// poison capturedOK - otherwise the feature's own target operation, embedding a cover
// that crosses a width boundary, is refused. Void is dropped on rebuild, matching the
// SeekHead path (which emits only its captured entries).
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
	if !ci.capturedOK {
		t.Fatal("Void padding must not poison capturedOK")
	}
	if len(ci.clusters) != 1 || len(ci.points) != 1 || len(ci.points[0].tracks) != 1 {
		t.Fatalf("capture: clusters=%d points=%d", len(ci.clusters), len(ci.points))
	}
	out, _, ok := rebuildCues(ci, map[int64]int64{872: 70000}, 0, 0)
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

	if ci.capturedOK {
		t.Error("a non-leading CRC-32 should mark the capture unrebuildable")
	}
	if _, _, ok := rebuildCues(ci, map[int64]int64{872: 70000}, 0, 0); ok {
		t.Error("rebuildCues should refuse a non-leading CRC")
	}
}

// TestCuesCaptureFidelity confirms cuesFromRaw populates both the flat fast-path
// list and the rebuild tree, splitting each CueTrackPositions at its position.
func TestCuesCaptureFidelity(t *testing.T) {
	raw := cuesBytes(true, cuePointBytes(0, cueTrackPosBytes(1, 872, 2)))
	ci := parseCues(t, raw)

	if !ci.capturedOK {
		t.Fatal("capturedOK = false for a well-formed Cues")
	}
	if len(ci.clusters) != 1 || ci.clusters[0].target != 872 {
		t.Fatalf("flat clusters = %+v, want one target 872", ci.clusters)
	}
	if len(ci.points) != 1 || len(ci.points[0].tracks) != 1 {
		t.Fatalf("points = %+v, want one point with one track", ci.points)
	}
	if ci.crc == nil {
		t.Error("CRC not captured")
	}
	tp := ci.points[0].tracks[0]
	if !tp.hasPos || tp.target != 872 {
		t.Errorf("track target = %d hasPos=%v, want 872/true", tp.target, tp.hasPos)
	}
	if len(tp.pre) == 0 || len(tp.post) == 0 {
		t.Errorf("pre/post not captured: pre=%d post=%d bytes (CueTrack/CueRelativePosition)", len(tp.pre), len(tp.post))
	}
}

// TestRebuildCuesMultiTrack rebuilds a single CuePoint carrying two
// CueTrackPositions (the nested case no fixture exercises), pushing both clusters
// past the 2-byte boundary, and asserts both positions widen and the verbatim
// pre/post children survive in order.
func TestRebuildCuesMultiTrack(t *testing.T) {
	raw := cuesBytes(true, cuePointBytes(0,
		cueTrackPosBytes(1, 872, 2),
		cueTrackPosBytes(2, 900, 2)))
	ci := parseCues(t, raw)
	if !ci.capturedOK || len(ci.clusters) != 2 {
		t.Fatalf("capture: capturedOK=%v clusters=%d", ci.capturedOK, len(ci.clusters))
	}

	out, _, ok := rebuildCues(ci, map[int64]int64{872: 70000, 900: 80000}, 0, 0)
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
	if !ci2.capturedOK || len(ci2.points) != 1 || len(ci2.points[0].tracks) != 2 {
		t.Errorf("rebuilt re-parse: capturedOK=%v points=%+v", ci2.capturedOK, ci2.points)
	}
}

// TestRebuildCuesMultiCluster rebuilds several CuePoints, asserting every position
// is re-pointed (not just the first) and each CueTime prefix survives.
func TestRebuildCuesMultiCluster(t *testing.T) {
	raw := cuesBytes(true,
		cuePointBytes(0, cueTrackPosBytes(1, 872, 2)),
		cuePointBytes(1000, cueTrackPosBytes(1, 5000, 2)))
	ci := parseCues(t, raw)
	if !ci.capturedOK || len(ci.points) != 2 {
		t.Fatalf("capture: capturedOK=%v points=%d", ci.capturedOK, len(ci.points))
	}

	out, _, ok := rebuildCues(ci, map[int64]int64{872: 70000, 5000: 130000}, 0, 0)
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

// TestRebuildCuesDroppedTarget: a CueTrackPositions whose target left oldToNew is
// omitted, and a CuePoint left with no tracks disappears - while the rebuild still
// succeeds for the survivors.
func TestRebuildCuesDroppedTarget(t *testing.T) {
	raw := cuesBytes(false,
		cuePointBytes(0, cueTrackPosBytes(1, 872, 2), cueTrackPosBytes(2, 900, 2)),
		cuePointBytes(1000, cueTrackPosBytes(1, 5000, 2)))
	ci := parseCues(t, raw)

	// Keep only cluster 872: track 900 (one track of point 1) and all of point 2 drop.
	out, _, ok := rebuildCues(ci, map[int64]int64{872: 70000}, 0, 0)
	if !ok {
		t.Fatal("rebuildCues failed (a partial survivor should still succeed)")
	}
	if got := collectCueUint(t, out, idCueClusterPos); !slices.Equal(got, []uint64{70000}) {
		t.Errorf("positions = %v, want [70000] (dropped targets omitted)", got)
	}
	ci2 := parseCues(t, out)
	if len(ci2.points) != 1 || len(ci2.points[0].tracks) != 1 {
		t.Errorf("rebuilt = %+v, want one point with one surviving track", ci2.points)
	}
}

// TestRebuildCuesEmptyRefused: when every target is dropped the result would be an
// empty (invalid) Cues, which is refused so the caller surfaces overflowErr.
func TestRebuildCuesEmptyRefused(t *testing.T) {
	ci := parseCues(t, cuesBytes(true, cuePointBytes(0, cueTrackPosBytes(1, 872, 2))))
	if _, _, ok := rebuildCues(ci, map[int64]int64{}, 0, 0); ok {
		t.Error("rebuildCues should refuse to emit an empty Cues")
	}
}

// TestRebuildCuesUncaptured: a CueTrackPositions with no CueClusterPosition cannot
// be modeled, so capture marks the index unrebuildable and both rebuildCues and a
// forced makeCues refuse rather than emit a wrong index.
func TestRebuildCuesUncaptured(t *testing.T) {
	bad := encElement(idCueTrackPos, encElement(idCueTrack, uintData(1))) // no position
	raw := cuesBytes(true, encElement(idCuePoint, append(uintElement(idCueTime, 0), bad...)))
	ci := parseCues(t, raw)

	if ci.capturedOK {
		t.Fatal("capturedOK should be false for a position-less CueTrackPositions")
	}
	if _, _, ok := rebuildCues(ci, map[int64]int64{}, 0, 0); ok {
		t.Error("rebuildCues should refuse an uncaptured tree")
	}
	wb := &writeBase{cues: ci}
	if _, _, _, ok := makeCues(wb, 0, map[int64]int64{}, 0, true); ok {
		t.Error("makeCues(force) should report ok=false for an uncaptured Cues")
	}
}

// TestRebuildCuesCRCPresence: the rebuilt Cues carries a CRC iff the source did
// (mkvmerge writes one; the WebM fixtures do not). The recomputed value's validity
// is covered end-to-end by checkCRCs in the integration test.
func TestRebuildCuesCRCPresence(t *testing.T) {
	for _, crc := range []bool{true, false} {
		ci := parseCues(t, cuesBytes(crc, cuePointBytes(0, cueTrackPosBytes(1, 872, 2))))
		out, _, ok := rebuildCues(ci, map[int64]int64{872: 70000}, 0, 0)
		if !ok {
			t.Fatalf("rebuild (crc=%v) failed", crc)
		}
		if got := parseCues(t, out).crc != nil; got != crc {
			t.Errorf("crc=%v: rebuilt CRC presence = %v", crc, got)
		}
	}
}

// TestCuesCaptureDepthTruncated: when the depth bound truncates the child walk
// before the positions are reached (eachChild's only error), the capture must mark
// itself unrebuildable rather than emit a partial tree - so the caller refuses the
// rebuild instead of writing an index missing entries.
func TestCuesCaptureDepthTruncated(t *testing.T) {
	raw := cuesBytes(true, cuePointBytes(0, cueTrackPosBytes(1, 872, 2)))
	// A budget too small to reach CueClusterPosition (Cues > CuePoint > CueTrackPositions).
	ci := cuesFromRaw(raw, 0, bits.NewDepth(2), maxElement)
	if ci == nil {
		t.Fatal("cuesFromRaw returned nil")
	}
	if ci.capturedOK {
		t.Error("capturedOK should be false when the depth bound truncates the walk")
	}
	if _, _, ok := rebuildCues(ci, map[int64]int64{872: 70000}, 0, 0); ok {
		t.Error("rebuildCues should refuse a depth-truncated capture")
	}
}

// TestShiftConvergence drives the real resolve fixpoint with a cover large enough to
// push the single cluster past the 2-byte boundary (forcing BOTH the Cues and
// SeekHead rebuild paths) and asserts it settles in a small, bounded number of
// iterations. This fix fails by refusing a legal edit (hitting the iter<=8 ceiling),
// not by crashing, so the bound is the regression guard.
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
		t.Errorf("iters = %d, expected >=2 (the cover should force a rebuild, not just an in-place patch)", iters)
	}
	if iters > 4 {
		t.Errorf("iters = %d, expected the fixpoint to settle in <=4", iters)
	}
	if lay.size <= int64(len(src)) {
		t.Errorf("layout size %d did not grow past original %d", lay.size, len(src))
	}
}
