// Package mapping translates between canonical [tag.Key]s and native tag
// names. M0 ships the Vorbis-comment mapping (FLAC, and later Ogg); other
// formats slot in as their milestones land.
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
	"DESCRIPTION":    tag.Comment,
	"ORGANIZATION":   tag.Label,
	"UNSYNCEDLYRICS": tag.Lyrics,
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

// VorbisName maps a canonical key to the native Vorbis field name used when
// writing it.
func VorbisName(key tag.Key) string {
	if name, ok := writePreferred[key]; ok {
		return name
	}
	return string(key)
}
