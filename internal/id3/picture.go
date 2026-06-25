package id3

import (
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
)

// apicHeader cuts an APIC frame body into its header fields and the remaining image
// payload. The payload is returned as a sub-slice, not copied. Layout:
// encoding(1) MIME(Latin-1, NUL-terminated) type(1) description(encoded, terminated)
// data. It is the shared structural parse behind decodeAPIC (which clones and sniffs
// the payload) and validAPIC (which does neither).
func apicHeader(body []byte) (enc byte, mime string, ptype byte, desc string, rest []byte, ok bool) {
	if len(body) < 4 {
		return 0, "", 0, "", nil, false
	}
	enc = body[0]
	if !validEncoding(enc) {
		return 0, "", 0, "", nil, false
	}
	rest = body[1:]
	mime, rest, ok = cutLatin1(rest)
	if !ok || len(rest) < 1 {
		return 0, "", 0, "", nil, false
	}
	ptype = rest[0]
	rest = rest[1:]
	desc, rest, ok = cutEncoded(enc, rest)
	if !ok {
		return 0, "", 0, "", nil, false
	}
	return enc, mime, ptype, desc, rest, true
}

// decodeAPIC decodes an APIC frame body into a Picture. A malformed frame yields ok=false
// and is preserved opaque.
func decodeAPIC(body []byte) (core.Picture, bool) {
	_, mime, ptype, desc, rest, ok := apicHeader(body)
	if !ok {
		return core.Picture{}, false
	}
	p := core.Picture{
		Type:        core.PictureType(ptype),
		MIME:        normalizeMIME(mime),
		Description: desc,
		Data:        slices.Clone(rest),
	}
	p.SniffInto()
	return p, true
}

// validAPIC reports whether an APIC frame body is well-formed through its header without
// cloning or sniffing the potentially large image payload. RebuildFrames uses it to flag
// a malformed cover it is about to drop.
func validAPIC(body []byte) bool {
	_, _, _, _, _, ok := apicHeader(body)
	return ok
}

// encodeAPIC renders a Picture as an APIC frame body. The description encoding
// is chosen by content (Latin-1 when it fits, else UTF-8/UTF-16 by version);
// the MIME type is always Latin-1 per the spec.
func encodeAPIC(p core.Picture, version byte) []byte {
	enc := chooseEncoding(version, []string{p.Description})
	mime := p.MIME
	if mime == "" {
		mime = "image/"
	}
	out := []byte{enc}
	out = append(out, encodeLatin1(mime)...)
	out = append(out, 0)
	out = append(out, byte(p.Type))
	out = append(out, encodeString(enc, p.Description)...)
	out = append(out, term(enc)...)
	out = append(out, p.Data...)
	return out
}

// convertPICtoAPIC rewrites a v2.2 PIC body as a v2.3/v2.4 APIC body so the rest
// of the codec deals only with APIC. PIC differs only in carrying a three-letter
// image format ("PNG", "JPG") instead of a MIME string.
func convertPICtoAPIC(body []byte) []byte {
	if len(body) < 5 {
		return body
	}
	enc := body[0]
	format := string(body[1:4])
	ptype := body[4]
	rest := body[5:]
	desc, data, ok := cutEncoded(enc, rest)
	if !ok {
		desc, data = "", rest
	}
	out := []byte{enc}
	out = append(out, encodeLatin1(mimeForFormat(format))...)
	out = append(out, 0, ptype)
	out = append(out, encodeString(enc, desc)...)
	out = append(out, term(enc)...)
	out = append(out, data...)
	return out
}

// mimeForFormat maps a v2.2 three-letter image format to a MIME type.
func mimeForFormat(format string) string {
	switch strings.ToUpper(strings.TrimSpace(format)) {
	case "PNG":
		return "image/png"
	case "JPG", "JPEG":
		return "image/jpeg"
	case "GIF":
		return "image/gif"
	case "BMP":
		return "image/bmp"
	default:
		return "image/jpeg"
	}
}

// normalizeMIME tidies a stored MIME type. The "-->" sentinel (a URL link
// instead of embedded data) is preserved as-is.
func normalizeMIME(mime string) string {
	mime = strings.TrimSpace(mime)
	if mime == "" {
		return "image/"
	}
	return mime
}

// cutLatin1 reads a NUL-terminated Latin-1 string, returning it and the bytes
// after the terminator.
func cutLatin1(b []byte) (string, []byte, bool) {
	for i, c := range b {
		if c == 0 {
			return decodeLatin1(b[:i]), b[i+1:], true
		}
	}
	return "", nil, false
}

// cutEncoded reads a terminated string in the given encoding, returning it and
// the bytes after the terminator. A missing terminator consumes the rest (the
// description is the final terminated field before binary data, so a well-formed
// frame always has one).
func cutEncoded(enc byte, b []byte) (string, []byte, bool) {
	tl := termLen(enc)
	idx := indexTerm(b, tl)
	if idx < 0 {
		return "", b, false
	}
	return decodeString(enc, b[:idx]), b[idx+tl:], true
}
