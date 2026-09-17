package core

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/tag"
)

// SanitizeUTF8 replaces invalid UTF-8 with U+FFFD. No-op on valid input.
func SanitizeUTF8(s string) string {
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

// IsTranscoderStamp reports Lavf/Lavc/libav* encoder stamps from transcodes.
func IsTranscoderStamp(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "lavf") || strings.Contains(s, "libavformat") ||
		strings.Contains(s, "lavc") || strings.Contains(s, "libavcodec")
}

// IndefiniteArticle returns "a" or "an" before a format name. MP3/MP4 use "an".
func IndefiniteArticle(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "a"
	}
	// MP* names read "em-pee" (vowel sound).
	if len(name) >= 2 && (name[0] == 'M' || name[0] == 'm') && (name[1] == 'P' || name[1] == 'p') {
		return "an"
	}
	switch name[0] {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return "an"
	}
	return "a"
}

// Fold delegates to [tag.Fold] for shared case/space-insensitive compare.
func Fold(s string) string { return tag.Fold(s) }

// ContainsFold reports whether vals holds value under fold compare.
func ContainsFold(vals []string, value string) bool {
	for _, v := range vals {
		if EqualFoldValue(v, value) {
			return true
		}
	}
	return false
}

// EqualFoldValue compares values case-insensitively with trimmed space.
func EqualFoldValue(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// FoldValueKey is the index key for [EqualFoldValue]. Used by [FamilySelector].
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

// FamilySelected reports whether a secondary value matches any authoritative value for key.
func FamilySelected(auth tag.TagSet, key tag.Key, value string) bool {
	if !auth.Has(key) {
		return true
	}
	// AnyValue avoids cloning the full value list per native item.
	return auth.AnyValue(key, func(v string) bool { return EqualFoldValue(v, value) })
}

// FamilySelector caches [FamilySelected] per key via [FoldValueKey] index.
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
				return false
			})
			index[key] = keys
		}
		return keys[FoldValueKey(value)]
	}
}
