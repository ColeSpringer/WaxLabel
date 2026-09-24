package matroska

import (
	"context"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestAbsorbClusterlessReportsNoAudioStart checks that the absorb path reports no
// audio extent for a clusterless segment with trailing bytes, matching a fresh parse
// and the shift path.
func TestAbsorbClusterlessReportsNoAudioStart(t *testing.T) {
	void := encElement(idVoid, make([]byte, 40)) // reserved Void so the small edit absorbs in place
	seg := segBytes(cat(mkInfo("Title"), void))
	src := append(append([]byte{}, seg...), 0x00, 0x00, 0x00, 0x00) // trailing bytes after the segment

	base := parseMKA(t, src)
	d := base.Native.(*doc)
	// Preconditions: no clusters, and clusterStart < size (trailing bytes).
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
	// The shift path also reports 0, so confirm absorb ran: only shift appends an
	// "N-byte tail shift" operation.
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
