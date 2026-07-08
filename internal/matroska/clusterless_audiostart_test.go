package matroska

import (
	"context"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestAbsorbClusterlessReportsNoAudioStart is a regression: for a clusterless
// (audio-less) segment with trailing bytes, the absorb write path once reported
// AudioStart = segDataEnd (a nonzero scalar) while a fresh parse and the shift path reported 0 -
// AudioRanges was empty in every case, so only the informational scalar disagreed. buildResult now
// gates the audio extent purely on cluster runs, so a segment with no clusters reports no audio
// extent, matching parse. The trailing bytes make clusterStart (== segDataEnd) < size, the exact
// geometry that fed the old scalar; the +4 Title edit fits the reserved Void so the absorb path is
// taken (not shift).
func TestAbsorbClusterlessReportsNoAudioStart(t *testing.T) {
	void := encElement(idVoid, make([]byte, 40)) // reserved Void so the small edit absorbs in place
	seg := segBytes(cat(mkInfo("Title"), void))
	src := append(append([]byte{}, seg...), 0x00, 0x00, 0x00, 0x00) // trailing bytes after the segment

	base := parseMKA(t, src)
	d := base.Native.(*doc)
	// Preconditions: no clusters (so a fresh parse reports no audio extent), and clusterStart < size
	// (trailing bytes) - the exact shape that produced the old nonzero absorb-path scalar.
	if base.AudioStart != 0 || len(base.AudioRanges) != 0 {
		t.Fatalf("setup: parse of a clusterless segment reported AudioStart=%d ranges=%v, want 0 / none", base.AudioStart, base.AudioRanges)
	}
	if !(d.wb.clusterStart < d.wb.size) {
		t.Fatalf("setup: need clusterStart(%d) < size(%d) to exercise the fixed branch", d.wb.clusterStart, d.wb.size)
	}

	edited := base.Clone()
	edited.Tags.Set(tag.Title, "TitleABCD") // +4 bytes, fits the 40-byte Void
	plan, err := Codec{}.Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Guard the test against silently taking the shift path (which also reports 0): the shift path
	// appends an "N-byte tail shift" operation, so its absence confirms the absorb path ran.
	for _, op := range plan.Report.Operations {
		if strings.Contains(op, "shift") {
			t.Fatalf("expected the absorb path, but a tail shift ran: %v", plan.Report.Operations)
		}
	}

	// The absorb result must report no audio extent, consistent with the parse side.
	if plan.Result.AudioStart != 0 || len(plan.Result.AudioRanges) != 0 {
		t.Errorf("absorb result AudioStart=%d ranges=%v, want 0 / none (a clusterless segment has no audio extent)",
			plan.Result.AudioStart, plan.Result.AudioRanges)
	}
	// And a fresh parse of the written bytes agrees.
	re := parseMKA(t, renderPlan(t, src, plan))
	if re.AudioStart != 0 || len(re.AudioRanges) != 0 {
		t.Errorf("re-parse AudioStart=%d ranges=%v, want 0 / none", re.AudioStart, re.AudioRanges)
	}
}
