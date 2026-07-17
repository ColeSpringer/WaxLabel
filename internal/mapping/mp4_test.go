package mapping

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestMP4FreeformKeyFoldsCase checks that MP4FreeformKey folds case (like the ID3/Matroska read
// paths) so a foreign or hand-edited "----" atom whose name uses non-standard casing still resolves
// into the canonical view. Folding is case-only, not separator-normalizing, so an underscore variant
// still misses; a genuinely unknown name still returns false.
func TestMP4FreeformKeyFoldsCase(t *testing.T) {
	for _, name := range []string{"MusicBrainz Album Id", "musicbrainz album id", "MUSICBRAINZ ALBUM ID"} {
		if k, ok := MP4FreeformKey(name); !ok || k != tag.MBReleaseID {
			t.Errorf("MP4FreeformKey(%q) = %q, %v; want MBReleaseID, true (case must fold)", name, k, ok)
		}
	}
	// Case folds; separators do not - underscores are not spaces.
	if k, ok := MP4FreeformKey("musicbrainz_album_id"); ok {
		t.Errorf("MP4FreeformKey(%q) = %q, true; want no match (folding is case-only, not separator-normalizing)", "musicbrainz_album_id", k)
	}
	// A genuinely unknown freeform name still misses.
	if k, ok := MP4FreeformKey("Unknown Freeform"); ok {
		t.Errorf("MP4FreeformKey(%q) = %q, true; want no match", "Unknown Freeform", k)
	}
}

// TestMP4KeyFreeformSpellingUnchanged checks the write path is untouched by the read-side fold: a
// canonical key still writes the exact Picard spelling, so folding the read never changes WaxLabel's
// output (which would break Picard/ReplayGain interop).
func TestMP4KeyFreeformSpellingUnchanged(t *testing.T) {
	if got := MP4KeyFreeform(tag.MBReleaseID); got != "MusicBrainz Album Id" {
		t.Errorf("MP4KeyFreeform(MBReleaseID) = %q, want the unchanged Picard spelling %q", got, "MusicBrainz Album Id")
	}
}

// TestMP4LyricistFreeform pins the LYRICIST freeform mapping. MP4 has no standard lyricist
// atom, so it is a com.apple.iTunes freeform: WaxLabel writes the canonical uppercase name,
// and the read path folds foreign casing (Lyricist / lyricist) back onto LYRICIST. The
// exact-case name already round-trips through decodeFreeform's valid-key fallback, so the
// case fold is the behavior this table entry actually adds.
func TestMP4LyricistFreeform(t *testing.T) {
	if got := MP4KeyFreeform(tag.Lyricist); got != "LYRICIST" {
		t.Errorf("MP4KeyFreeform(Lyricist) = %q, want LYRICIST", got)
	}
	for _, name := range []string{"LYRICIST", "Lyricist", "lyricist"} {
		if k, ok := MP4FreeformKey(name); !ok || k != tag.Lyricist {
			t.Errorf("MP4FreeformKey(%q) = %q, %v; want LYRICIST, true (case must fold)", name, k, ok)
		}
	}
}
