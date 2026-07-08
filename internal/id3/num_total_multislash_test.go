package id3

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestNumTotalMultiSlashPreserved is a regression: a number field with two slashes
// ("1/2/3") is not a valid "n/total" value, so re-joining "1/<total>" would silently drop
// "/2/3". The prior code composed a *valid* "1/12" and lost the rest with no warning, while
// the pre-parse plan note kept it "as text" - a contradiction. It must now be written
// verbatim with the canonical total flagged dropped (surfacing value-dropped), matching the
// no-total path which already keeps "1/2/3". Covers both TRACK (TRCK) and DISC (TPOS).
func TestNumTotalMultiSlashPreserved(t *testing.T) {
	for _, kp := range []struct {
		name           string
		numKey, totKey tag.Key
		frameID        string
	}{
		{"track", tag.TrackNumber, tag.TrackTotal, "TRCK"},
		{"disc", tag.DiscNumber, tag.DiscTotal, "TPOS"},
	} {
		t.Run(kp.name+"/with total drops it", func(t *testing.T) {
			edited := tag.NewTagSet()
			edited.Set(kp.numKey, "1/2/3")
			edited.Set(kp.totKey, "12")
			out, info := RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{}, WriteOpts{})
			if got, ok := frameValue(out, kp.frameID); !ok || got != "1/2/3" {
				t.Errorf("%s = %q (ok=%v), want \"1/2/3\" (verbatim, not recomposed to 1/12)", kp.frameID, got, ok)
			}
			if !slices.Contains(info.DroppedTotals, kp.totKey) {
				t.Errorf("%s not flagged dropped (DroppedTotals=%v)", kp.totKey, info.DroppedTotals)
			}
			ws := AppendRebuildWarnings(nil, info, tag.NewTagSet())
			if !slices.ContainsFunc(ws, func(w core.Warning) bool {
				return w.Code == core.WarnValueDropped && slices.Contains(w.Keys, kp.totKey)
			}) {
				t.Errorf("no value-dropped warning keyed to %s (warnings=%v)", kp.totKey, ws)
			}
		})
		t.Run(kp.name+"/no total kept verbatim", func(t *testing.T) {
			edited := tag.NewTagSet()
			edited.Set(kp.numKey, "1/2/3")
			out, info := RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{}, WriteOpts{})
			if got, ok := frameValue(out, kp.frameID); !ok || got != "1/2/3" {
				t.Errorf("%s = %q (ok=%v), want \"1/2/3\" (verbatim)", kp.frameID, got, ok)
			}
			if len(info.DroppedTotals) != 0 {
				t.Errorf("no canonical total present, nothing should be dropped, got %v", info.DroppedTotals)
			}
		})
		// The empty-number guard: a lone total (no number) is cleanly representable as "/total"
		// and must round-trip, NOT be dropped like a non-numeric number. This pins the num!=""
		// condition so the multi-slash guard never regresses a bare --set TRACKTOTAL=N.
		t.Run(kp.name+"/lone total preserved as /total", func(t *testing.T) {
			edited := tag.NewTagSet()
			edited.Set(kp.totKey, "12") // total only, no number
			out, info := RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{}, WriteOpts{})
			if got, ok := frameValue(out, kp.frameID); !ok || got != "/12" {
				t.Errorf("%s = %q (ok=%v), want \"/12\" (lone total preserved, not dropped)", kp.frameID, got, ok)
			}
			if slices.Contains(info.DroppedTotals, kp.totKey) {
				t.Errorf("a lone total must not be flagged dropped (DroppedTotals=%v)", info.DroppedTotals)
			}
		})
	}
}
