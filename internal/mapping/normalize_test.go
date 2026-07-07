package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestReadPathsShareKeyNormalization pins that every codec read path folds a native tag name
// through the shared normalizeKey (trim surrounding whitespace, then upper-case) before resolving
// it - so a padded or foreign-cased name resolves to the same canonical key regardless of format.
// Before unification, mp4 and vorbis did not trim, so " artist " round-tripped differently there.
func TestReadPathsShareKeyNormalization(t *testing.T) {
	// Vorbis: a padded alias now resolves (the alias lookup sees the trimmed name), and a padded
	// custom name trims to its canonical spelling.
	if k := CanonicalVorbis("  date  "); k != tag.RecordingDate {
		t.Errorf("CanonicalVorbis(padded DATE alias) = %q, want RecordingDate", k)
	}
	if k := CanonicalVorbis("  MyField  "); k != tag.Key("MYFIELD") {
		t.Errorf("CanonicalVorbis(padded custom) = %q, want MYFIELD", k)
	}
	// MP4 freeform: a padded, foreign-cased known name still folds to the canonical key.
	if k, ok := MP4FreeformKey("  musicbrainz album id  "); !ok || k != tag.MBReleaseID {
		t.Errorf("MP4FreeformKey(padded foreign) = %q,%v, want MBReleaseID,true", k, ok)
	}
	// Matroska and ID3 already trimmed; pin that they share the same helper (unchanged behavior).
	if k, ok := MatroskaTagKey("  ARTIST  "); !ok || k != tag.Artist {
		t.Errorf("MatroskaTagKey(padded) = %q,%v, want Artist,true", k, ok)
	}
	if k, ok := ID3TXXXKey("  MyField  "); !ok || k != tag.Key("MYFIELD") {
		t.Errorf("ID3TXXXKey(padded) = %q,%v, want MYFIELD,true", k, ok)
	}

	// Internal spaces and separators are preserved (not folded), matching each alias table and
	// tag.ParseKey - an underscore stays distinct from a space.
	if _, ok := MP4FreeformKey("musicbrainz_album_id"); ok {
		t.Error("MP4FreeformKey must not fold separators: underscores are not spaces")
	}
}
