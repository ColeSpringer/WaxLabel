package id3

import (
	"strconv"
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// V1 is a decoded ID3v1 / ID3v1.1 tag — the fixed 128-byte trailer. It is read
// for the family view and preserved verbatim; ID3v2 is authoritative.
type V1 struct {
	Title   string
	Artist  string
	Album   string
	Year    string
	Comment string
	Track   int    // ID3v1.1 only (0 if absent)
	Genre   string // resolved name, or "" if unset/unknown
}

// ParseV1 decodes a 128-byte ID3v1 trailer. ok is false if b is not a tag.
func ParseV1(b []byte) (*V1, bool) {
	if len(b) != 128 || string(b[:3]) != "TAG" {
		return nil, false
	}
	v := &V1{
		Title:  v1Field(b[3:33]),
		Artist: v1Field(b[33:63]),
		Album:  v1Field(b[63:93]),
		Year:   v1Field(b[93:97]),
	}
	comment := b[97:127]
	// ID3v1.1 stores the track number in the final two comment bytes when the
	// 29th is zero and the 30th is not.
	if comment[28] == 0 && comment[29] != 0 {
		v.Track = int(comment[29])
		comment = comment[:28]
	}
	v.Comment = v1Field(comment)
	if g, ok := genreName(int(b[127])); ok {
		v.Genre = g
	}
	return v, true
}

// v1Field decodes a fixed-width Latin-1 field, trimming trailing NULs and
// spaces.
func v1Field(b []byte) string {
	s := decodeLatin1(b)
	return strings.TrimRight(s, "\x00 ")
}

// Pairs returns the canonical key/value pairs ID3v1 supplies, in a stable order,
// skipping empty fields. Used to build the family/source view.
func (v *V1) Pairs() []struct {
	Key   tag.Key
	Value string
} {
	type kv = struct {
		Key   tag.Key
		Value string
	}
	var out []kv
	add := func(k tag.Key, val string) {
		if val != "" {
			out = append(out, kv{k, val})
		}
	}
	add(tag.Title, v.Title)
	add(tag.Artist, v.Artist)
	add(tag.Album, v.Album)
	add(tag.RecordingDate, v.Year)
	add(tag.Comment, v.Comment)
	add(tag.Genre, v.Genre)
	if v.Track > 0 {
		add(tag.TrackNumber, strconv.Itoa(v.Track))
	}
	return out
}
