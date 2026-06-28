package tag

import "strings"

// keyAliases folds common field-name spellings onto canonical keys. Vorbis mapping and
// [ClosestKey] share this table so aliases resolve the same way on read, edit, and
// suggestion paths.
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
	// Bare DISC/TRACK and spaced or underscored ALBUM ARTIST are common user spellings.
	// DISC and TRACK are too far from their canonical names for the distance fallback.
	"DISC":         DiscNumber,
	"TRACK":        TrackNumber,
	"ALBUM ARTIST": AlbumArtist,
	"ALBUM_ARTIST": AlbumArtist,
}

// AliasKey returns the canonical key for a recognized alternative spelling.
func AliasKey(name string) (Key, bool) {
	k, ok := keyAliases[strings.ToUpper(name)]
	return k, ok
}
