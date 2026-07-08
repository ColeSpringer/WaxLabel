package waxlabel_test

import (
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// segmentFirstChildIsVoid reports whether the first child of the Segment master is a Void
// element (id 0xEC) rather than a CRC-32 (id 0xBF).
func segmentFirstChildIsVoid(t *testing.T, b []byte) bool {
	t.Helper()
	_, ds, _, ok := elemRange(b, 0, len(b), idSegment, nil)
	if !ok {
		t.Fatal("no Segment in output")
	}
	return b[ds] == 0xEC
}

// TestMatroskaSegmentCRCDroppedToVoid checks that a CRC-32 directly under the Segment covers the
// whole segment body, so any edit makes it stale. The writer must neutralize it to a Void (not
// copy the stale CRC), keeping the output valid (the CRC is spec-optional) without a whole-file
// recompute. checkCRCs recurses into the Segment master, so it would flag a stale Segment CRC;
// both the in-place absorb (layout) and the shift path are exercised.
func TestMatroskaSegmentCRCDroppedToVoid(t *testing.T) {
	tags := mkEl(idTags, mkEl(idTag, concat(
		mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "AA"))))
	// Segment children: Info(title), a padding Void (so a small edit can absorb in place), an
	// audio Cluster, and Tags - wrapped in a leading CRC-32 covering the whole body.
	seg := concat(
		mkEl(idInfo, mkStr(idSegTitle, "Old")),
		mkEl(0xEC, make([]byte, 64)), // Void padding (id 0xEC) enabling the in-place absorb path
		mkAudioCluster(),
		tags,
	)
	data := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, mkCRC(seg)))

	// The synthesized fixture itself carries a valid Segment CRC (sanity-checks the builder
	// and the validator agree before any edit).
	checkCRCs(t, data, 0, len(data), 0)
	if segmentFirstChildIsVoid(t, data) {
		t.Fatal("setup: fixture Segment should start with a CRC-32, not a Void")
	}

	cases := []struct {
		name string
		edit func() *wl.Editor
	}{
		{"absorb", func() *wl.Editor { return mustParseBytes(t, data).Edit().Set(tag.Title, "New") }},
		{"shift", func() *wl.Editor {
			return mustParseBytes(t, data).Edit().Set(tag.Artist, "A Much Longer Artist Forcing A Tail Shift")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := saveMatroska(t, data, tc.edit())
			// Would fail on a stale, copied Segment CRC: the edit changed the segment body.
			checkCRCs(t, out, 0, len(out), 0)
			if !segmentFirstChildIsVoid(t, out) {
				t.Error("Segment-level CRC was not neutralized to a Void (first child is not 0xEC)")
			}
			if _, err := wl.Parse(context.Background(), wl.BytesSource(out)); err != nil {
				t.Errorf("edited output failed to re-parse: %v", err)
			}
		})
	}
}

// TestMatroskaSegmentCRCUncapturableRefused covers the robustness gap: a Segment-level CRC-32
// whose declared size exceeds the alloc limit cannot be captured for neutralization, so an edit
// must refuse loudly (the same contract the index elements use) rather than copy the stale CRC
// over an edited body and silently produce an invalid file.
func TestMatroskaSegmentCRCUncapturableRefused(t *testing.T) {
	// A non-conformant Segment CRC declaring far more content (2000 bytes) than the 1 KiB parse
	// limit will read, so its capture fails and segVoidFromCRC stays nil.
	bigCRC := mkEl(idCRC32, make([]byte, 2000))
	seg := concat(bigCRC, mkEl(idInfo, mkStr(idSegTitle, "Old")), mkAudioCluster(),
		mkEl(idTags, mkEl(idTag, concat(mkEl(idTargets, mkUint(idTgtTypeVal, 50)), mkSimple("ARTIST", "AA")))))
	data := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, seg))

	doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: 1024}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := doc.Edit().Set(tag.Title, "New").Prepare(); !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Fatalf("edit of a file with an uncapturable Segment CRC: err = %v, want ErrUnsupportedTag (loud refusal, not a stale-CRC copy)", err)
	}
}
