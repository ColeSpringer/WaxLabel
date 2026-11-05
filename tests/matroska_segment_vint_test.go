package waxlabel_test

import (
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// Seek child IDs (idSeekHead lives in matroska_write_test.go).
const (
	idSeek         = 0x4DBB
	idSeekID       = 0x53AB
	idSeekPosition = 0x53AC
)

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
	if !ok || id != testfile.IDSegment {
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

// checks chained edits after a Segment size VINT widens.
func TestMatroskaChainedEditAcrossSegmentVINTWidthCues(t *testing.T) {
	src := testfile.MultiCluster(false, true)
	if got := mustParseBytes(t, src).Fields().Title; got != "x" {
		t.Fatalf("source title = %q, want x", got)
	}

	// A 70 KB title pushes the body past the 2-byte Segment VINT (widening it) and past
	// every 2-byte cue slot at once.
	out1, doc1 := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Title, strings.Repeat("T", 70000)))
	assertSegmentVINTWidened(t, src, out1)
	assertAllCuesPointAtClusters(t, out1, true)

	// The second edit starts from the Document the first write returned.
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
	info := testfile.El(testfile.IDInfo, mkStr(testfile.IDSegTitle, title))
	cluster := testfile.AudioCluster(0)
	tags := testfile.El(testfile.IDTags, testfile.El(testfile.IDTag, concat(testfile.El(testfile.IDTargets, testfile.Uint(testfile.IDTgtTypeVal, 50)), testfile.SimpleTag("ARTIST", "AA"))))

	seek := func(oInfo, oTags int) []byte {
		entry := func(id uint64, pos int) []byte {
			return testfile.El(idSeek, concat(testfile.El(idSeekID, testfile.IDBytes(id)), testfile.Uint(idSeekPosition, uint64(pos))))
		}
		return testfile.El(idSeekHead, concat(entry(testfile.IDInfo, oInfo), entry(testfile.IDTags, oTags)))
	}
	seekLen := len(seek(0, 0)) // fixed 8-byte position slots: length is value-independent
	oInfo := seekLen
	oTags := seekLen + len(info) + len(cluster)
	body := concat(seek(oInfo, oTags), info, cluster, tags)
	return concat(testfile.EBMLHeader("matroska"), testfile.MinWidthSegment(body))
}

// assertSeekEntriesResolve: each SeekPosition points at a top-level element matching SeekID.
func assertSeekEntriesResolve(t *testing.T, data []byte) {
	t.Helper()
	_, segData, segEnd, ok := elemRange(data, 0, len(data), testfile.IDSegment, nil)
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
		de := min(ds+int(sz), shEnd)
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

// SeekHead variant of the chained edit check. After a width-changing first edit, a second edit on
// the returned document must rebuild SeekHead positions that still resolve.
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
