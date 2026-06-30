package waxlabel_test

import (
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// Seek child IDs (idSeekHead lives in matroska_write_test.go).
const (
	idSeek         = 0x4DBB
	idSeekID       = 0x53AB
	idSeekPosition = 0x53AC
)

// minWidthVINT encodes n as the shortest valid EBML data-size VINT. The shared sizeVINT
// helper always emits 8 bytes, so it cannot exercise width-changing Segment sizes. The
// all-ones value at a given width is the reserved unknown-size form, so a value needing every
// bit rolls to the next width.
func minWidthVINT(n int) []byte {
	v := uint64(n)
	for w := 1; w <= 8; w++ {
		if v < uint64(1)<<(7*w)-1 {
			b := make([]byte, w)
			for i := w - 1; i >= 0; i-- {
				b[i] = byte(v)
				v >>= 8
			}
			b[0] |= 0x80 >> (w - 1) // width marker bit
			return b
		}
	}
	return sizeVINT(n) // unreachable for test-sized bodies
}

// mkSegmentMinWidth wraps body in a Segment whose data-size VINT is minimal width.
func mkSegmentMinWidth(body []byte) []byte {
	return concat(idToBytes(idSegment), minWidthVINT(len(body)), body)
}

// segVINTWidth returns the byte width of the Segment data-size VINT in a Matroska file.
func segVINTWidth(t *testing.T, data []byte) int {
	t.Helper()
	_, idn, ok := readVint(data, 0, true) // EBML header element
	if !ok {
		t.Fatal("bad EBML id")
	}
	sz, szn, ok := readVint(data, idn, false)
	if !ok {
		t.Fatal("bad EBML size")
	}
	segOff := idn + szn + int(sz)
	id, sidn, ok := readVint(data, segOff, true)
	if !ok || id != idSegment {
		t.Fatalf("expected Segment at %d, got %#x", segOff, id)
	}
	_, w, ok := readVint(data, segOff+sidn, false)
	if !ok {
		t.Fatal("bad Segment size VINT")
	}
	return w
}

// assertSegmentVINTWidened fails unless the edit grew the Segment size VINT's width, which
// is the precondition for exercising updated Segment geometry.
func assertSegmentVINTWidened(t *testing.T, src, out []byte) {
	t.Helper()
	if sw, ow := segVINTWidth(t, src), segVINTWidth(t, out); ow <= sw {
		t.Fatalf("Segment size VINT width did not grow (src %d, out %d); the edit never crossed the boundary", sw, ow)
	}
}

// buildMinSegMultiCluster is buildMultiClusterMKA's trailing-Cues layout wrapped in a
// minimal-width Segment VINT. A body-growing edit widens both the Segment header and cue
// slots.
func buildMinSegMultiCluster(t *testing.T, title string) []byte {
	t.Helper()
	info := mkEl(idInfo, mkStr(idSegTitle, title))
	tracks := mkEl(idTracks, mkEl(idTrackEntry, concat(
		mkUint(idTrackType, 2), // audio
		mkStr(idCodecID, "A_PCM/INT/LIT"),
	)))
	pre := len(info) + len(tracks)
	c0, c1, c2 := mkAudioClusterTS(0), mkAudioClusterTS(1), mkAudioClusterTS(2)
	mkCues := func(o0, o1, o2 int) []byte {
		return mkEl(idCues, concat(mkCuePointAt(0, o0), mkCuePointAt(1, o1), mkCuePointAt(2, o2)))
	}
	o0 := pre
	o1, o2 := o0+len(c0), o0+len(c0)+len(c1)
	if o2 > 0xFFFF {
		t.Fatalf("setup: cluster offset %d exceeds the 2-byte cue slot before any edit", o2)
	}
	body := concat(info, tracks, c0, c1, c2, mkCues(o0, o1, o2))
	return concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkSegmentMinWidth(body))
}

// TestMatroskaChainedEditAcrossSegmentVINTWidthCues checks chained edits after a Segment
// size VINT widens. The returned in-memory document must carry updated Segment geometry so a
// second edit can rebuild cue positions without a reparse.
func TestMatroskaChainedEditAcrossSegmentVINTWidthCues(t *testing.T) {
	src := buildMinSegMultiCluster(t, "x")
	if got := mustParseBytes(t, src).Fields().Title; got != "x" {
		t.Fatalf("source title = %q, want x", got)
	}

	// A 70 KB title pushes the body past the 2-byte Segment VINT (widening it) and past
	// every 2-byte cue slot at once.
	out1, doc1 := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Title, strings.Repeat("T", 70000)))
	assertSegmentVINTWidened(t, src, out1)
	assertAllCuesPointAtClusters(t, out1, true)

	// The second edit runs on the returned document without reparsing it first.
	out2, _ := saveMatroska(t, out1, doc1.Edit().Set(tag.Artist, "A2"))
	assertAllCuesPointAtClusters(t, out2, true)
	essenceUnchanged(t, src, out2)
	if got := mustParseBytes(t, out2).Fields().Artists; len(got) != 1 || got[0] != "A2" {
		t.Errorf("re-edited artists = %v, want [A2]", got)
	}
}

