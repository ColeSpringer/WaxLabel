package core

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// IsTranscoderStamp reports whether s looks like an inherited transcoder/encoder
// stamp. ffmpeg's libavformat writes "Lavf<version>", the typical signature of a
// file produced by transcoding; surfacing it lets a tagger dedupe the noise. It
// is the single predicate shared by every codec's encoder-noise check.
func IsTranscoderStamp(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "lavf") || strings.Contains(s, "libavformat")
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

// Fold normalizes a string for case- and space-insensitive comparison
// (lowercased, surrounding whitespace trimmed). It is the one place the
// normalization rule lives for codecs that import core; tag/* cannot use it (core
// imports tag, not the reverse).
func Fold(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ContainsFold reports whether vals holds value, comparing case- and
// space-insensitively. It is the shared rule for deciding whether a secondary
// tag container agrees with the authoritative value.
func ContainsFold(vals []string, value string) bool {
	value = strings.TrimSpace(value)
	for _, v := range vals {
		if strings.EqualFold(strings.TrimSpace(v), value) {
			return true
		}
	}
	return false
}

// FamilySelected reports whether a secondary tag container's value for key should
// be marked selected (i.e. not a conflict) in the family view: true unless the
// authoritative set carries key with a different value. Comparing against every
// authoritative value (not just the first) avoids falsely flagging a multi-value
// field - e.g. ID3v2 ARTIST=[A,B] against an ID3v1 artist of "B". Shared by the
// codecs that surface a secondary container (MP3's ID3v1/APE, WAV's INFO/id3).
func FamilySelected(auth tag.TagSet, key tag.Key, value string) bool {
	avs, ok := auth.Get(key)
	return !ok || ContainsFold(avs, value)
}
