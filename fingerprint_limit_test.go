package waxlabel

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
)

// TestFingerprintLimit: save-time fingerprint uses the document's parse limit
// verbatim; non-positive falls back to default (else Fingerprint skips).
func TestFingerprintLimit(t *testing.T) {
	if got := (&Document{limits: Limits{}}).fingerprintLimit(); got != bits.DefaultLimits.MaxAllocBytes {
		t.Errorf("zero-limit fingerprintLimit() = %d, want the default fallback %d", got, bits.DefaultLimits.MaxAllocBytes)
	}
	// Positive limit used verbatim (tight sub-default must not floor up).
	for _, lim := range []int64{1024, bits.DefaultLimits.MaxAllocBytes * 4} {
		if got := (&Document{limits: Limits{MaxAllocBytes: lim}}).fingerprintLimit(); got != lim {
			t.Errorf("fingerprintLimit() with parse limit %d = %d, want it used verbatim", lim, got)
		}
	}
}
