package mp4

import (
	"encoding/binary"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// buildItems renders the edited canonical tags and pictures into ilst items,
// then appends the preserved items (unknown atoms and foreign freeforms kept
// verbatim) in their original order. The canonical items come first in a stable
// order derived from the tag set's key order, so the same input and edit produce
// the same bytes. Number/total pairs (trkn, disk) and pictures (covr) are
// special-cased; everything else is a text atom (known four-cc) or a freeform.
func buildItems(edited tag.TagSet, pics []core.Picture, preserved []item) []item {
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
		default:
			if len(vals) == 0 {
				continue // present-but-empty: nothing to store
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
