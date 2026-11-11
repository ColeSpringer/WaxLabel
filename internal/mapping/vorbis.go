// Package mapping translates between canonical [tag.Key]s and native tag names.
// This file covers Vorbis-comment names; other files cover ID3, MP4, RIFF, AIFF, and Matroska.
// Most keys are identity; value is read-side aliases ([tag.AliasKey]) and writePreferred.
package mapping

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// Read-side aliases live in [tag.AliasKey]; native bytes are preserved.

// writePreferred overrides native spelling on write. Unlisted keys write verbatim.
var writePreferred = map[tag.Key]string{
	tag.RecordingDate: "DATE",
}

// CanonicalVorbis maps a native Vorbis field name to its canonical key: ASCII letters uppercased
// and aliases folded, an unknown name otherwise as spelled. A name with a byte outside ASCII or
// whitespace at an edge is no key's spelling and maps to "", which is no valid key: trimming it,
// or folding its case as Unicode does (a dotless i, a long s), would read a field there that no
// other reader of the file sees.
func CanonicalVorbis(name string) tag.Key {
	if name != strings.TrimSpace(name) || strings.ContainsFunc(name, func(r rune) bool { return r >= 0x80 }) {
		return ""
	}
	norm := strings.ToUpper(name)
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
