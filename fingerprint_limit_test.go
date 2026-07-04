package waxlabel

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
)

// TestFingerprintLimit locks in the save-time fingerprint limit: the document's own parse
// limit is used verbatim (symmetric with the parse-time fingerprint and honoring a caller's
// explicit WithLimits cap), falling back to the default only when the recorded limit is
// non-positive - a hand-constructed Document with no resolved limit, whose zero MaxAllocBytes
// would otherwise make core.Fingerprint skip silently (bits.ReadSlice rejects a non-positive
// limit) and degrade save-back change detection to inode+size+mtime.
func TestFingerprintLimit(t *testing.T) {
	if got := (&Document{limits: Limits{}}).fingerprintLimit(); got != bits.DefaultLimits.MaxAllocBytes {
		t.Errorf("zero-limit fingerprintLimit() = %d, want the default fallback %d", got, bits.DefaultLimits.MaxAllocBytes)
	}
	// A positive limit is used verbatim - whether a deliberately tight sub-default cap (which
	// must NOT be floored up to the default, or the fingerprint would allocate past the cap and
	// diverge from a fresh parse) or an elevated one.
	for _, lim := range []int64{1024, bits.DefaultLimits.MaxAllocBytes * 4} {
		if got := (&Document{limits: Limits{MaxAllocBytes: lim}}).fingerprintLimit(); got != lim {
			t.Errorf("fingerprintLimit() with parse limit %d = %d, want it used verbatim", lim, got)
		}
	}
}
