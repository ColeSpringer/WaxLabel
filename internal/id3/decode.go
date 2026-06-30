package id3

import (
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// DecodeText decodes a plain text frame's value(s), for callers outside the
// package that need a frame's textual content (e.g. encoder-noise detection). It
// returns nil for non-text, user-defined (TXXX), or opaque frames.
func DecodeText(f Frame) []string {
	if f.Opaque || f.ID == "TXXX" || len(f.ID) == 0 || f.ID[0] != 'T' {
		return nil
	}
	return decodeTextFrame(f.Body)
}

// decodeTextFrame decodes a text frame body (encoding byte + values) into one or
// more strings.
func decodeTextFrame(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	enc := body[0]
	if !validEncoding(enc) {
		enc = encLatin1
	}
	return decodeStrings(enc, body[1:])
}

// decodeUserText decodes a TXXX frame: encoding, a description, then the
// value(s). It returns the description and values; the caller maps the
// description to a canonical key.
func decodeUserText(body []byte) (desc string, vals []string, ok bool) {
	if len(body) < 1 {
		return "", nil, false
	}
	enc := body[0]
	if !validEncoding(enc) {
		return "", nil, false
	}
	// Share byte-order state across the descriptor and values. Some foreign frames put a
	// UTF-16 BOM only on the descriptor, then omit it from the values.
	order := &utf16Order{}
	desc, rest, ok := cutEncodedTracked(enc, body[1:], order)
	if !ok {
		return "", nil, false
	}
	return desc, decodeStringsTracked(enc, rest, order), true
}

// decodeUFID decodes a UFID frame: an owner identifier (Latin-1, terminated)
// followed by the raw identifier bytes.
func decodeUFID(body []byte) (owner, id string, ok bool) {
	owner, rest, ok := cutLatin1(body)
	if !ok {
		return "", "", false
	}
	return owner, string(rest), true
}

// decodeCommentFrame decodes a COMM frame: encoding, a 3-byte language, a short
// description, then the comment text (possibly multi-value).
func decodeCommentFrame(body []byte) (desc string, vals []string, ok bool) {
	if len(body) < 4 {
		return "", nil, false
	}
	enc := body[0]
	if !validEncoding(enc) {
		return "", nil, false
	}
	// Share byte-order state across the descriptor and values. Some foreign frames put a
	// UTF-16 BOM only on the descriptor, then omit it from the values.
	order := &utf16Order{}
	desc, rest, ok := cutEncodedTracked(enc, body[4:], order) // skip the language bytes
	if !ok {
		return "", nil, false
	}
	return desc, decodeStringsTracked(enc, rest, order), true
}

// decodeLangText decodes a USLT frame: encoding, a 3-byte language, a content
// descriptor, then the lyrics text (kept whole, newlines preserved).
func decodeLangText(body []byte) (desc, text string, ok bool) {
	if len(body) < 4 {
		return "", "", false
	}
	enc := body[0]
	if !validEncoding(enc) {
		return "", "", false
	}
	// Share byte-order state across the descriptor and text. Some foreign frames put a
	// UTF-16 BOM only on the descriptor, then omit it from the text.
	order := &utf16Order{}
	desc, rest, ok := cutEncodedTracked(enc, body[4:], order)
	if !ok {
		return "", "", false
	}
	return desc, decodeStringTracked(enc, rest, order), true
}

// isDateFrame reports whether id is one of the date frames the projector
// composes into the canonical date keys.
func isDateFrame(id string) bool {
	switch id {
	case "TYER", "TDAT", "TIME", "TORY", "TDRC", "TDRL", "TDOR":
		return true
	}
	return false
}

// dateParts accumulates the date frames of either version so they can be
// composed into the three canonical date keys after the frame walk.
type dateParts struct {
	tyer, tdat, ttime, tory string // v2.3
	tdrc, tdrl, tdor        string // v2.4
}

func (d *dateParts) add(id string, vals []string) {
	v := ""
	if len(vals) > 0 {
		v = vals[0]
	}
	switch id {
	case "TYER":
		d.tyer = v
	case "TDAT":
		d.tdat = v
	case "TIME":
		d.ttime = v
	case "TORY":
		d.tory = v
	case "TDRC":
		d.tdrc = v
	case "TDRL":
		d.tdrl = v
	case "TDOR":
		d.tdor = v
	}
}

func (d *dateParts) emit(emit func(tag.Key, string, string)) {
	switch {
	case d.tdrc != "":
		emit(tag.RecordingDate, d.tdrc, "TDRC")
	case d.tyer != "":
		emit(tag.RecordingDate, composeV23Date(d.tyer, d.tdat, d.ttime), "TYER")
	}
	if d.tdrl != "" {
		emit(tag.ReleaseDate, d.tdrl, "TDRL")
	}
	switch {
	case d.tdor != "":
		emit(tag.OriginalDate, d.tdor, "TDOR")
	case d.tory != "":
		emit(tag.OriginalDate, d.tory, "TORY")
	}
}

// composeV23Date assembles an ISO-8601 date from the v2.3 TYER (YYYY), TDAT
// (DDMM), and TIME (HHMM) frames, including only the parts that are present and
// well-formed.
func composeV23Date(year, ddmm, hhmm string) string {
	year = strings.TrimSpace(year)
	if len(year) != 4 || !allDigits(year) {
		return year
	}
	out := year
	if len(ddmm) == 4 && allDigits(ddmm) {
		dd, mm := ddmm[0:2], ddmm[2:4]
		out += "-" + mm + "-" + dd
		if len(hhmm) == 4 && allDigits(hhmm) {
			out += "T" + hhmm[0:2] + ":" + hhmm[2:4]
		}
	}
	return out
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}
