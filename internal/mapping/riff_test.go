package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestRIFFTrackNumberAliases checks that IPRT and ITRK both read as TrackNumber,
// while the write mapping stays deterministic by choosing IPRT.
func TestRIFFTrackNumberAliases(t *testing.T) {
	for _, id := range []string{"IPRT", "ITRK"} {
		k, ok := RIFFInfoKey(id)
		if !ok || k != tag.TrackNumber {
			t.Errorf("RIFFInfoKey(%q) = %s,%v, want TrackNumber,true", id, k, ok)
		}
	}
	if id, ok := RIFFKeyInfo(tag.TrackNumber); !ok || id != "IPRT" {
		t.Errorf("RIFFKeyInfo(TrackNumber) = %q,%v, want IPRT,true", id, ok)
	}
}
