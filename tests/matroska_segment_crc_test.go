package waxlabel_test

import (
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// segmentFirstChildIsVoid reports whether the first child of the Segment master is a Void
// element (id 0xEC) rather than a CRC-32 (id 0xBF).
func segmentFirstChildIsVoid(t *testing.T, b []byte) bool {
	t.Helper()
	_, ds, _, ok := elemRange(b, 0, len(b), testfile.IDSegment, nil)
	if !ok {
		t.Fatal("no Segment in output")
	}
	return b[ds] == 0xEC
}

// A CRC-32 directly under the Segment covers the whole segment body, so any edit makes it
// stale. The writer neutralizes it to a Void; the CRC is spec-optional, so the output stays
// valid without a whole-file recompute.
func TestMatroskaSegmentCRCDroppedToVoid(t *testing.T) {
	tags := testfile.El(testfile.IDTags, testfile.El(testfile.IDTag, concat(
		testfile.El(testfile.IDTargets, testfile.Uint(testfile.IDTgtTypeVal, 50)), testfile.SimpleTag("ARTIST", "AA"))))
	// Segment children: Info(title), a padding Void (so a small edit can absorb in place), an audio
	// Cluster, and Tags; wrapped in a leading CRC-32 covering the whole body.
	seg := concat(
		testfile.El(testfile.IDInfo, mkStr(testfile.IDSegTitle, "Old")),
		testfile.El(0xEC, make([]byte, 64)), // Void padding (id 0xEC) enabling the in-place absorb path
		testfile.AudioCluster(0),
		tags,
	)
	data := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, mkCRC(seg)))

	// The fixture carries a valid Segment CRC: the builder and the validator agree before any edit.
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

// A Segment-level CRC-32 whose declared size exceeds the alloc limit cannot be read for
// neutralization, so the parse refuses the file: no edit can copy the stale CRC over an edited
// body.
func TestMatroskaSegmentCRCOverLimitRefused(t *testing.T) {
	// A non-conformant Segment CRC declaring far more content (2000 bytes) than the 1 KiB parse
	// limit reads.
	bigCRC := testfile.El(idCRC32, make([]byte, 2000))
	seg := concat(bigCRC, testfile.El(testfile.IDInfo, mkStr(testfile.IDSegTitle, "Old")), testfile.AudioCluster(0),
		testfile.El(testfile.IDTags, testfile.El(testfile.IDTag, concat(testfile.El(testfile.IDTargets, testfile.Uint(testfile.IDTgtTypeVal, 50)), testfile.SimpleTag("ARTIST", "AA")))))
	data := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, seg))

	_, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxAllocBytes: 1024}))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("parse under a 1 KiB limit: err = %v, want declared size too large", err)
	}
}
