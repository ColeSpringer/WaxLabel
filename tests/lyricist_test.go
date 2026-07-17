package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestLyricistRoundTrip proves the canonical LYRICIST key survives a real
// set -> write -> reparse across the main storage mechanisms: a Vorbis comment (FLAC),
// an ID3 TEXT frame (MP3), an MP4 com.apple.iTunes freeform atom (M4A), and the
// embedded-ID3 chunk WAV and AIFF carry (RIFF/IFF have no native lyricist chunk, so
// LYRICIST lands in their ID3 chunk as a TEXT frame, the same route COMPOSER takes).
// Two values exercise the multivalued projection. On MP3 it additionally confirms the
// value lands in the conformant TEXT frame rather than a TXXX:LYRICIST user frame.
func TestLyricistRoundTrip(t *testing.T) {
	want := []string{"Bernie Taupin", "Tim Rice"}

	for _, f := range []string{sampleFLAC, sampleMP3, sampleMP4, sampleWAV, sampleAIFF} {
		src := readFixture(t, f)
		plan, err := mustParseBytes(t, src).Edit().Set(tag.Lyricist, want...).Prepare()
		if err != nil {
			t.Fatalf("%s: prepare: %v", f, err)
		}
		out := applyToBytes(t, src, plan)

		re := mustParseBytes(t, out)
		if got, _ := re.Tags().Get(tag.Lyricist); !slices.Equal(got, want) {
			t.Errorf("%s: LYRICIST = %v, want %v", f, got, want)
		}
		if got := re.Fields().Lyricists; !slices.Equal(got, want) {
			t.Errorf("%s: Fields().Lyricists = %v, want %v", f, got, want)
		}

		if f == sampleMP3 {
			// The value must render as the conformant TEXT frame, never a TXXX:LYRICIST
			// user frame. Absence of the "LYRICIST" description bytes proves it is not a
			// TXXX frame, and presence of the "TEXT" frame id confirms the intended home.
			if !bytes.Contains(out, []byte("TEXT")) {
				t.Errorf("%s: expected a TEXT frame in the written output", f)
			}
			if bytes.Contains(out, []byte("LYRICIST")) {
				t.Errorf("%s: LYRICIST stored as a TXXX user frame, want conformant TEXT frame", f)
			}
		}
	}
}
