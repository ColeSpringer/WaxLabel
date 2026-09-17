package tag

import (
	"slices"
	"strings"
)

// keyAliases folds alternate spellings onto canonical keys (shared with ClosestKey).
var keyAliases = map[string]Key{
	"DATE":           RecordingDate,
	"YEAR":           RecordingDate,
	"ORIGINALYEAR":   OriginalDate,
	"TOTALTRACKS":    TrackTotal,
	"TRACKTOTAL":     TrackTotal,
	"TOTALDISCS":     DiscTotal,
	"DISCTOTAL":      DiscTotal,
	"ORGANIZATION":   Label,
	"UNSYNCEDLYRICS": Lyrics,
	// Bare DISC/TRACK and spaced ALBUM ARTIST are common user spellings.
	"DISC":         DiscNumber,
	"TRACK":        TrackNumber,
	"ALBUM ARTIST": AlbumArtist,
	"ALBUM_ARTIST": AlbumArtist,
	// DJMIXER spaced/underscored/hyphenated forms.
	"DJ MIXER": DJMixer,
	"DJ_MIXER": DJMixer,
	"DJ-MIXER": DJMixer,
	// Legacy Picard / APE release status and type spellings.
	"MUSICBRAINZ_ALBUMSTATUS": ReleaseStatus,
	"MUSICBRAINZ_ALBUMTYPE":   ReleaseType,
	// Matroska native spellings (retarget edits; ENCODER omitted, already canonical).
	"LEAD_PERFORMER": Artist,
	"DATE_RECORDED":  RecordingDate,
	"DATE_RELEASED":  ReleaseDate,
	"DATE_RELEASE":   ReleaseDate,
	"DATE_ORIGINAL":  OriginalDate,
	"ORIGINAL_DATE":  OriginalDate,
	"ENCODED_BY":     EncodedBy,
	"PART_NUMBER":    TrackNumber,
	"TOTAL_PARTS":    TrackTotal,
	"TOTAL_DISCS":    DiscTotal,
	"CATALOG_NUMBER": CatalogNumber,
	"PUBLISHER":      Label,
	"REMIXED_BY":     Remixer,
	"CONTENT_GROUP":  Grouping,
}

// AliasKey returns the canonical key for a recognized alternative spelling.
func AliasKey(name string) (Key, bool) {
	k, ok := keyAliases[strings.ToUpper(name)]
	return k, ok
}

// KeyAliases returns alternate spellings for k (sorted), excluding self-aliases.
func KeyAliases(k Key) []string {
	canon := strings.ToUpper(string(k))
	var out []string
	for alias, target := range keyAliases {
		if target == k && alias != canon {
			out = append(out, alias)
		}
	}
	slices.Sort(out)
	return out
}
