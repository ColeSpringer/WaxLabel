package mp4

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestFreeformCaseFoldCollisionFlagsConflict pins the deliberate tradeoff of the read-side case
// fold (MP4FreeformKey): when a file carries the same MusicBrainz field twice under different
// casing (two taggers) with disagreeing values, both now resolve to one canonical key. No value is
// dropped - both survive as contributions - but because they arrive under two distinct native
// sources (the verbatim casing is kept in Source), BuildFamilies marks the key unselected: a
// surfaced conflict where the pre-fold read hid the foreign-cased atom from the canonical view
// entirely. The foreign spelling and the two-separate-atoms structure normalize on the next write;
// the values do not. Pinned so the merge is a decision, not an accident.
func TestFreeformCaseFoldCollisionFlagsConflict(t *testing.T) {
	canonical := decodeItem(freeformItem("MusicBrainz Album Id", []string{"AAA"}))
	foreign := decodeItem(freeformItem("musicbrainz album id", []string{"BBB"}))

	if !canonical.owned || !foreign.owned {
		t.Fatalf("both freeform atoms should be owned; got canonical=%v foreign=%v", canonical.owned, foreign.owned)
	}
	contribs := append(canonical.contribs, foreign.contribs...)
	if len(contribs) != 2 {
		t.Fatalf("want 2 contributions (no value dropped), got %d: %+v", len(contribs), contribs)
	}
	for _, c := range contribs {
		if c.Key != tag.MBReleaseID {
			t.Errorf("contribution key = %q, want MBReleaseID (both casings fold to one key)", c.Key)
		}
	}
	// Distinct native sources (verbatim casing kept in Source) => BuildFamilies sees a conflict.
	if contribs[0].Source == contribs[1].Source {
		t.Errorf("sources should stay distinct (verbatim casing): both %q", contribs[0].Source)
	}
	fams := core.BuildFamilies(contribs, core.FamilyMP4)
	if len(fams) != 1 {
		t.Fatalf("want 1 family entry for the merged key, got %d", len(fams))
	}
	if fams[0].Selected {
		t.Error("two case-variant atoms with disagreeing values must surface as an unselected conflict")
	}
	if len(fams[0].Values) != 2 {
		t.Errorf("both values must be preserved in the family view, got %v", fams[0].Values)
	}
}
