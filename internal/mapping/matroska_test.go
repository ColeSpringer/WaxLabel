package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestMatroskaDJMixerRead folds Matroska's underscore/space DJMIXER spellings onto the
// canonical key on read, while the write side keeps the identity "DJMIXER". Matroska tag
// names are conventionally uppercase-underscore, so "DJ_MIXER" is the spelling a foreign
// file most likely uses for this multi-token role key.
func TestMatroskaDJMixerRead(t *testing.T) {
	for _, name := range []string{"DJ_MIXER", "DJ MIXER", "DJ-MIXER", "dj_mixer", "DJMIXER"} {
		if k, ok := MatroskaTagKey(name); !ok || k != tag.DJMixer {
			t.Errorf("MatroskaTagKey(%q) = %q, %v; want DJMIXER, true", name, k, ok)
		}
	}
	// The write side stays identity (no matroskaNames entry): the canonical "DJMIXER" is
	// emitted and reads back to the same key, so the round-trip is exact.
	if got := MatroskaTagName(tag.DJMixer); got != "DJMIXER" {
		t.Errorf("MatroskaTagName(DJMIXER) = %q, want DJMIXER (identity write)", got)
	}
}
