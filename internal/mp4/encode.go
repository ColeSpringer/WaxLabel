package mp4

import (
	"encoding/binary"
	"strconv"
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// buildItems renders the edited canonical tags and pictures into ilst items,
// then appends the preserved items (unknown atoms and foreign freeforms kept
// verbatim) in their original order. The canonical items come first in a stable
// order derived from the tag set's key order, so the same input and edit produce
// the same bytes. Number/total pairs (trkn, disk) and pictures (covr) are
// special-cased; everything else is a text atom (known four-cc) or a freeform.
func buildItems(edited tag.TagSet, pics []core.Picture, preserved []item, numericGenre bool) []item {
	var out []item
	consumed := map[tag.Key]bool{}

	for _, key := range edited.Keys() {
		if consumed[key] {
			continue
		}
		vals, _ := edited.Get(key)
		switch key {
		case tag.TrackNumber, tag.TrackTotal:
			consumed[tag.TrackNumber] = true
			consumed[tag.TrackTotal] = true
			if it, ok := pairItem("trkn", edited, tag.TrackNumber, tag.TrackTotal, true); ok {
				out = append(out, it)
			}
		case tag.DiscNumber, tag.DiscTotal:
			consumed[tag.DiscNumber] = true
			consumed[tag.DiscTotal] = true
			if it, ok := pairItem("disk", edited, tag.DiscNumber, tag.DiscTotal, false); ok {
				out = append(out, it)
			}
		case tag.Compilation:
			if it, ok := boolItem("cpil", vals); ok {
				out = append(out, it)
			}
		case tag.MediaType:
			if it, ok := mediaTypeItem(vals); ok {
				out = append(out, it)
			}
		default:
			if len(vals) == 0 {
				continue // present-but-empty: nothing to store
			}
			// Numeric genre: emit the legacy "gnre" atom (a 1-based ID3v1 index) only
			// when every value resolves to a standard genre; otherwise the whole field
			// falls through to the text "\xa9gen" atom so an order-preserving mix of
			// standard and custom genres is kept verbatim.
			if key == tag.Genre && numericGenre {
				if indices, ok := allGenreIndices(vals); ok {
					out = append(out, gnreItem(indices))
					continue
				}
			}
			if name, ok := mapping.MP4KeyText(key); ok {
				out = append(out, textItem(atomName(name), vals))
			} else {
				out = append(out, freeformItem(mapping.MP4KeyFreeform(key), vals))
			}
		}
	}

	if len(pics) > 0 {
		out = append(out, coverItem(pics))
	}
	out = append(out, preserved...)
	return out
}

// textItem builds a text item: one UTF-8 data atom per value.
func textItem(name [4]byte, vals []string) item {
	var payload []byte
	for _, v := range vals {
		payload = append(payload, renderData(typeUTF8, []byte(v))...)
	}
	return item{name: name, payload: payload}
}

// allGenreIndices resolves every value to its 0-based ID3v1 genre index, reporting
// false if any value is not a standard genre (so the field stays a text atom). It
// requires at least one value, mirroring the present-but-empty skip in buildItems.
func allGenreIndices(vals []string) ([]int, bool) {
	if len(vals) == 0 {
		return nil, false
	}
	indices := make([]int, 0, len(vals))
	for _, v := range vals {
		idx := id3.GenreIndex(v)
		if idx < 0 {
			return nil, false
		}
		indices = append(indices, idx)
	}
	return indices, true
}

// gnreItem builds the legacy numeric-genre item: one implicit-type (class 0) data
// atom per genre holding the 1-based ID3v1 index as a big-endian uint16, the exact
// form decodeGnre reads back (id3.GenreName(n-1)). The implicit type is what makes it
// a classic "gnre" rather than a UTF-8 text atom.
func gnreItem(indices []int) item {
	var payload []byte
	for _, idx := range indices {
		var v [2]byte
		binary.BigEndian.PutUint16(v[:], uint16(idx+1))
		payload = append(payload, renderData(typeImplicit, v[:])...)
	}
	return item{name: atomName("gnre"), payload: payload}
}

// freeformItem builds a "----" freeform item under the com.apple.iTunes mean.
func freeformItem(name string, vals []string) item {
	payload := renderLabel("mean", itunesMean)
	payload = append(payload, renderLabel("name", name)...)
	for _, v := range vals {
		payload = append(payload, renderData(typeUTF8, []byte(v))...)
	}
	return item{name: atomName("----"), payload: payload}
}

// pairItem builds a trkn/disk number/total atom. trkn carries a trailing
// reserved 16 bits (8-byte value); disk does not (6-byte value), matching iTunes.
func pairItem(name string, ts tag.TagSet, numKey, totKey tag.Key, trailing bool) (item, bool) {
	num, total := numTotal(ts, numKey, totKey)
	if num == 0 && total == 0 {
		return item{}, false
	}
	n := 8
	if !trailing {
		n = 6
	}
	v := make([]byte, n)
	binary.BigEndian.PutUint16(v[2:4], num)
	binary.BigEndian.PutUint16(v[4:6], total)
	return item{name: atomName(name), payload: renderData(typeImplicit, v)}, true
}

// mediaTypeItem builds the iTunes "stik" media-kind atom from the canonical
// MediaType value (a decimal integer). The value is written in the minimal big-
// endian width that holds it (1, 2, or 4 bytes), so any stik that parsed in
// round-trips rather than being dropped; a non-numeric or out-of-range value is
// skipped.
func mediaTypeItem(vals []string) (item, bool) {
	if len(vals) == 0 {
		return item{}, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(vals[0]), 10, 32)
	if err != nil {
		return item{}, false
	}
	var v []byte
	switch {
	case n <= 0xFF:
		v = []byte{byte(n)}
	case n <= 0xFFFF:
		v = []byte{byte(n >> 8), byte(n)}
	default:
		v = []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
	return item{name: atomName("stik"), payload: renderData(typeSignedInt, v)}, true
}

// boolItem builds a single-byte boolean atom (cpil) from a canonical boolean
// value (parsed the same way as the typed projection, so "TRUE"/" yes " agree).
func boolItem(name string, vals []string) (item, bool) {
	if len(vals) == 0 {
		return item{}, false
	}
	b := byte(0)
	if tag.ParseBool(vals[0]) {
		b = 1
	}
	return item{name: atomName(name), payload: renderData(typeSignedInt, []byte{b})}, true
}

// coverItem builds a covr atom with one image data atom per picture.
func coverItem(pics []core.Picture) item {
	var payload []byte
	for _, p := range pics {
		payload = append(payload, renderData(coverType(p.MIME), p.Data)...)
	}
	return item{name: atomName("covr"), payload: payload}
}

// numTotal resolves the number/total a trkn/disk atom encodes, reusing the
// canonical pair parser (which tolerates a slash-combined "n/total" or stray
// spaces in the number field, with an explicit total key winning) and clamping to
// the 16-bit range the atom stores.
func numTotal(ts tag.TagSet, numKey, totKey tag.Key) (num, total uint16) {
	numStr, _ := ts.First(numKey)
	totStr, _ := ts.First(totKey)
	n, tot := tag.ParseNumPair(numStr, totStr)
	return clampUint16(n), clampUint16(tot)
}

// clampUint16 returns n as a uint16, or 0 if it is out of range.
func clampUint16(n int) uint16 {
	if n < 0 || n > 0xFFFF {
		return 0
	}
	return uint16(n)
}

// droppedValue records a canonical (key, value) the iTunes atom encoders cannot
// represent and would otherwise silently drop.
type droppedValue struct {
	Key   tag.Key
	Value string
}

// droppedValues returns the canonical numeric values this edit would lose at the
// iTunes encode site: a trkn/disk slot outside the uint16 the atom holds
// (numTotal -> clampUint16), or a stik media kind strconv.ParseUint cannot read
// (mediaTypeItem). It reads the same raw canonical strings those encoders consume -
// so it names exactly what buildItems drops and cannot desync from it - and treats a
// literal 0 (the pair encoder's "absent") and an absent/empty slot as no loss. The
// encoders stay the authority on the written value; this is only which were lost.
func droppedValues(ts tag.TagSet) []droppedValue {
	var out []droppedValue
	out = appendDroppedPair(out, ts, tag.TrackNumber, tag.TrackTotal)
	out = appendDroppedPair(out, ts, tag.DiscNumber, tag.DiscTotal)
	// MEDIATYPE (stik) shares its representability rule with the linter/note: reuse
	// tag.ValidMediaTypeValue so the encoder's drop and the validator cannot disagree on
	// what a storable stik value is. A present-but-empty value is not a drop (it stores
	// nothing by design), so skip it before the validity check.
	if v, ok := ts.First(tag.MediaType); ok && strings.TrimSpace(v) != "" && !tag.ValidMediaTypeValue(tag.MediaType, v) {
		out = append(out, droppedValue{Key: tag.MediaType, Value: strings.TrimSpace(v)})
	}
	// COMPILATION (cpil) is a single boolean byte, so a non-boolean value ("2",
	// "maybe") cannot be represented: boolItem coerces it to false (0) and the user's
	// distinct value is silently lost. Flag it the same way, reusing tag.ValidBooleanValue
	// so the encoder's coercion and the set-time malformed-value note agree on what a
	// storable boolean is. A recognized spelling ("0"/"true"/...) stores faithfully.
	if v, ok := ts.First(tag.Compilation); ok && strings.TrimSpace(v) != "" && !tag.ValidBooleanValue(tag.Compilation, v) {
		out = append(out, droppedValue{Key: tag.Compilation, Value: strings.TrimSpace(v)})
	}
	return out
}

// appendDroppedPair adds the dropped slot(s) of one trkn/disk pair. It mirrors
// numTotal/ParseNumPair's slot resolution exactly - a combined "n/total" in the
// number field feeds the total slot, an explicit total key overrides it - so it
// names the same slots the encoder reads. TRACKNUMBER and TRACKTOTAL share one trkn
// atom, so each slot is judged against its own source string (TRACKTOTAL=abc names
// TRACKTOTAL, not the merged pair).
func appendDroppedPair(out []droppedValue, ts tag.TagSet, numKey, totKey tag.Key) []droppedValue {
	numStr, _ := ts.First(numKey)
	totStr, _ := ts.First(totKey)
	// Resolve the two slots exactly as the encoder's tag.ParseNumPair does: split the
	// number field on "/" with the shared tag.SplitNumberTotal, then let a present (raw
	// non-empty) explicit total key override the "/total" tail. The override gates on the
	// raw string, not the trimmed one - matching ParseNumPair - so a whitespace-only total
	// (which ParseNumPair reads as 0, discarding any tail) overrides here too and does not
	// leave a stale tail this would misreport as dropped.
	numPart, totPart := tag.SplitNumberTotal(numStr)
	if totStr != "" {
		totPart = strings.TrimSpace(totStr)
	}
	if uint16ValueDropped(numPart) {
		out = append(out, droppedValue{Key: numKey, Value: numPart})
	}
	if uint16ValueDropped(totPart) {
		out = append(out, droppedValue{Key: totKey, Value: totPart})
	}
	return out
}

// uint16ValueDropped reports whether the trimmed slot string holds a value the
// uint16 trkn/disk atom cannot represent: a non-numeric value, a negative, or one
// past 65535. An empty/absent slot is not a drop, and neither is a literal 0 - the
// pair encoder treats 0 as "absent" (pairItem), so flagging it would be wrong.
func uint16ValueDropped(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return true
	}
	return n < 0 || n > 0xFFFF
}

// renderData builds a "data" sub-atom: [size]["data"][version=0][type:24][locale=0][value].
func renderData(typ uint32, value []byte) []byte {
	b := make([]byte, 16+len(value))
	binary.BigEndian.PutUint32(b[0:4], uint32(16+len(value)))
	copy(b[4:8], "data")
	binary.BigEndian.PutUint32(b[8:12], typ&0x00FFFFFF) // version 0 in the high byte
	copy(b[16:], value)
	return b
}

// renderLabel builds a "mean" or "name" FullBox: [size][label][version/flags=0][text].
func renderLabel(label, text string) []byte {
	b := make([]byte, 12+len(text))
	binary.BigEndian.PutUint32(b[0:4], uint32(12+len(text)))
	copy(b[4:8], label)
	copy(b[12:], text)
	return b
}

// itemBytes renders a full ilst item atom from its name and payload.
func itemBytes(it item) []byte {
	return renderAtom(it.name, it.payload)
}

// renderAtom wraps a payload in an atom header: [size][name][payload].
func renderAtom(name [4]byte, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], name[:])
	copy(b[8:], payload)
	return b
}
