package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestID3TXXXKeyTCMP checks that ffmpeg's TXXX:TCMP user frame folds onto canonical
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

// TestID3LyricistFrame pins the LYRICIST mapping coupling: the conformant TEXT frame
// reads onto canonical LYRICIST and writes back to TEXT, and a legacy TXXX:LYRICIST
// user frame still folds onto LYRICIST on read.
func TestID3LyricistFrame(t *testing.T) {
	if k, ok := ID3FrameKey("TEXT"); !ok || k != tag.Lyricist {
		t.Errorf("ID3FrameKey(\"TEXT\") = %q, %v; want LYRICIST, true", k, ok)
	}
	if id, ok := ID3KeyFrame(tag.Lyricist); !ok || id != "TEXT" {
		t.Errorf("ID3KeyFrame(LYRICIST) = %q, %v; want TEXT, true", id, ok)
	}
	if k, ok := ID3TXXXKey("LYRICIST"); !ok || k != tag.Lyricist {
		t.Errorf("ID3TXXXKey(\"LYRICIST\") = %q, %v; want LYRICIST, true", k, ok)
	}
}
