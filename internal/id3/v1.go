package id3

import (
	"strconv"
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// V1 is a decoded ID3v1 / ID3v1.1 tag - the fixed 128-byte trailer. It is read
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

// LooksLikeID3v1 reports whether b is very likely a genuine 128-byte ID3v1/v1.1
// trailer rather than audio bytes that merely begin with "TAG" at size-128. The
// bare 3-byte magic is far too weak to gate an essence boundary: a false positive
// pulls the audio end back 128 bytes and drops real audio from the essence digest and
// structural fingerprint. This adds cheap structural checks that every real
// Latin-1/Windows-1252 tag passes but a random audio tail almost never does - the
// year field is digits/space/NUL, and the text fields carry no binary control bytes -
// so the essence-affecting detection path can require them while the lenient display
// path (ParseV1) keeps showing an already-detected tag.
//
// The strict year is a deliberate trade-off: a genuine but non-standard year at the
// trailing position (a legacy writer's "90s" or "200?") is not auto-detected here and its
// 128 bytes are treated as audio. That rare miss is preferred over the far more common
// false positive - four printable-ASCII audio bytes landing in the year field - which would
// silently pull the essence boundary back. ParseV1 stays lenient for the display path, so a
// tag detected by other means still renders such a year.
func LooksLikeID3v1(b []byte) bool {
	if len(b) != 128 || string(b[:3]) != "TAG" {
		return false
	}
	// Year "YYYY": ASCII digits, space, or NUL padding.
	for _, c := range b[93:97] {
		if !(c >= '0' && c <= '9') && c != 0x20 && c != 0x00 {
			return false
		}
	}
	// Title, Artist, Album (contiguous b[3:93]): no binary control bytes.
	for _, c := range b[3:93] {
		if !v1TextByte(c) {
			return false
		}
	}
	// Comment b[97:127], with the ID3v1.1 carve-out: ParseV1 reads b[126] as a binary
	// track byte when b[125]==0 && b[126]!=0 (values like 3, 5, 7 fall in the rejected
	// control range), so validate only the 28-byte comment text there and leave the
	// track byte unchecked; otherwise the full 30-byte comment is text.
	commentEnd := 127
	if b[125] == 0 && b[126] != 0 {
		commentEnd = 125
	}
	for _, c := range b[97:commentEnd] {
		if !v1TextByte(c) {
			return false
		}
	}
	return true
}

// v1TextByte reports whether c is acceptable in an ID3v1 Latin-1 text field. It
// rejects only the C0/C1-style binary control bytes a real tag never contains,
// allowing NUL/space padding, tab/CR/LF, printable ASCII, and every high-bit
// Latin-1 byte (0x80-0xFF).
func v1TextByte(c byte) bool {
	switch {
	case c >= 1 && c <= 8:
		return false
	case c == 11 || c == 12:
		return false
	case c >= 14 && c <= 31:
		return false
	case c == 127:
		return false
	}
	return true
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
