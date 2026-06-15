// Package tag defines WaxLabel's canonical, format-neutral tag model: the
// validated [Key] vocabulary, the presence-aware [TagSet]/[TagPatch] that are
// authoritative for editing, the typed [Tags] projection used for convenient
// reads and sugar writes, and [Merge].
package tag

import (
	"fmt"
	"strings"

	"github.com/colespringer/waxlabel/waxerr"
)

// Key is a validated canonical tag name. Canonical keys are format-neutral:
// each codec's mapping layer translates them to and from native
// representations (Vorbis comment names, ID3 frame IDs, MP4 atoms).
//
// A Key is uppercase ASCII. Keys drawn from the published vocabulary (the
// exported constants below) are "known"; any other valid key is a canonical
// custom field that passes through read and write unchanged. Native-only
// entries that have no neutral meaning are modeled separately (see the
// native editing hatch), not as keys.
type Key string

// Validity rules: a key is non-empty, uppercase, printable ASCII (0x20–0x7E)
// excluding '=' (which separates key from value in Vorbis comments) and
// excluding lowercase letters. Vorbis comment names are case-insensitive, so
// canonical keys are normalized to uppercase; requiring that here keeps the
// canonical form unique, so direct comparisons and round-trips can't silently
// disagree because of case. This is also the strictest native vocabulary, so a
// key valid here is representable everywhere a string key is.
func validKeyByte(b byte) bool {
	return b >= 0x20 && b <= 0x7E && b != '=' && !(b >= 'a' && b <= 'z')
}

// ParseKey normalizes s to uppercase, then validates it, returning the
// canonical Key. It accepts any case on input (so "title" becomes TITLE) and
// returns [waxerr.ErrInvalidKey] for empty input or a disallowed byte. ParseKey
// is the blessed way to build a Key from external input; the exported constants
// are the way to name a known one.
func ParseKey(s string) (Key, error) {
	if s == "" {
		return "", fmt.Errorf("%w: empty key", waxerr.ErrInvalidKey)
	}
	up := strings.ToUpper(s)
	for i := 0; i < len(up); i++ {
		if !validKeyByte(up[i]) {
			return "", fmt.Errorf("%w: %q contains byte 0x%02x at offset %d",
				waxerr.ErrInvalidKey, s, up[i], i)
		}
	}
	return Key(up), nil
}

// MustKey is ParseKey for compile-time constants; it panics on an invalid key.
func MustKey(s string) Key {
	k, err := ParseKey(s)
	if err != nil {
		panic(err)
	}
	return k
}

// Valid reports whether k satisfies the key rules (non-empty, uppercase,
// printable ASCII without '='). The exported Key constants are always valid; a
// hand-built Key such as Key("title") is not — use [ParseKey] to normalize.
func (k Key) Valid() bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		if !validKeyByte(k[i]) {
			return false
		}
	}
	return true
}

// Known reports whether k is part of the published canonical vocabulary.
func (k Key) Known() bool {
	_, ok := vocabulary[k]
	return ok
}

// Description returns the human-readable meaning of a known key, or "" for a
// custom field.
func (k Key) Description() string { return vocabulary[k] }

func (k Key) String() string { return string(k) }

