package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestRolesRoundTrip proves the six contributor-role keys survive a real set -> write ->
// reparse across the main storage mechanisms: a Vorbis comment (FLAC), an ID3 involved-people
// frame (MP3), MP4 com.apple.iTunes freeforms (M4A), the embedded-ID3 chunk WAV and AIFF carry,
// and Matroska SimpleTags (MKA, identity names). Producers and Writers hold two values to
// exercise the multivalued projection. On MP3
// it additionally confirms the five involved-people roles land in the ID3 involved-people
// frame with the Picard lowercase function strings, WRITER lands in a TXXX:Writer user frame,
// and no role leaks as an uppercase TXXX user frame.
func TestRolesRoundTrip(t *testing.T) {
	roles := []struct {
		key  tag.Key
		vals []string
		get  func(*wl.Document) []string
	}{
		{tag.Producer, []string{"Alice", "Amy"}, func(d *wl.Document) []string { return d.Fields().Producers }},
		{tag.Engineer, []string{"Ed"}, func(d *wl.Document) []string { return d.Fields().Engineers }},
		{tag.Mixer, []string{"Max"}, func(d *wl.Document) []string { return d.Fields().Mixers }},
		{tag.Arranger, []string{"Ann"}, func(d *wl.Document) []string { return d.Fields().Arrangers }},
		{tag.Writer, []string{"Will", "Wanda"}, func(d *wl.Document) []string { return d.Fields().Writers }},
		{tag.DJMixer, []string{"Dee"}, func(d *wl.Document) []string { return d.Fields().DJMixers }},
	}

	for _, f := range []string{sampleFLAC, sampleMP3, sampleMP4, sampleWAV, sampleAIFF, sampleMKA} {
		src := readFixture(t, f)
		ed := mustParseBytes(t, src).Edit()
		for _, r := range roles {
			ed = ed.Set(r.key, r.vals...)
		}
		plan, err := ed.Prepare()
		if err != nil {
			t.Fatalf("%s: prepare: %v", f, err)
		}
		out := applyToBytes(t, src, plan)

		re := mustParseBytes(t, out)
		for _, r := range roles {
			if got, _ := re.Tags().Get(r.key); !slices.Equal(got, r.vals) {
				t.Errorf("%s: %s = %v, want %v", f, r.key, got, r.vals)
			}
			if got := r.get(re); !slices.Equal(got, r.vals) {
				t.Errorf("%s: Fields().%s = %v, want %v", f, r.key, got, r.vals)
			}
		}

		if f == sampleMP3 {
			// The five involved-people roles render with the Picard lowercase function strings
			// (in a TIPL or IPLS frame, version-dependent), never an uppercase TXXX user frame.
			for _, fn := range []string{"producer", "mix", "DJ-mix"} {
				if !bytes.Contains(out, []byte(fn)) {
					t.Errorf("%s: expected involved-people function %q in the written output", f, fn)
				}
			}
			if bytes.Contains(out, []byte("PRODUCER")) {
				t.Errorf("%s: a role leaked as an uppercase TXXX user frame (found \"PRODUCER\")", f)
			}
			// WRITER rides a TXXX:Writer user frame (Picard spelling), not the involved-people list.
			if !bytes.Contains(out, []byte("Writer")) {
				t.Errorf("%s: expected a TXXX:Writer user frame in the written output", f)
			}
		}
	}
}
