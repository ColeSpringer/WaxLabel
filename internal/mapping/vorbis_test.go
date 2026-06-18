package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

func TestCanonicalVorbisAliases(t *testing.T) {
	cases := map[string]tag.Key{
		"date":         tag.RecordingDate,
		"DATE":         tag.RecordingDate,
		"Year":         tag.RecordingDate,
		"totaltracks":  tag.TrackTotal,
		"TOTALDISCS":   tag.DiscTotal,
		"organization": tag.Label,
		"TITLE":        tag.Title,
		"artist":       tag.Artist,
		"WEIRD_CUSTOM": tag.Key("WEIRD_CUSTOM"), // unknown passes through, uppercased
		"weird_custom": tag.Key("WEIRD_CUSTOM"),
	}
	for in, want := range cases {
		if got := CanonicalVorbis(in); got != want {
			t.Errorf("CanonicalVorbis(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVorbisNamePreferred(t *testing.T) {
	if got := VorbisName(tag.RecordingDate); got != "DATE" {
		t.Errorf("VorbisName(RecordingDate) = %q, want DATE", got)
	}
	if got := VorbisName(tag.Title); got != "TITLE" {
		t.Errorf("VorbisName(Title) = %q, want TITLE", got)
	}
}

// Writing a canonical key then reading it back must recover the same key, so
// edits round-trip through the native representation.
func TestVorbisBijectiveForKnownKeys(t *testing.T) {
	keys := []tag.Key{
		tag.Title, tag.Artist, tag.Album, tag.AlbumArtist, tag.Genre,
		tag.RecordingDate, tag.TrackNumber, tag.TrackTotal, tag.DiscNumber,
		tag.MBReleaseID, tag.ReplayGainTrackGain, tag.ISRC, tag.Comment,
		tag.Encoder,
		tag.Key("ARBITRARY_CUSTOM"),
	}
	for _, k := range keys {
		if got := CanonicalVorbis(VorbisName(k)); got != k {
			t.Errorf("round-trip %q -> %q -> %q broke bijectivity", k, VorbisName(k), got)
		}
	}
}

// TestVorbisEncoderCoupling pins both wire directions for the Encoder key, a
// known Vorbis key only because tag.Encoder == "ENCODER" lines up with the
// identity pass-through - a coupling that would break silently if it changed.
func TestVorbisEncoderCoupling(t *testing.T) {
	if got := CanonicalVorbis("ENCODER"); got != tag.Encoder {
		t.Errorf("CanonicalVorbis(%q) = %q, want %q", "ENCODER", got, tag.Encoder)
	}
	if got := VorbisName(tag.Encoder); got != "ENCODER" {
		t.Errorf("VorbisName(Encoder) = %q, want ENCODER", got)
	}
}
