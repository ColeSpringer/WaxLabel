package mp3

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
)

// TestCapabilitiesOriginalDateVersion (C1): ORIGINALDATE is reported lossy onto a v2.3
// MP3 (TORY stores the year only) and lossless onto v2.4 (TDOR keeps the full date), so
// a transfer onto a v2.3 destination grades the truncation Lossy instead of claiming it
// carried. The file-less query (nil media, the PlanTransfer simulation) does not panic
// and assumes the MP3 default of v2.3.
func TestCapabilitiesOriginalDateVersion(t *testing.T) {
	codec := New()
	originalDateWrite := func(m *core.Media) core.AccessLevel {
		return codec.Capabilities(m, core.DefaultWriteOptions()).Field(tag.OriginalDate).Write
	}

	mediaWithVersion := func(v byte) *core.Media {
		return &core.Media{Format: core.FormatMP3, Native: &doc{id3: id3.NewEmpty(v)}}
	}

	if w := originalDateWrite(mediaWithVersion(3)); w != core.AccessPartial {
		t.Errorf("v2.3 ORIGINALDATE write = %v, want AccessPartial (lossy: TORY year-only)", w)
	}
	if w := originalDateWrite(mediaWithVersion(4)); w != core.AccessFull {
		t.Errorf("v2.4 ORIGINALDATE write = %v, want AccessFull (lossless: TDOR)", w)
	}
	// nil media (file-less PlanTransfer): no panic, defaults to v2.3, so lossy.
	if w := originalDateWrite(nil); w != core.AccessPartial {
		t.Errorf("nil-file ORIGINALDATE write = %v, want AccessPartial (v2.3 default)", w)
	}

	// Other date fields keep their precision in v2.3, so they are not downgraded.
	for _, k := range []tag.Key{tag.RecordingDate, tag.ReleaseDate} {
		if w := codec.Capabilities(mediaWithVersion(3), core.DefaultWriteOptions()).Field(k).Write; w != core.AccessFull {
			t.Errorf("v2.3 %s write = %v, want AccessFull (only ORIGINALDATE is downgraded)", k, w)
		}
	}
}
