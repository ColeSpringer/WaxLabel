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
// terminator yields no extra empty value, and any trailing empties left by
// padding terminators (a frame ending in two or more NULs, as some foreign
// encoders write) are stripped too, matching TagLib/mutagen; an all-terminator
// frame still decodes to a single empty value.
func decodeStrings(enc byte, data []byte) []string {
	// A text frame is one frame: share byte-order state across its values so a UTF-16 BOM on
	// the first value also applies to later values that omit a BOM.
	return decodeStringsTracked(enc, data, &utf16Order{})
}

// decodeStringsTracked is [decodeStrings] with byte-order state shared by the surrounding
// frame, such as a COMM or TXXX descriptor decoded before these values.
func decodeStringsTracked(enc byte, data []byte, order *utf16Order) []string {
	tl := termLen(enc)
	var out []string
	for len(data) > 0 {
		idx := indexTerm(data, tl)
		if idx < 0 {
			out = append(out, decodeStringTracked(enc, data, order))
			break
		}
		out = append(out, decodeStringTracked(enc, data[:idx], order))
		data = data[idx+tl:]
		// A terminator at the very end terminates the last value rather than
		// introducing an empty one.
		if len(data) == 0 {
			break
		}
	}
	// Strip trailing empties produced by padding terminators (a frame ending in a
	// double NUL decodes to [..., ""]). Trailing-only: an interior present-empty value
	// in a genuine multi-value frame is preserved, and the len>1 floor keeps a lone ""
	// for an all-terminator frame.
	for len(out) > 1 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
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

// decodeString decodes one unterminated string in the given encoding. A standalone string
// gets fresh byte-order state, so its UTF-16 endianness is decided by its own BOM and
// defaults to big-endian when no BOM is present.
func decodeString(enc byte, b []byte) string {
	return decodeStringTracked(enc, b, &utf16Order{})
}

// decodeStringTracked decodes one string, consulting and updating shared UTF-16 byte-order
// state for encUTF16. encUTF16BE is always big-endian, and the single-byte encodings ignore
// the tracker.
func decodeStringTracked(enc byte, b []byte, order *utf16Order) string {
	switch enc {
	case encUTF8:
		return sanitizeUTF8(b)
	case encUTF16:
		// This string's own BOM wins. Without a BOM, use the order carried from earlier in
		// the frame. A present BOM is stripped before decoding.
		le := order.resolve(b)
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

// utf16Order tracks byte order across the UTF-16 segments of one ID3 frame. A frame may put
// a BOM on the descriptor or first value and omit it from later values. The default remains
// big-endian for a lone BOM-less string.
type utf16Order struct {
	known bool // whether a BOM has set the running order yet
	le    bool // the running order: true little-endian, false big-endian
}

// resolve returns the byte order for b and updates the running order when b starts with a
// BOM. A BOM applies to its own string and becomes the default for later BOM-less strings.
// The caller strips the BOM after calling resolve.
func (o *utf16Order) resolve(b []byte) (littleEndian bool) {
	if len(b) >= 2 {
		if b[0] == 0xFF && b[1] == 0xFE {
			o.known, o.le = true, true
			return true
		}
		if b[0] == 0xFE && b[1] == 0xFF {
			o.known, o.le = true, false
			return false
		}
	}
	return o.known && o.le
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
