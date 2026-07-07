// Package mapping translates between canonical [tag.Key]s and native tag
// names. This file covers Vorbis-comment style names; other files in this
// package cover ID3, MP4, RIFF, AIFF, and Matroska.
//
// The canonical vocabulary deliberately uses Vorbis-style spellings, so most
// of the mapping is identity. The value here is the read-side alias table
// (folding common alternative spellings and the encoder's date conventions
// onto one canonical key) and the small set of write-side preferred spellings.
package mapping

import "github.com/colespringer/waxlabel/tag"

// The read-side alias table lives in package tag ([tag.AliasKey]) so canonicalization and
// "did you mean?" suggestions share one definition. Native bytes are preserved; alias
// resolution only affects the canonical view and edit diffing.

// writePreferred overrides the native spelling used when a canonical key is
// (re)written. Keys not listed write their own name verbatim.
var writePreferred = map[tag.Key]string{
	tag.RecordingDate: "DATE",
}

// CanonicalVorbis maps a native Vorbis field name (any case, ignoring surrounding
// whitespace) to its canonical key. Unknown names pass through as canonical custom
// fields (the normalized name), so nothing is lost.
func CanonicalVorbis(name string) tag.Key {
	norm := normalizeKey(name)
	if k, ok := tag.AliasKey(norm); ok {
		return k
	}
	return tag.Key(norm)
}

// ResolveAlias returns the canonical key for a recognized alternative Vorbis spelling
// (DATE/YEAR -> RECORDINGDATE, TOTALTRACKS -> TRACKTOTAL, ORGANIZATION -> LABEL,
// DISC -> DISCNUMBER, TRACK -> TRACKNUMBER, ALBUM ARTIST/ALBUM_ARTIST -> ALBUMARTIST,
// ...), or key unchanged when it is not an alias. It is case-insensitive for aliases and
// leaves non-alias keys otherwise untouched.
func ResolveAlias(key tag.Key) tag.Key {
	if k, ok := tag.AliasKey(string(key)); ok {
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
