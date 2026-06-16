package mp4

import (
	"encoding/binary"
	"strconv"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// itunesMean is the reverse-DNS "mean" under which Picard and friends store the
// freeform MusicBrainz/ReplayGain long tail. Freeform atoms with any other mean
// are foreign and preserved verbatim rather than projected.
const itunesMean = "com.apple.iTunes"

// Well-known "data" atom type codes (the 24-bit type field).
const (
	typeImplicit  = 0
	typeUTF8      = 1
	typeJPEG      = 13
	typePNG       = 14
	typeBMP       = 27
	typeSignedInt = 21
)

// dataAtom is one decoded "data" sub-atom: its well-known type and value bytes.
type dataAtom struct {
	typ   uint32
	value []byte
}

// itemResult is what decoding one ilst item yields: canonical contributions and
// pictures, whether a numeric (gnre) genre was resolved, and whether the
// canonical rebuild owns the item. owned == false means the item is preserved
// verbatim (an unknown atom, a foreign-mean freeform, or a parse failure).
type itemResult struct {
	contribs     []core.Contribution
	pics         []core.Picture
	numericGenre bool
	owned        bool
}

// parseDataAtoms decodes the "data" sub-atoms of an ilst item payload. It
// returns ok == false on any malformation so the caller preserves the item
// verbatim. Values alias the payload (read-only by contract), avoiding a copy of
// embedded cover art.
func parseDataAtoms(p []byte) ([]dataAtom, bool) {
	var out []dataAtom
	// All arithmetic is in int64: a crafted 32-bit size near 2^31 plus a non-zero
	// pos would overflow a 32-bit int (so the bounds check could pass and the slice
	// panic). pos stays well within the payload, so the slice indices fit in int.
	n := int64(len(p))
	pos := int64(0)
	for pos+8 <= n {
		size := int64(binary.BigEndian.Uint32(p[pos : pos+4]))
		if size < 16 || pos+size > n || string(p[pos+4:pos+8]) != "data" {
			return nil, false
		}
		typ := uint32(p[pos+9])<<16 | uint32(p[pos+10])<<8 | uint32(p[pos+11])
		out = append(out, dataAtom{typ: typ, value: p[pos+16 : pos+size]})
		pos += size
	}
	if pos != n || len(out) == 0 {
		return nil, false
	}
	return out, true
}

// decodeItem decodes one ilst item into canonical contributions and pictures.
func decodeItem(it item) itemResult {
	switch it.id() {
	case "----":
		return decodeFreeform(it)
	case "trkn":
		return decodePair(it, tag.TrackNumber, tag.TrackTotal)
	case "disk":
		return decodePair(it, tag.DiscNumber, tag.DiscTotal)
	case "covr":
		return decodeCover(it)
	case "gnre":
		return decodeGnre(it)
	case "cpil":
		return decodeBool(it, tag.Compilation)
	default:
		key, ok := mapping.MP4TextKey(it.id())
		if !ok {
			return itemResult{owned: false} // unknown atom: preserve verbatim
		}
		return decodeText(it, key)
	}
}

// decodeText decodes a plain UTF-8 text item (possibly multi-value). An
// unexpected data type or invalid UTF-8 makes the item not-owned (preserved).
func decodeText(it item, key tag.Key) itemResult {
	atoms, ok := parseDataAtoms(it.payload)
	if !ok {
		return itemResult{owned: false}
	}
	var contribs []core.Contribution
	for _, a := range atoms {
		if a.typ != typeUTF8 && a.typ != typeImplicit {
			return itemResult{owned: false}
		}
		if !utf8.Valid(a.value) {
			return itemResult{owned: false}
		}
		contribs = append(contribs, core.Contribution{Key: key, Value: string(a.value), Source: it.id()})
	}
	return itemResult{contribs: contribs, owned: true}
}

// decodePair decodes a trkn/disk numeric pair: reserved(2), number(2), total(2),
// [reserved(2)]. A zero number or total is omitted.
func decodePair(it item, numKey, totKey tag.Key) itemResult {
	atoms, ok := parseDataAtoms(it.payload)
	if !ok {
		return itemResult{owned: false}
	}
	var contribs []core.Contribution
	for _, a := range atoms {
		if len(a.value) < 6 {
			return itemResult{owned: false}
		}
		num := binary.BigEndian.Uint16(a.value[2:4])
		total := binary.BigEndian.Uint16(a.value[4:6])
		if num > 0 {
			contribs = append(contribs, core.Contribution{Key: numKey, Value: strconv.Itoa(int(num)), Source: it.id()})
		}
		if total > 0 {
			contribs = append(contribs, core.Contribution{Key: totKey, Value: strconv.Itoa(int(total)), Source: it.id()})
		}
	}
	return itemResult{contribs: contribs, owned: true}
}

// decodeCover decodes covr image data atoms into pictures.
func decodeCover(it item) itemResult {
	atoms, ok := parseDataAtoms(it.payload)
	if !ok {
		return itemResult{owned: false}
	}
	var pics []core.Picture
	for _, a := range atoms {
		p := core.Picture{Type: core.PicFrontCover, MIME: coverMIME(a.typ), Data: a.value}
		p.SniffInto()
		pics = append(pics, p)
	}
	return itemResult{pics: pics, owned: true}
}

// coverMIME maps a covr data-atom type code to an image MIME (JPEG by default, as
// iTunes uses for an implicit or unknown type), and coverType the reverse — the
// single place the cover image-format mapping lives.
func coverMIME(typ uint32) string {
	switch typ {
	case typePNG:
		return "image/png"
	case typeBMP:
		return "image/bmp"
	default:
		return "image/jpeg"
	}
}

func coverType(mime string) uint32 {
	switch mime {
	case "image/png":
		return typePNG
	case "image/bmp":
		return typeBMP
	default:
		return typeJPEG
	}
}

// decodeGnre decodes the legacy numeric genre atom (a 1-based ID3v1 genre index)
// into a genre name, mirroring how iTunes/mutagen fold "gnre" into the text
// genre. It is always rewritten as a text "©gen" atom.
func decodeGnre(it item) itemResult {
	atoms, ok := parseDataAtoms(it.payload)
	if !ok {
		return itemResult{owned: false}
	}
	var contribs []core.Contribution
	for _, a := range atoms {
		if len(a.value) != 2 {
			return itemResult{owned: false}
		}
		n := int(binary.BigEndian.Uint16(a.value))
		name, ok := id3.GenreName(n - 1)
		if !ok {
			return itemResult{owned: false}
		}
		contribs = append(contribs, core.Contribution{Key: tag.Genre, Value: name, Source: "gnre"})
	}
	return itemResult{contribs: contribs, numericGenre: true, owned: true}
}

// decodeBool decodes a single-byte boolean atom (cpil) into "1"/"0".
func decodeBool(it item, key tag.Key) itemResult {
	atoms, ok := parseDataAtoms(it.payload)
	if !ok || len(atoms) != 1 || len(atoms[0].value) != 1 {
		return itemResult{owned: false}
	}
	val := "0"
	if atoms[0].value[0] != 0 {
		val = "1"
	}
	return itemResult{contribs: []core.Contribution{{Key: key, Value: val, Source: key.String()}}, owned: true}
}

// decodeFreeform decodes a "----" freeform item. It is owned only when its mean
// is com.apple.iTunes and its name maps to a canonical key (a known Picard name,
// or a name that is already a valid canonical key — which is how this codec
// writes custom keys). Foreign means and mixed-case iTunes-internal names
// (iTunNORM, …) are preserved verbatim.
func decodeFreeform(it item) itemResult {
	mean, name, dataStart, ok := parseMeanName(it.payload)
	if !ok || mean != itunesMean {
		return itemResult{owned: false}
	}
	key, known := mapping.MP4FreeformKey(name)
	if !known {
		k := tag.Key(name)
		if !k.Valid() { // mixed-case / spaced foreign name: preserve verbatim
			return itemResult{owned: false}
		}
		key = k
	}
	atoms, ok := parseDataAtoms(it.payload[dataStart:])
	if !ok {
		return itemResult{owned: false}
	}
	var contribs []core.Contribution
	src := "----:" + name
	for _, a := range atoms {
		if a.typ != typeUTF8 && a.typ != typeImplicit {
			return itemResult{owned: false} // binary freeform: preserve verbatim
		}
		if !utf8.Valid(a.value) {
			return itemResult{owned: false}
		}
		contribs = append(contribs, core.Contribution{Key: key, Value: string(a.value), Source: src})
	}
	return itemResult{contribs: contribs, owned: true}
}

// parseMeanName decodes the leading "mean" and "name" atoms of a freeform item,
// returning the mean string, the name string, and the offset where the "data"
// atoms begin. Each is a FullBox: [size]["mean"/"name"][4 version/flags][bytes].
func parseMeanName(p []byte) (mean, name string, dataStart int64, ok bool) {
	meanStr, next, ok := parseLabelAtom(p, 0, "mean")
	if !ok {
		return "", "", 0, false
	}
	nameStr, next2, ok := parseLabelAtom(p, next, "name")
	if !ok {
		return "", "", 0, false
	}
	return meanStr, nameStr, next2, true
}

// parseLabelAtom decodes a "mean" or "name" FullBox at p[pos:], returning its
// text and the offset just past it. Offsets are int64 so a crafted size cannot
// overflow a 32-bit int and slip past the bounds check (see parseDataAtoms).
func parseLabelAtom(p []byte, pos int64, want string) (string, int64, bool) {
	n := int64(len(p))
	if pos+12 > n {
		return "", 0, false
	}
	size := int64(binary.BigEndian.Uint32(p[pos : pos+4]))
	if size < 12 || pos+size > n || string(p[pos+4:pos+8]) != want {
		return "", 0, false
	}
	return string(p[pos+12 : pos+size]), pos + size, true
}

// project derives the canonical view from a parsed (or rewritten) document. It is
// a pure read — it does not mutate the items — so it is shared by Parse and the
// post-write result without coupling the writer to call order.
func project(d *doc) (tags tag.TagSet, pics []core.Picture, families []core.FamilyValue, numericGenre bool) {
	var contribs []core.Contribution
	for _, it := range d.items {
		r := decodeItem(it)
		if !r.owned {
			continue
		}
		contribs = append(contribs, r.contribs...)
		pics = append(pics, r.pics...)
		numericGenre = numericGenre || r.numericGenre
	}
	return core.BuildTagSet(contribs), pics, core.BuildFamilies(contribs, core.FamilyMP4), numericGenre
}

// owned reports whether the canonical rebuild owns an item — i.e. re-renders it
// from the edited tag set. Items it does not own (unknown atoms, foreign-mean
// freeforms, parse failures) are preserved verbatim. It is recomputed wherever
// needed rather than cached on the item, keeping projection a pure read.
func owned(it item) bool { return decodeItem(it).owned }
