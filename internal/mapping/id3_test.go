package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestID3TXXXKeyTCMP is the L3 regression: ffmpeg's TXXX:TCMP user frame folds onto canonical
// COMPILATION (case- and whitespace-insensitive), matching the dedicated TCMP text frame.
func TestID3TXXXKeyTCMP(t *testing.T) {
	for _, desc := range []string{"TCMP", "tcmp", " Tcmp "} {
		if k, ok := ID3TXXXKey(desc); !ok || k != tag.Compilation {
			t.Errorf("ID3TXXXKey(%q) = %q, %v; want COMPILATION, true", desc, k, ok)
		}
	}
	// An unlisted description stays a custom key, not COMPILATION.
	if k, ok := ID3TXXXKey("SOMETHINGELSE"); !ok || k == tag.Compilation {
		t.Errorf("ID3TXXXKey(unlisted) = %q, %v; want a custom key (not COMPILATION)", k, ok)
	}
}
