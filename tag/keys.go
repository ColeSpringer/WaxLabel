// Package tag defines WaxLabel's canonical, format-neutral tag model: the
// validated [Key] vocabulary, the presence-aware [TagSet]/[TagPatch] that are
// authoritative for editing, the typed [Tags] projection used for convenient
// reads and sugar writes, and [Merge].
package tag

import (
	"fmt"
	"slices"
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

// Validity rules: a key is non-empty, uppercase, printable ASCII (0x20-0x7E)
// excluding '=' (which separates key from value in Vorbis comments) and
// excluding lowercase letters. Vorbis comment names are case-insensitive, so
// canonical keys are normalized to uppercase; requiring that here keeps the
// canonical form unique, so direct comparisons and round-trips can't silently
// disagree because of case. This is also the strictest native vocabulary, so a
// key valid here is representable everywhere a string key is.
func validKeyByte(b byte) bool {
	return b >= 0x20 && b <= 0x7E && b != '=' && !(b >= 'a' && b <= 'z')
}

// ParseKey trims surrounding whitespace, normalizes the result to uppercase,
// then validates it, returning the canonical Key. It accepts any case on input
// (so "title" becomes TITLE) and ignores surrounding whitespace (so "  X  "
// becomes X; internal spaces are preserved, since a space is a valid key byte),
// returning [waxerr.ErrInvalidKey] for empty (or all-whitespace) input or a
// disallowed byte. ParseKey is the blessed way to build a Key from external
// input; the exported constants are the way to name a known one.
func ParseKey(s string) (Key, error) {
	// Trim before the empty check so an all-whitespace input is rejected as empty
	// rather than slipping through, and so the offset in a disallowed-byte error is
	// measured against the trimmed key the caller actually gets.
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%w: empty key", waxerr.ErrInvalidKey)
	}
	// Reject non-ASCII before ToUpper. Some confusable runes fold to ASCII letters under
	// ToUpper, which could turn an invalid field name into a valid key; length-changing
	// folds would also misalign the invalid-byte offset below.
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return "", invalidKeyByteError(s, s[i], i)
		}
	}
	up := strings.ToUpper(s)
	for i := 0; i < len(up); i++ {
		if !validKeyByte(up[i]) {
			return "", invalidKeyByteError(s, up[i], i)
		}
	}
	return Key(up), nil
}

