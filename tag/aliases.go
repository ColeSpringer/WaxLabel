package tag

import (
	"slices"
	"strings"
)

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
	// DJMIXER is the only multi-token role key, so fold its spaced/underscored/hyphenated
	// spellings (which validKeyByte accepts as custom keys, a quiet mismatch) onto the canonical.
	"DJ MIXER": DJMixer,
	"DJ_MIXER": DJMixer,
	"DJ-MIXER": DJMixer,
}

// AliasKey returns the canonical key for a recognized alternative spelling.
func AliasKey(name string) (Key, bool) {
	k, ok := keyAliases[strings.ToUpper(name)]
	return k, ok
}

// KeyAliases returns the recognized alternative spellings that resolve to k, sorted, so a
// consumer (the keys command) can surface them. A self-alias - an entry whose spelling is k's
// own canonical name, present so an uppercased canonical spelling still resolves (TRACKTOTAL,
// DISCTOTAL) - is excluded, since listing a key as its own alias is noise. Returns nil for a
// key with no genuine aliases.
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
