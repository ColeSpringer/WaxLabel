package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// The MP4, Matroska and ID3 read paths use normalizeKey (trim + upper) before lookup. A Vorbis
// field name folds ASCII case only: padded, or with a letter outside ASCII, it is no key.
func TestReadPathsShareKeyNormalization(t *testing.T) {
	if k := CanonicalVorbis("date"); k != tag.RecordingDate {
		t.Errorf("CanonicalVorbis(DATE alias) = %q, want RecordingDate", k)
	}
	if k := CanonicalVorbis("MyField"); k != tag.Key("MYFIELD") {
		t.Errorf("CanonicalVorbis(custom) = %q, want MYFIELD", k)
	}
	for _, name := range []string{"  date  ", "MyField ", "\tTITLE", "t\u0131tle", "ALBUM\u017f"} {
		if k := CanonicalVorbis(name); k != "" {
			t.Errorf("CanonicalVorbis(%q) = %q, want no key", name, k)
		}
	}
	if k, ok := MP4FreeformKey("  musicbrainz album id  "); !ok || k != tag.MBReleaseID {
		t.Errorf("MP4FreeformKey(padded foreign) = %q,%v, want MBReleaseID,true", k, ok)
	}
	if k, ok := MatroskaTagKey("  ARTIST  "); !ok || k != tag.Artist {
		t.Errorf("MatroskaTagKey(padded) = %q,%v, want Artist,true", k, ok)
	}
	if k, ok := ID3TXXXKey("  MyField  "); !ok || k != tag.Key("MYFIELD") {
		t.Errorf("ID3TXXXKey(padded) = %q,%v, want MYFIELD,true", k, ok)
	}
	// Separators are not folded.
	if _, ok := MP4FreeformKey("musicbrainz_album_id"); ok {
		t.Error("MP4FreeformKey must not fold separators: underscores are not spaces")
	}
}