// invalidKeyByteError formats ParseKey's "disallowed byte" error. A printable
// ASCII offender (0x20-0x7E) is shown as a character - "contains '=' at offset 3"
// reads better than "0x3d" - while a control or non-ASCII byte keeps the
// unambiguous hex form.
func invalidKeyByteError(s string, b byte, offset int) error {
	if b >= 0x20 && b <= 0x7E {
		return fmt.Errorf("%w: %q contains %q at offset %d", waxerr.ErrInvalidKey, s, rune(b), offset)
	}
	return fmt.Errorf("%w: %q contains byte 0x%02x at offset %d", waxerr.ErrInvalidKey, s, b, offset)
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
// hand-built Key such as Key("title") is not - use [ParseKey] to normalize.
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

// Multivalued reports whether key canonically holds an ordered list of values
// (multiple artists, composers, genres, comments, performers, or per-artist
// MusicBrainz IDs) rather than a single one. A consumer rendering an edit form uses it to
// choose between one input and a repeatable list. The set mirrors the
// list-valued ([]string) fields of the typed [Tags] projection, so the
// structured signal and the typed sugar agree on which fields are plural. It is
// the key's inherent cardinality; a custom (unknown) key is single-valued, and a
// format that restricts a field further reports that through its capability's
// MaxValues, not here.
func (k Key) Multivalued() bool { return multivalued[k] }

// numberPairKeys are the numeric index/total keys - a track's or disc's number and
// optional total. Each names a single scalar a recording carries exactly one of, and the
// composite formats render it as one value (ID3 collapses TrackNumber+TrackTotal into a single
// "n/total" TRCK frame, TPOS likewise; MP4 packs both into one trkn/disk atom). A Vorbis comment
// (FLAC/Ogg) or a Matroska SimpleTag could physically store two, but WaxLabel models these as a
// single scalar and keeps only the first, so unlike a genuinely multi-valued text key a duplicate
// is treated as non-conformant rather than preserved.
var numberPairKeys = map[Key]bool{
	TrackNumber: true, TrackTotal: true, DiscNumber: true, DiscTotal: true,
}

// NumberPair reports whether k is a numeric track/disc index or total. WaxLabel models such a
// key as a single scalar: the composite formats (ID3 TRCK/TPOS, MP4 trkn/disk) store only one,
// and although a Vorbis comment or Matroska SimpleTag could hold two, the readers keep only the
// first. So a duplicate native item mapping to it - two RIFF IPRT chunks, say - is non-conformant
// junk rather than preservable data, and the IFF readers keep only the first. A plain
// single-valued *text* key is not a NumberPair: its duplicates round-trip through the
// multi-value-capable ID3 fallback and so are preserved, not dropped.
func (k Key) NumberPair() bool { return numberPairKeys[k] }

// SingleValuedMulti reports whether holding count values violates the key's
// single-valued cardinality: it is a known, single-valued key - so the typed [Tags]
// projection would read only the first value - being given more than one. A
// multivalued key, or a custom (unknown) key (which has no typed accessor and no
// enforced cardinality), is never a violation. It is the shared predicate behind
// the linter's single-valued-multi finding and the set/plan --strict guardrail, so
// the two cannot drift apart on the rule.
func (k Key) SingleValuedMulti(count int) bool {
	return count > 1 && k.Known() && !k.Multivalued()
}

func (k Key) String() string { return string(k) }

// KnownKeys returns the published canonical vocabulary in a stable, sorted order.
// It is the programmatic counterpart to the exported Key constants: a consumer
// can enumerate every editable field - pairing each with [Key.Description] and
// [Key.Multivalued] - instead of hard-coding the constant list. The order is
// deterministic so output and golden tests do not churn. The result is a fresh
// copy the caller may sort, filter, or append to freely.
func KnownKeys() []Key {
	return slices.Clone(sortedKnownKeys)
}

// The canonical vocabulary. Names follow the de-facto Vorbis/Picard
// conventions (uppercase, underscore-separated), and the mapping layer
// translates them to each format's native scheme. This set covers the typed
// fields plus the MusicBrainz/Picard long tail.
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

	// Credits. EncodedBy is the person who encoded the file; Encoder is the
	// software/tool that did it (the transcoder stamp). They are distinct keys
	// so a single ENCODER edit reaches the tool stamp on every format.
	Conductor Key = "CONDUCTOR"
	Remixer   Key = "REMIXER"
	Performer Key = "PERFORMER"
	EncodedBy Key = "ENCODEDBY"
	Encoder   Key = "ENCODER"

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

	// Audiobook / spoken-word fields. MediaType is the iTunes "stik" media kind
	// (e.g. 2 = audiobook); Description and LongDescription are the short and
	// full blurbs; Narrator is the reader/performer of an audiobook.
	MediaType       Key = "MEDIATYPE"
	Description     Key = "DESCRIPTION"
	LongDescription Key = "LONGDESCRIPTION"
	Narrator        Key = "NARRATOR"
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
	EncodedBy:           "encoding person",
	Encoder:             "encoding software/tool",
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
	MediaType:           "iTunes media-kind code (numeric; 2 = audiobook), from stik",
	Description:         "short description / blurb",
	LongDescription:     "long description",
	Narrator:            "audiobook narrator",
}

// multivalued is the set of canonical keys that hold a list of distinct values
// rather than a single one. It is kept in lockstep with the list-valued fields of
// the typed [Tags] projection: Artists, Composers, Genres, Comment, Performers, and the
// per-artist MusicBrainz IDs. Keys absent here are single-valued.
var multivalued = map[Key]bool{
	Artist:          true,
	Composer:        true,
	Genre:           true,
	Comment:         true,
	Performer:       true,
	MBArtistID:      true,
	MBAlbumArtistID: true,
}

// sortedKnownKeys is the vocabulary in sorted order, computed once at package
// init (the vocabulary is static), so [KnownKeys] re-sorts nothing per call - it
// just clones this. Kept beside the vocabulary it derives from.
var sortedKnownKeys = func() []Key {
	keys := make([]Key, 0, len(vocabulary))
	for k := range vocabulary {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}()
