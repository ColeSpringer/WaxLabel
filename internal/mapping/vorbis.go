// Package mapping translates between canonical [tag.Key]s and native tag names.
// This file covers Vorbis-comment names; other files cover ID3, MP4, RIFF, AIFF, and Matroska.
// Most keys are identity; value is read-side aliases ([tag.AliasKey]) and writePreferred.
package mapping

import "github.com/colespringer/waxlabel/tag"

// Read-side aliases live in [tag.AliasKey]; native bytes are preserved.

// writePreferred overrides native spelling on write. Unlisted keys write verbatim.
var writePreferred = map[tag.Key]string{
	tag.RecordingDate: "DATE",
}

// CanonicalVorbis maps a native Vorbis field name to its canonical key. Unknown names pass
// through uppercased.
func CanonicalVorbis(name string) tag.Key {
	norm := normalizeKey(name)
	if k, ok := tag.AliasKey(norm); ok {
		return k
	}
	return tag.Key(norm)
}

// ResolveAlias folds recognized alternative spellings onto canonical keys; unchanged otherwise.
func ResolveAlias(key tag.Key) tag.Key {
	if k, ok := tag.AliasKey(string(key)); ok {
		return k
	}
	return key
}

// VorbisName returns the native field name to write for a canonical key.
func VorbisName(key tag.Key) string {
	if name, ok := writePreferred[key]; ok {
		return name
	}
	return string(key)
}
