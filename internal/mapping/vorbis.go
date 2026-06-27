// Package mapping translates between canonical [tag.Key]s and native tag
// names. This file covers Vorbis-comment style names; other files in this
// package cover ID3, MP4, RIFF, AIFF, and Matroska.
//
// The canonical vocabulary deliberately uses Vorbis-style spellings, so most
// of the mapping is identity. The value here is the read-side alias table
// (folding common alternative spellings and the encoder's date conventions
// onto one canonical key) and the small set of write-side preferred spellings.
package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// readAliases fold alternative native spellings onto a canonical key when
// projecting a parsed file. The native bytes themselves are preserved
// regardless; this only affects the canonical/typed view and edit diffing.
var readAliases = map[string]tag.Key{
	"DATE":           tag.RecordingDate,
	"YEAR":           tag.RecordingDate,
	"ORIGINALYEAR":   tag.OriginalDate,
	"TOTALTRACKS":    tag.TrackTotal,
	"TRACKTOTAL":     tag.TrackTotal,
	"TOTALDISCS":     tag.DiscTotal,
	"DISCTOTAL":      tag.DiscTotal,
	"ORGANIZATION":   tag.Label,
	"UNSYNCEDLYRICS": tag.Lyrics,
	// Bare DISC/TRACK and the spaced/underscored ALBUM ARTIST spellings. DISC and
	// TRACK are 6 edits from their canonical keys, past ClosestKey's distance-2
	// suggestion cap, so without these they would land as custom fields and break a
	// --strict --set DISC=1. ALBUM_ARTIST is Matroska's native spelling; folding both
	// it and the spaced form here makes them resolve canonically on every format.
	"DISC":         tag.DiscNumber,
	"TRACK":        tag.TrackNumber,
	"ALBUM ARTIST": tag.AlbumArtist,
	"ALBUM_ARTIST": tag.AlbumArtist,
}

// writePreferred overrides the native spelling used when a canonical key is
// (re)written. Keys not listed write their own name verbatim.
var writePreferred = map[tag.Key]string{
	tag.RecordingDate: "DATE",
}

// CanonicalVorbis maps a native Vorbis field name (any case) to its canonical
// key. Unknown names pass through as canonical custom fields (the uppercased
// name), so nothing is lost.
func CanonicalVorbis(name string) tag.Key {
	up := strings.ToUpper(name)
	if k, ok := readAliases[up]; ok {
		return k
	}
	return tag.Key(up)
}

// ResolveAlias returns the canonical key for a recognized alternative Vorbis spelling
// (DATE/YEAR -> RECORDINGDATE, TOTALTRACKS -> TRACKTOTAL, ORGANIZATION -> LABEL,
// DISC -> DISCNUMBER, TRACK -> TRACKNUMBER, ALBUM ARTIST/ALBUM_ARTIST -> ALBUMARTIST,
// ...), or key unchanged when it is not an alias. It is case-insensitive for aliases and
// leaves non-alias keys otherwise untouched.
func ResolveAlias(key tag.Key) tag.Key {
	if k, ok := readAliases[strings.ToUpper(string(key))]; ok {
		return k
	}
	return key
}

// VorbisName maps a canonical key to the native Vorbis field name used when
// writing it.
func VorbisName(key tag.Key) string {
	if name, ok := writePreferred[key]; ok {
		return name
	}
	return string(key)
}
