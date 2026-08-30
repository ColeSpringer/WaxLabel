package core

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/tag"
)

// SanitizeUTF8 returns valid UTF-8, replacing any invalid byte sequence with the Unicode
// replacement character. A reader that stores text best-effort from a non-conformant file -
// the Vorbis and Matroska parsers keep raw bytes the spec requires to be UTF-8 - runs its
// projected canonical values through it, so the model never carries invalid sequences: a copy
// of such a value is not spuriously rejected by the write-time UTF-8 guard, and --json never
// emits invalid bytes. It is a no-op on already-valid input. The shared helper keeps the
// ID3/Vorbis/Matroska read paths sanitizing identically.
func SanitizeUTF8(s string) string {
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

// IsTranscoderStamp reports whether s looks like an inherited transcoder/encoder
// stamp. ffmpeg's libavformat writes "Lavf<version>" and its libavcodec "Lavc<version>
// <codec>"; both describe the transcode that produced the file rather than the work, so both
// count and --strip-encoder / lint --fix remove either. It is the single predicate shared by
// every codec's encoder-noise check.
func IsTranscoderStamp(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "lavf") || strings.Contains(s, "libavformat") ||
		strings.Contains(s, "lavc") || strings.Contains(s, "libavcodec")
}

// IndefiniteArticle returns "a" or "an" to precede name, so an interpolated format
// name reads grammatically ("an AIFF file", not "a AIFF file"). It keys on the
// leading sound: a vowel letter takes "an", and so does an "MP" initialism (MP3/MP4
// read "em-pee-...", a vowel sound, despite the consonant 'M'). Everything else
// takes "a". This is correct for every format name WaxLabel interpolates - the
// vowel-initial ones (AIFF, AAC, Ogg), the "MP" pair, and the rest which read as
// written (FLAC, WAV, Matroska, WebM) - without a full pronunciation table. Use it
// wherever a message computes the article for a format name that varies at runtime
// (the chapter-unsupported message, the WebM cover refusal); a message with a
// single fixed, known format may spell the article inline.
func IndefiniteArticle(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "a"
	}
	// MP3/MP4 are the one case a leading-letter rule gets wrong: read letter by
	// letter ("em-pee"), they begin with a vowel sound. No "a"-taking format name
	// starts with "MP", so this prefix test is safe.
	if len(name) >= 2 && (name[0] == 'M' || name[0] == 'm') && (name[1] == 'P' || name[1] == 'p') {
		return "an"
	}
	switch name[0] {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return "an"
	}
	return "a"
}

// Fold normalizes a string for case- and space-insensitive comparison. It
// delegates to [tag.Fold] so the whole tree shares one fold rule (core imports
// tag, not the reverse): codecs that import core fold through this, and tag's own
// callers fold through tag.Fold directly.
func Fold(s string) string { return tag.Fold(s) }

// ContainsFold reports whether vals holds value, comparing case- and
// space-insensitively. It is the shared rule for deciding whether a secondary
// tag container agrees with the authoritative value.
func ContainsFold(vals []string, value string) bool {
	for _, v := range vals {
		if EqualFoldValue(v, value) {
			return true
		}
	}
	return false
}

// EqualFoldValue reports whether two tag values match under the family view's comparison:
// case-insensitive and ignoring surrounding space. [ContainsFold] and [FamilySelected] both
// resolve the rule here, so the slice form and the set form cannot come to disagree on what
// counts as the same value.
func EqualFoldValue(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// FoldValueKey returns the index key under which [EqualFoldValue] treats a value as equal:
// space-trimmed, with each rune replaced by the smallest rune in its simple-fold orbit.
// [strings.EqualFold] compares rune by rune over exactly those orbits and requires equal
// rune counts, so two values share this key precisely when EqualFoldValue reports them
// equal. It exists so [FamilySelector] can index instead of rescan; a caller comparing two
// values should use EqualFoldValue, which allocates nothing.
func FoldValueKey(s string) string {
	var b strings.Builder
	s = strings.TrimSpace(s)
	b.Grow(len(s))
	for _, r := range s {
		lo := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < lo {
				lo = f
			}
		}
		b.WriteRune(lo)
	}
	return b.String()
}

// FamilySelected reports whether a secondary tag container's value for key should
// be marked selected (i.e. not a conflict) in the family view: true unless the
// authoritative set carries key with a different value. Comparing against every
// authoritative value (not just the first) avoids falsely flagging a multi-value
// field - e.g. ID3v2 ARTIST=[A,B] against an ID3v1 artist of "B". Shared by the
// codecs that surface a secondary container (MP3's ID3v1/APE, WAV's INFO/id3).
func FamilySelected(auth tag.TagSet, key tag.Key, value string) bool {
	if !auth.Has(key) {
		return true
	}
	// Ask the set rather than take a copy of its values: this runs once per native item,
	// and a file whose items all map to one key would otherwise clone the whole value list
	// that many times.
	return auth.AnyValue(key, func(v string) bool { return EqualFoldValue(v, value) })
}

// FamilySelector answers [FamilySelected] over many values without rescanning auth for
// each one. A container's family view asks once per native item, so a file whose items all
// map to one key makes the scan quadratic: a crafted LIST/INFO at the element cap is tens
// of seconds of CPU for a parse that reads no audio. The per-key value index is built on
// the first question about that key, so an ordinary file with a handful of items pays only
// a map it never fills.
//
// It is exactly [FamilySelected], not an approximation: the index is keyed on
// [FoldValueKey], whose equality is [EqualFoldValue]'s by construction.
func FamilySelector(auth tag.TagSet) func(tag.Key, string) bool {
	index := map[tag.Key]map[string]bool{}
	return func(key tag.Key, value string) bool {
		if !auth.Has(key) {
			return true
		}
		keys, built := index[key]
		if !built {
			keys = map[string]bool{}
			auth.AnyValue(key, func(v string) bool {
				keys[FoldValueKey(v)] = true
				return false // never match: visit every value
			})
			index[key] = keys
		}
		return keys[FoldValueKey(value)]
	}
}
