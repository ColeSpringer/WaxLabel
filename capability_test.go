package waxlabel_test

import (
	"testing"

	_ "github.com/colespringer/waxlabel" // registers every codec (init side effect)
	"github.com/colespringer/waxlabel/internal/core"
)

// TestCodecCapabilitiesNilSafe proves every registered codec answers a
// file-agnostic capability query (m == nil, as PlanTransfer makes) without
// panicking and self-reports the format it claims. Step 14 threaded a *core.Media
// into Capabilities; the 8 file-uniform codecs ignore it and Matroska nil-guards
// before reading docType, so a nil file must be safe for all of them. The blank
// import above ensures registration regardless of how this file is compiled, so the
// test does not silently depend on a sibling importing the package.
func TestCodecCapabilitiesNilSafe(t *testing.T) {
	codecs := core.Codecs()
	if len(codecs) == 0 {
		t.Fatal("no codecs registered (the package import side effect is missing)")
	}
	for _, c := range codecs {
		caps := c.Capabilities(nil, core.DefaultWriteOptions())
		if caps.Format != c.Format() {
			t.Errorf("%s: caps.Format = %s, want the codec's own format", c.Format(), caps.Format)
		}
	}
}
