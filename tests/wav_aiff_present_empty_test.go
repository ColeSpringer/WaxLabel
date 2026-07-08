package waxlabel_test

import (
	"path/filepath"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestWAVAIFFPresentEmptyRoundTripIdempotent is the library-level regression: WAV INFO items
// (a size-1 NUL for an empty ZSTR) and AIFF text chunks (genuinely zero-length) now store a
// present-empty value in their native chunk, so `--set COPYRIGHT=` reads back present-empty
// (['']) like every other format, and re-applying the same edit is a byte-stable no-op.
func TestWAVAIFFPresentEmptyRoundTripIdempotent(t *testing.T) {
	for _, fx := range []string{notagsWAV, notagsAIFF} {
		t.Run(filepath.Base(fx), func(t *testing.T) {
			data := readFixture(t, fx)
			plan, err := mustParseBytes(t, data).Edit().Set(tag.Copyright, "").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if plan.IsNoOp() {
				t.Fatal("setting an absent key to present-empty is a real change, not a no-op")
			}
			re := mustParseBytes(t, applyToBytes(t, data, plan))
			if v, ok := re.Get(tag.Copyright); !ok || len(v) != 1 || v[0] != "" {
				t.Fatalf("present-empty COPYRIGHT = %v (ok=%v), want [\"\"] (native chunk stores it)", v, ok)
			}
			// Re-applying the same present-empty value is a stable no-op.
			p2, err := re.Edit().Set(tag.Copyright, "").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if !p2.IsNoOp() {
				t.Errorf("re-applying present-empty must be a no-op; operations: %v", p2.Report().Operations)
			}
			if out2 := applyToBytes(t, applyToBytes(t, data, plan), p2); len(out2) == 0 {
				t.Error("re-apply produced no output")
			}
		})
	}
}
