package tag

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/waxerr"
)

// allKeyConstants lists every exported Key constant. TestKnownKeysMatchConstants
// asserts it equals KnownKeys() exactly, so adding a constant without a
// vocabulary entry, or adding a vocabulary entry without a constant, fails this
// test instead of breaking discovery output quietly.
var allKeyConstants = []Key{
	Title, Artist, Album, AlbumArtist, Composer, Lyricist, Genre,
	TrackNumber, TrackTotal, DiscNumber, DiscTotal,
	RecordingDate, ReleaseDate, OriginalDate,
	Comment, Lyrics, Grouping, Copyright,
	TitleSort, ArtistSort, AlbumSort, AlbumArtistSort, ComposerSort,
	ISRC, Barcode, CatalogNumber, Label, Media, DiscSubtitle,
	ReleaseCountry, ReleaseStatus, ReleaseType,
	Conductor, Remixer, Performer, EncodedBy, Encoder,
	Producer, Engineer, Mixer, Arranger, Writer, DJMixer,
	AcoustID, AcoustIDFingerprint,
	Compilation,
	MBReleaseID, MBReleaseGroupID, MBRecordingID, MBReleaseTrackID, MBWorkID, MBDiscID, MBArtistID, MBAlbumArtistID,
	ReplayGainTrackGain, ReplayGainTrackPeak, ReplayGainAlbumGain, ReplayGainAlbumPeak,
	Rating, PlayCount,
	SourceURL, SourceID, AcquisitionDate, EncodingHistory,
	MediaType, Description, LongDescription, Narrator,
	ITunesAdvisory, ITunesGapless, ShowMovement, BPM,
	Work, MovementName, Movement, MovementTotal,
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

// TestMultivalued keeps the cardinality signal aligned with the fields the typed Tags
// projection stores as slices.
func TestMultivalued(t *testing.T) {
	multi := []Key{Artist, Composer, Lyricist, Genre, Comment, Performer,
		Producer, Engineer, Mixer, Arranger, Writer, DJMixer,
		ReleaseType, MBArtistID, MBAlbumArtistID}
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

// TestParseKeyInvalidByteMessage checks the offending-byte rendering: a printable
// ASCII byte is shown as a character (easier to read than hex), while a control or
// non-ASCII byte keeps the unambiguous hex form. Both stay ErrInvalidKey.
func TestParseKeyInvalidByteMessage(t *testing.T) {
	if _, err := ParseKey("TI=TLE"); err == nil || !strings.Contains(err.Error(), "'='") {
		t.Errorf("ParseKey(\"TI=TLE\") error = %v, want a quoted '=' character", err)
	}
	if _, err := ParseKey("TIT\x01LE"); err == nil || !strings.Contains(err.Error(), "0x01") {
		t.Errorf("ParseKey(ctrl) error = %v, want hex 0x01", err)
	}
	if _, err := ParseKey("A=B"); !errors.Is(err, waxerr.ErrInvalidKey) {
		t.Errorf("ParseKey error is not ErrInvalidKey: %v", err)
	}
}

// TestR128GainKeysAreCustomOwnAudio: the Opus loudness tags sit outside the canonical
// vocabulary on purpose (they are valid custom keys), but they describe this file's own
// audio, so a metadata copy must leave the destination's alone.
func TestR128GainKeysAreCustomOwnAudio(t *testing.T) {
	for _, k := range []Key{"R128_TRACK_GAIN", "R128_ALBUM_GAIN"} {
		if !IsR128GainKey(k) {
			t.Errorf("IsR128GainKey(%s) = false, want true", k)
		}
		if k.Known() {
			t.Errorf("%s.Known() = true, want false (deliberately outside the canonical vocabulary)", k)
		}
		if !k.Valid() {
			t.Errorf("%s.Valid() = false, want a valid custom key", k)
		}
		if !k.DescribesOwnAudio() {
			t.Errorf("%s.DescribesOwnAudio() = false, want true", k)
		}
		if IsReplayGainKey(k) {
			t.Errorf("IsReplayGainKey(%s) = true; R128 values are plain Q7.8 integers, not dB text", k)
		}
		// RFC 7845 defines the value exactly, so these keys are validated and trimmed even
		// though they are not canonical.
		if !IsTrimmableKey(k) {
			t.Errorf("IsTrimmableKey(%s) = false, want true", k)
		}
		if _, ok := ValidatorFor(k); !ok {
			t.Errorf("ValidatorFor(%s) reported no contract, want the R128 gain one", k)
		}
	}
	if IsR128GainKey("R128_TRACK_GAINX") {
		t.Error("IsR128GainKey should not match a longer key")
	}
}

// TestValidR128GainValue: RFC 7845 section 5.2.1 spells the value out - a base-10 integer in
// the signed 16-bit range, optional sign, leading zeros allowed, at most 6 characters.
func TestValidR128GainValue(t *testing.T) {
	valid := []string{"-573", " 111 ", "+5", "-32768", "32767", "000573", "0"}
	invalid := []string{"abc", "-3.5 dB", "40000", "-40000", "0000573", "- 5", "", "  ", "+", "-", "1e3", "5 5"}
	for _, v := range valid {
		if !ValidR128GainValue("R128_TRACK_GAIN", v) {
			t.Errorf("ValidR128GainValue(%q) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if ValidR128GainValue("R128_ALBUM_GAIN", v) {
			t.Errorf("ValidR128GainValue(%q) = true, want false", v)
		}
	}
	// A key outside the category has no opinion, matching every other validator here.
	if !ValidR128GainValue(Title, "not a number") {
		t.Error("ValidR128GainValue should report a non-R128 key valid")
	}
}

func TestFoldKey(t *testing.T) {
	cases := []struct {
		in   string
		want Key
		ok   bool
	}{
		{"MYCUSTOM", "MYCUSTOM", true},
		{"custom_key", "CUSTOM_KEY", true},
		{"author", "AUTHOR", true},
		{"org.example.thing", "ORG.EXAMPLE.THING", true}, // a dot is a valid key byte and is kept
		{"a=b", "A_B", true},
		{"naïve", "NA_VE", true}, // one placeholder per rune, not per byte
		{"tab\there", "TAB_HERE", true},
		{"", "", false},
		{"   ", "", false},
	}
	for _, c := range cases {
		got, ok := FoldKey(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("FoldKey(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
