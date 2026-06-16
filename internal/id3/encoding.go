package id3

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ID3v2 text encodings. The encoding is the first byte of a text frame and
// selects how the rest of the frame (and any null terminators) is interpreted.
const (
	encLatin1  = 0 // ISO-8859-1, single-byte terminator
	encUTF16   = 1 // UTF-16 with a byte-order mark, two-byte terminator
	encUTF16BE = 2 // UTF-16 big-endian, no BOM (v2.4 only)
	encUTF8    = 3 // UTF-8 (v2.4 only)
)

// validEncoding reports whether b is a defined text-encoding byte.
func validEncoding(b byte) bool { return b <= encUTF8 }

// termLen is the width of a string terminator for an encoding: one byte for the
// single-byte encodings, two for the UTF-16 variants.
func termLen(enc byte) int {
	if enc == encUTF16 || enc == encUTF16BE {
		return 2
	}
	return 1
}

// decodeStrings interprets a text-frame payload (the bytes after the encoding
// byte) as one or more null-separated strings. ID3v2.4 allows multiple values
// in a single text frame; earlier versions officially allow one, but real files
// null-separate anyway, so splitting is safe across versions. A trailing
// terminator yields no extra empty value.
func decodeStrings(enc byte, data []byte) []string {
	tl := termLen(enc)
	var out []string
	for len(data) > 0 {
		idx := indexTerm(data, tl)
		if idx < 0 {
			out = append(out, decodeString(enc, data))
			break
		}
		out = append(out, decodeString(enc, data[:idx]))
		data = data[idx+tl:]
		// A terminator at the very end terminates the last value rather than
		// introducing an empty one.
		if len(data) == 0 {
			break
		}
	}
	if out == nil {
		out = []string{""}
	}
	return out
}

// indexTerm finds the first terminator of width tl. For the two-byte encodings
// the terminator must sit on an even offset so a 0x00 byte inside a code unit is
// not mistaken for it.
func indexTerm(data []byte, tl int) int {
	if tl == 1 {
		for i, b := range data {
			if b == 0 {
				return i
			}
		}
		return -1
	}
	for i := 0; i+1 < len(data); i += 2 {
		if data[i] == 0 && data[i+1] == 0 {
			return i
		}
	}
	return -1
}

// decodeString decodes a single string (no terminator) in the given encoding.
func decodeString(enc byte, b []byte) string {
	switch enc {
	case encUTF8:
		return sanitizeUTF8(b)
	case encUTF16:
		// A byte-order mark selects endianness and is then stripped.
		le := detectBOM(b)
		if hasBOM(b) {
			b = b[2:]
		}
		return decodeUTF16(b, le)
	case encUTF16BE:
		// No BOM in this encoding: a leading U+FEFF is a real character, not a mark.
		return decodeUTF16(b, false)
	default: // Latin-1
		return decodeLatin1(b)
	}
}

// detectBOM reports whether a UTF-16 string is little-endian per its byte-order
// mark, defaulting to big-endian when the mark is absent or malformed.
func detectBOM(b []byte) (littleEndian bool) {
	if len(b) >= 2 {
		if b[0] == 0xFF && b[1] == 0xFE {
			return true
		}
		if b[0] == 0xFE && b[1] == 0xFF {
			return false
		}
	}
	return false
}

// hasBOM reports whether b begins with a UTF-16 byte-order mark.
func hasBOM(b []byte) bool {
	return len(b) >= 2 && (b[0] == 0xFF && b[1] == 0xFE || b[0] == 0xFE && b[1] == 0xFF)
}

// decodeUTF16 decodes UTF-16 code units in the given endianness. It does not
// strip a byte-order mark; the caller does that only for the BOM-bearing
// encoding so a genuine leading U+FEFF in UTF-16BE survives.
func decodeUTF16(b []byte, littleEndian bool) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if littleEndian {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		} else {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
	}
	return string(utf16.Decode(u))
}

func decodeLatin1(b []byte) string {
	// Every byte is a Unicode code point in [0,255]; widen to runes.
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// sanitizeUTF8 returns valid UTF-8, replacing any invalid byte sequence with the
// Unicode replacement character so a malformed frame cannot inject invalid
// sequences into the model.
func sanitizeUTF8(b []byte) string {
	return strings.ToValidUTF8(string(b), string(utf8.RuneError))
}

// encodeString encodes s (without a terminator) in the given encoding.
func encodeString(enc byte, s string) []byte {
	switch enc {
	case encUTF8:
		return []byte(s)
	case encUTF16:
		return encodeUTF16LE(s) // little-endian with BOM
	case encUTF16BE:
		return encodeUTF16BE(s)
	default:
		return encodeLatin1(s)
	}
}

// encodeUTF16LE encodes s as little-endian UTF-16 prefixed with a byte-order
// mark (the encUTF16 form).
func encodeUTF16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+len(u)*2)
	out = append(out, 0xFF, 0xFE) // BOM
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

// encodeUTF16BE encodes s as big-endian UTF-16 with no byte-order mark (the
// encUTF16BE form).
func encodeUTF16BE(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(u)*2)
	for _, c := range u {
		out = append(out, byte(c>>8), byte(c))
	}
	return out
}

func encodeLatin1(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			out = append(out, '?')
			continue
		}
		out = append(out, byte(r))
	}
	return out
}

// latin1able reports whether s is fully representable in ISO-8859-1.
func latin1able(s string) bool {
	for _, r := range s {
		if r > 0xFF {
			return false
		}
	}
	return true
}

// chooseEncoding picks the text encoding for re-rendering values under a write
// version: Latin-1 when every value fits (compact and maximally compatible),
// else UTF-8 for v2.4 or UTF-16 for v2.3 (which has no UTF-8). Unchanged frames
// keep their original bytes; this only governs frames we re-render.
func chooseEncoding(version byte, values []string) byte {
	allLatin1 := true
	for _, v := range values {
		if !latin1able(v) {
			allLatin1 = false
			break
		}
	}
	if allLatin1 {
		return encLatin1
	}
	if version >= 4 {
		return encUTF8
	}
	return encUTF16
}

// encodeTextFrame renders a text-frame body: the encoding byte followed by the
// values joined by the encoding's terminator (no trailing terminator).
func encodeTextFrame(enc byte, values []string) []byte {
	return appendValues([]byte{enc}, enc, values)
}

// term returns the terminator bytes for an encoding.
func term(enc byte) []byte {
	if termLen(enc) == 2 {
		return []byte{0, 0}
	}
	return []byte{0}
}
