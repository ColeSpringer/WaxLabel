package matroska

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// The lossless and DTS CodecIDs take the names their MP4 spellings do.
func TestCodecNameFoldsToTheSharedNames(t *testing.T) {
	for id, want := range map[string][2]string{
		"A_TRUEHD":       {"TrueHD", ""},
		"A_DTS":          {"DTS", ""},
		"A_DTS/EXPRESS":  {"DTS", "DTS Express"},
		"A_DTS/LOSSLESS": {"DTS", "DTS-HD Master Audio"},
		"A_MLP":          {"MLP", ""},
	} {
		if codec, profile := core.CanonicalCodec(codecName(id)); codec != want[0] || profile != want[1] {
			t.Errorf("%s: %q/%q, want %q/%q", id, codec, profile, want[0], want[1])
		}
	}
}
