package tag

import (
	"slices"
	"testing"
)

// allKeyConstants lists every exported Key constant. TestKnownKeysMatchConstants
// asserts it equals KnownKeys() exactly, so adding a constant without a vocabulary
// entry (or a vocabulary entry without a constant) fails this test instead of
// silently drifting - the lock the discovery API depends on before the v1.0
// vocabulary freeze.
var allKeyConstants = []Key{
	Title, Artist, Album, AlbumArtist, Composer, Genre,
	TrackNumber, TrackTotal, DiscNumber, DiscTotal,
	RecordingDate, ReleaseDate, OriginalDate,
	Comment, Lyrics, Grouping, Copyright,
	TitleSort, ArtistSort, AlbumSort, AlbumArtistSort, ComposerSort,
	ISRC, Barcode, CatalogNumber, Label, Media, DiscSubtitle,
	Conductor, Remixer, Performer, EncodedBy, Encoder,
	AcoustID, AcoustIDFingerprint,
	Compilation,
	MBReleaseID, MBReleaseGroupID, MBRecordingID, MBReleaseTrackID, MBWorkID, MBDiscID, MBArtistID, MBAlbumArtistID,
	ReplayGainTrackGain, ReplayGainTrackPeak, ReplayGainAlbumGain, ReplayGainAlbumPeak,
	Rating, PlayCount,
	SourceURL, SourceID, AcquisitionDate, EncodingHistory,
	MediaType, Description, LongDescription, Narrator,
}

func TestKnownKeysMatchConstants(t *testing.T) {
	known := KnownKeys()

	if !slices.IsSorted(known) {
		t.Errorf("KnownKeys() is not sorted: %v", known)
	}

	// Every listed key is part of the published vocabulary and carries a meaning.
	for _, k := range known {
		if !k.Known() {
			t.Errorf("KnownKeys() includes %q, which reports Known()==false", k)
		}
		if k.Description() == "" {
			t.Errorf("known key %q has an empty Description()", k)
		}
	}

	// KnownKeys() and the exported constants are the same set, with no duplicates.
	want := make(map[Key]bool, len(allKeyConstants))
	for _, k := range allKeyConstants {
		if want[k] {
			t.Errorf("allKeyConstants lists %q twice", k)
		}
		want[k] = true
	}
	got := make(map[Key]bool, len(known))
	for _, k := range known {
		got[k] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("constant %q is missing from KnownKeys()", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("KnownKeys() has %q, which is not in allKeyConstants", k)
		}
	}
}

// TestMultivalued locks the cardinality signal to the exact set of keys the typed
// Tags projection stores as slices (Artists, Composers, Genres, Performers, and
// the two per-artist MusicBrainz ID lists), so the structured signal and the typed
// sugar cannot disagree about which fields are plural.
func TestMultivalued(t *testing.T) {
	multi := []Key{Artist, Composer, Genre, Performer, MBArtistID, MBAlbumArtistID}
	isMulti := make(map[Key]bool, len(multi))
	for _, k := range multi {
		isMulti[k] = true
		if !k.Multivalued() {
			t.Errorf("%q: Multivalued()=false, want true", k)
		}
	}
	// Every other known key is single-valued.
	for _, k := range KnownKeys() {
		if !isMulti[k] && k.Multivalued() {
			t.Errorf("%q: Multivalued()=true, want false", k)
		}
	}
	// A custom (unknown) key defaults to single-valued.
	if Key("CUSTOM_THING").Multivalued() {
		t.Error("a custom key reported Multivalued()=true, want false")
	}
}

func TestSingleValuedMulti(t *testing.T) {
	// A known single-valued key is a violation only at 2+ values.
	for _, n := range []int{0, 1} {
		if Encoder.SingleValuedMulti(n) {
			t.Errorf("Encoder.SingleValuedMulti(%d)=true, want false", n)
		}
	}
	if !Encoder.SingleValuedMulti(2) {
		t.Error("Encoder.SingleValuedMulti(2)=false, want true")
	}
	// A multivalued key is never a violation, however many values it holds.
	if Artist.SingleValuedMulti(5) {
		t.Error("Artist (multivalued) reported SingleValuedMulti(5)=true, want false")
	}
	// A custom (unknown) key is exempt: it has no typed accessor or enforced
	// cardinality, so several values are legitimate.
	if Key("CUSTOM_THING").SingleValuedMulti(3) {
		t.Error("a custom key reported SingleValuedMulti(3)=true, want false")
	}
}
