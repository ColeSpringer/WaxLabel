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
// field — e.g. ID3v2 ARTIST=[A,B] against an ID3v1 artist of "B". Shared by the
// codecs that surface a secondary container (MP3's ID3v1/APE, WAV's INFO/id3).
func FamilySelected(auth tag.TagSet, key tag.Key, value string) bool {
	avs, ok := auth.Get(key)
	return !ok || ContainsFold(avs, value)
}