// buildMinSegSeekHeadMKA builds a minimal-Segment-VINT file whose SeekHead indexes Info and
// Tags. A title edit grows Info, moves Tags, and widens the Segment VINT, forcing the
// SeekHead to be rebuilt with updated positions.
func buildMinSegSeekHeadMKA(t *testing.T, title string) []byte {
	t.Helper()
	info := mkEl(idInfo, mkStr(idSegTitle, title))
	cluster := mkAudioCluster()
	tags := mkEl(idTags, mkEl(idTag, concat(mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "AA"))))

	seek := func(oInfo, oTags int) []byte {
		entry := func(id uint64, pos int) []byte {
			return mkEl(idSeek, concat(mkEl(idSeekID, idToBytes(id)), mkUint(idSeekPosition, uint64(pos))))
		}
		return mkEl(idSeekHead, concat(entry(idInfo, oInfo), entry(idTags, oTags)))
	}
	seekLen := len(seek(0, 0)) // fixed 8-byte position slots: length is value-independent
	oInfo := seekLen
	oTags := seekLen + len(info) + len(cluster)
	body := concat(seek(oInfo, oTags), info, cluster, tags)
	return concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkSegmentMinWidth(body))
}

// assertSeekEntriesResolve checks that every segment-relative SeekPosition points at a
// top-level element whose ID equals the entry's SeekID.
func assertSeekEntriesResolve(t *testing.T, data []byte) {
	t.Helper()
	_, segData, segEnd, ok := elemRange(data, 0, len(data), idSegment, nil)
	if !ok {
		t.Fatal("no Segment in output")
	}
	_, shData, shEnd, ok := elemRange(data, segData, segEnd, idSeekHead, nil)
	if !ok {
		t.Fatal("no SeekHead in output")
	}
	readBE := func(start, end int) uint64 {
		var v uint64
		for i := start; i < end; i++ {
			v = v<<8 | uint64(data[i])
		}
		return v
	}
	found := 0
	for off := shData; off < shEnd; {
		id, idn, ok := readVint(data, off, true)
		if !ok {
			t.Fatal("bad VINT in SeekHead")
		}
		sz, szn, _ := readVint(data, off+idn, false)
		ds := off + idn + szn
		de := ds + int(sz)
		if de > shEnd {
			de = shEnd
		}
		if id == idSeek {
			var seekID uint64
			pos := -1
			for c := ds; c < de; {
				cid, cidn, ok := readVint(data, c, true)
				if !ok {
					break
				}
				csz, cszn, _ := readVint(data, c+cidn, false)
				cds := c + cidn + cszn
				cde := cds + int(csz)
				switch cid {
				case idSeekID:
					seekID = readBE(cds, cde)
				case idSeekPosition:
					pos = int(readBE(cds, cde))
				}
				c = cde
			}
			target := segData + pos
			gotID, _, ok := readVint(data, target, true)
			if pos < 0 || !ok || gotID != seekID {
				t.Errorf("Seek #%d: position %d (+segData %d = %d) does not point at element %#x (found %#x, ok=%v)",
					found, pos, segData, target, seekID, gotID, ok)
			}
			found++
		}
		off = de
	}
	if found == 0 {
		t.Error("no Seek entries in the output SeekHead")
	}
}

// TestMatroskaChainedEditAcrossSegmentVINTWidthSeekHead is the SeekHead variant of the
// chained edit check. After a width-changing first edit, a second edit on the returned
// document must rebuild SeekHead positions that still resolve.
func TestMatroskaChainedEditAcrossSegmentVINTWidthSeekHead(t *testing.T) {
	src := buildMinSegSeekHeadMKA(t, "x")
	if got := mustParseBytes(t, src).Fields().Title; got != "x" {
		t.Fatalf("source title = %q, want x", got)
	}
	assertSeekEntriesResolve(t, src) // the synthetic source is itself consistent

	out1, doc1 := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Title, strings.Repeat("T", 70000)))
	assertSegmentVINTWidened(t, src, out1)
	assertSeekEntriesResolve(t, out1)

	out2, _ := saveMatroska(t, out1, doc1.Edit().Set(tag.Artist, "A2"))
	assertSeekEntriesResolve(t, out2)
	essenceUnchanged(t, src, out2)
	if got := mustParseBytes(t, out2).Fields().Artists; len(got) != 1 || got[0] != "A2" {
		t.Errorf("re-edited artists = %v, want [A2]", got)
	}
}
