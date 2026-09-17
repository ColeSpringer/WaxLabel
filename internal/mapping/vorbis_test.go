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
		"DISC":         tag.DiscNumber,
		"Track":        tag.TrackNumber,
		"ALBUM ARTIST": tag.AlbumArtist,
		"album_artist": tag.AlbumArtist,
		"PUBLISHER":    tag.Label,
		"PART_NUMBER":  tag.TrackNumber,
		"WEIRD_CUSTOM": tag.Key("WEIRD_CUSTOM"),
		"weird_custom": tag.Key("WEIRD_CUSTOM"),
	}
	for in, want := range cases {
		if got := CanonicalVorbis(in); got != want {
			t.Errorf("CanonicalVorbis(%q) = %q, want %q", in, got, want)
		}
	}
}

// DISC/TRACK and ALBUM ARTIST variants are 6 edits from canonical keys (past ClosestKey cap).
func TestResolveAliasNewSpellings(t *testing.T) {
	cases := map[tag.Key]tag.Key{
		"DISC":         tag.DiscNumber,
		"disc":         tag.DiscNumber,
		"TRACK":        tag.TrackNumber,
		"track":        tag.TrackNumber,
		"ALBUM ARTIST": tag.AlbumArtist,
		"album artist": tag.AlbumArtist,
		"ALBUM_ARTIST": tag.AlbumArtist,
		"TITLE":        tag.Title,
		"MY_CUSTOM":    tag.Key("MY_CUSTOM"),
	}
	for in, want := range cases {
		if got := ResolveAlias(in); got != want {
			t.Errorf("ResolveAlias(%q) = %q, want %q", in, got, want)
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

// CanonicalVorbis(VorbisName(k)) must equal k for known keys plus a custom key.
func TestVorbisBijectiveForKnownKeys(t *testing.T) {
	keys := append(tag.KnownKeys(), tag.Key("ARBITRARY_CUSTOM"))
	for _, k := range keys {
		if got := CanonicalVorbis(VorbisName(k)); got != k {
			t.Errorf("round-trip %q -> %q -> %q broke bijectivity", k, VorbisName(k), got)
		}
	}
}

// tag.Encoder == "ENCODER" identity coupling.
func TestVorbisEncoderCoupling(t *testing.T) {
	if got := CanonicalVorbis("ENCODER"); got != tag.Encoder {
		t.Errorf("CanonicalVorbis(%q) = %q, want %q", "ENCODER", got, tag.Encoder)
	}
	if got := VorbisName(tag.Encoder); got != "ENCODER" {
		t.Errorf("VorbisName(Encoder) = %q, want ENCODER", got)
	}
}