// The canonical vocabulary. Names follow the de-facto Vorbis/Picard
// conventions (uppercase, underscore-separated) because FLAC is the first
// codec; the mapping layer translates to other native schemes. This set
// covers the typed sugar plus the MusicBrainz/Picard long tail and is frozen
// at v1.0.
const (
	// Core descriptive fields.
	Title       Key = "TITLE"
	Artist      Key = "ARTIST"
	Album       Key = "ALBUM"
	AlbumArtist Key = "ALBUMARTIST"
	Composer    Key = "COMPOSER"
	Genre       Key = "GENRE"

	// Numbering. Total may be carried separately or as "n/total" natively;
	// the model keeps them distinct.
	TrackNumber Key = "TRACKNUMBER"
	TrackTotal  Key = "TRACKTOTAL"
	DiscNumber  Key = "DISCNUMBER"
	DiscTotal   Key = "DISCTOTAL"

	// Three-way dates: when it was recorded, when this edition was released,
	// when the work was originally released.
	RecordingDate Key = "RECORDINGDATE"
	ReleaseDate   Key = "RELEASEDATE"
	OriginalDate  Key = "ORIGINALDATE"

	// Free text.
	Comment   Key = "COMMENT"
	Lyrics    Key = "LYRICS"
	Grouping  Key = "GROUPING"
	Copyright Key = "COPYRIGHT"

	// Sort names.
	TitleSort       Key = "TITLESORT"
	ArtistSort      Key = "ARTISTSORT"
	AlbumSort       Key = "ALBUMSORT"
	AlbumArtistSort Key = "ALBUMARTISTSORT"
	ComposerSort    Key = "COMPOSERSORT"

	// Identifiers and release detail.
	ISRC          Key = "ISRC"
	Barcode       Key = "BARCODE"
	CatalogNumber Key = "CATALOGNUMBER"
	Label         Key = "LABEL"
	Media         Key = "MEDIA"
	DiscSubtitle  Key = "DISCSUBTITLE"

	// Credits.
	Conductor Key = "CONDUCTOR"
	Remixer   Key = "REMIXER"
	Performer Key = "PERFORMER"
	EncodedBy Key = "ENCODEDBY"

	// Acoustic fingerprint (stored, never computed by WaxLabel).
	AcoustID            Key = "ACOUSTID_ID"
	AcoustIDFingerprint Key = "ACOUSTID_FINGERPRINT"

	// Compilation flag.
	Compilation Key = "COMPILATION"

	// MusicBrainz identifiers.
	MBReleaseID      Key = "MUSICBRAINZ_ALBUMID"
	MBReleaseGroupID Key = "MUSICBRAINZ_RELEASEGROUPID"
	MBRecordingID    Key = "MUSICBRAINZ_TRACKID"
	MBReleaseTrackID Key = "MUSICBRAINZ_RELEASETRACKID"
	MBWorkID         Key = "MUSICBRAINZ_WORKID"
	MBDiscID         Key = "MUSICBRAINZ_DISCID"
	MBArtistID       Key = "MUSICBRAINZ_ARTISTID"
	MBAlbumArtistID  Key = "MUSICBRAINZ_ALBUMARTISTID"

	// ReplayGain (album-level distinct from track-level; Opus R128 is modeled
	// separately by the Opus codec, not here).
	ReplayGainTrackGain Key = "REPLAYGAIN_TRACK_GAIN"
	ReplayGainTrackPeak Key = "REPLAYGAIN_TRACK_PEAK"
	ReplayGainAlbumGain Key = "REPLAYGAIN_ALBUM_GAIN"
	ReplayGainAlbumPeak Key = "REPLAYGAIN_ALBUM_PEAK"

	// Player metadata.
	Rating    Key = "RATING"
	PlayCount Key = "PLAYCOUNT"

	// Acquisition provenance. Generic (not specific to any one tool): where a
	// file came from and how it was produced.
	SourceURL       Key = "SOURCE_URL"
	SourceID        Key = "SOURCE_ID"
	AcquisitionDate Key = "ACQUISITION_DATE"
	EncodingHistory Key = "ENCODING_HISTORY"
)

// vocabulary maps every known key to its description. KnownKeys derives its
// order from this map's keys (sorted) so callers get a stable listing.
var vocabulary = map[Key]string{
	Title:               "track title",
	Artist:              "track artist",
	Album:               "album/release title",
	AlbumArtist:         "album artist",
	Composer:            "composer",
	Genre:               "genre",
	TrackNumber:         "track number within the disc",
	TrackTotal:          "total tracks on the disc",
	DiscNumber:          "disc number within the release",
	DiscTotal:           "total discs in the release",
	RecordingDate:       "date the audio was recorded",
	ReleaseDate:         "date this edition was released",
	OriginalDate:        "date the work was originally released",
	Comment:             "free-form comment",
	Lyrics:              "unsynchronized lyrics",
	Grouping:            "content grouping",
	Copyright:           "copyright statement",
	TitleSort:           "title sort name",
	ArtistSort:          "artist sort name",
	AlbumSort:           "album sort name",
	AlbumArtistSort:     "album-artist sort name",
	ComposerSort:        "composer sort name",
	ISRC:                "International Standard Recording Code",
	Barcode:             "release barcode (UPC/EAN)",
	CatalogNumber:       "label catalog number",
	Label:               "record label",
	Media:               "physical media type",
	DiscSubtitle:        "disc subtitle",
	Conductor:           "conductor",
	Remixer:             "remixer",
	Performer:           "performer, optionally role-qualified",
	EncodedBy:           "encoding person or tool",
	AcoustID:            "AcoustID identifier",
	AcoustIDFingerprint: "AcoustID fingerprint",
	Compilation:         "part-of-compilation flag",
	MBReleaseID:         "MusicBrainz release ID",
	MBReleaseGroupID:    "MusicBrainz release-group ID",
	MBRecordingID:       "MusicBrainz recording ID",
	MBReleaseTrackID:    "MusicBrainz release-track ID",
	MBWorkID:            "MusicBrainz work ID",
	MBDiscID:            "MusicBrainz disc ID",
	MBArtistID:          "MusicBrainz artist ID",
	MBAlbumArtistID:     "MusicBrainz album-artist ID",
	ReplayGainTrackGain: "ReplayGain track gain",
	ReplayGainTrackPeak: "ReplayGain track peak",
	ReplayGainAlbumGain: "ReplayGain album gain",
	ReplayGainAlbumPeak: "ReplayGain album peak",
	Rating:              "user rating",
	PlayCount:           "play count",
	SourceURL:           "acquisition source URL",
	SourceID:            "acquisition source identifier",
	AcquisitionDate:     "date the file was acquired",
	EncodingHistory:     "encoding history / provenance chain",
}
