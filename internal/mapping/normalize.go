package mapping

import "strings"

// normalizeKey folds a native tag name to the read-lookup form shared by every codec's read path:
// trim surrounding whitespace, then upper-case. Internal spaces and separators are preserved (an
// underscore stays distinct from a space, matching each codec's alias tables and [tag.ParseKey]),
// so "musicbrainz_album_id" still differs from "MusicBrainz Album Id". Centralizing it keeps the
// ID3, MP4, Matroska, and Vorbis read paths from drifting: each applies this one fold instead of
// its own ToUpper (some trimmed, some not), so the same padded or foreign-cased name resolves to
// the same canonical key across formats. It mirrors the trim+upper [tag.ParseKey] already applies,
// so a name passed to ParseKey after this is unchanged by it.
func normalizeKey(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}
