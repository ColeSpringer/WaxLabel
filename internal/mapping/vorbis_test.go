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
		"DISC":         tag.DiscNumber,  // bare DISC folds to the canonical key on read
		"Track":        tag.TrackNumber, // ...as does bare TRACK
		"ALBUM ARTIST": tag.AlbumArtist, // spaced and underscored album-artist variants
		"album_artist": tag.AlbumArtist,
		"WEIRD_CUSTOM": tag.Key("WEIRD_CUSTOM"), // unknown passes through, uppercased
		"weird_custom": tag.Key("WEIRD_CUSTOM"),
	}
	for in, want := range cases {
		if got := CanonicalVorbis(in); got != want {
			t.Errorf("CanonicalVorbis(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveAliasNewSpellings pins the format-independent edit-resolve path for the
// bare DISC/TRACK and spaced/underscored ALBUM ARTIST spellings. DISC and TRACK are 6
// edits from their canonical keys, past ClosestKey's distance-2 suggestion cap, so
// without these aliases a --set DISC=1 would land as a custom field (and break --strict).
func TestResolveAliasNewSpellings(t *testing.T) {
	cases := map[tag.Key]tag.Key{
		"DISC":         tag.DiscNumber,
		"disc":         tag.DiscNumber,
		"TRACK":        tag.TrackNumber,
		"track":        tag.TrackNumber,
		"ALBUM ARTIST": tag.AlbumArtist,
		"album artist": tag.AlbumArtist,
		"ALBUM_ARTIST": tag.AlbumArtist,
		"TITLE":        tag.Title,            // a non-alias key is returned unchanged
		"MY_CUSTOM":    tag.Key("MY_CUSTOM"), // an unknown key is returned unchanged
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

// Writing a canonical key and reading it back must recover the same key, so edits
// round-trip through the native representation. Use the published vocabulary plus a
// custom key to catch aliases that collapse distinct keys, such as DESCRIPTION and
// COMMENT, onto the same canonical field.
func TestVorbisBijectiveForKnownKeys(t *testing.T) {
	keys := append(tag.KnownKeys(), tag.Key("ARBITRARY_CUSTOM"))
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
