package mp4

import (
	"encoding/binary"
	"slices"
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
	// typeUnsignedInt is what some writers give the integer atoms; an atom's value is never
	// negative, so both read alike.
	typeUnsignedInt = 22
)

// integerType reports whether a data atom's type holds an integer: one of the two integer
// types, or the implicit type iTunes gives its fixed-layout atoms.
func integerType(typ uint32) bool {
	return typ == typeSignedInt || typ == typeUnsignedInt || typ == typeImplicit
}

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
	if it.mdta() {
		return decodeMdtaItem(it)
	}
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
	case "stik":
		return decodeInt(it, tag.MediaType)
	case "rtng":
		return decodeInt(it, tag.ITunesAdvisory)
	case "tmpo":
		return decodeInt(it, tag.BPM)
	case "\xa9mvi":
		return decodeInt(it, tag.Movement)
	case "\xa9mvc":
		return decodeInt(it, tag.MovementTotal)
	case "pgap":
		return decodeBool(it, tag.ITunesGapless)
	case "shwm":
		return decodeBool(it, tag.ShowMovement)
	case "tves":
		return decodeInt(it, tag.EpisodeNumber)
	case "tvsn":
		return decodeInt(it, tag.SeasonNumber)
	default:
		key, ok := mapping.MP4TextKey(it.id())
		if !ok {
			return itemResult{owned: false} // unknown atom: preserve verbatim
		}
		return decodeText(it, key)
	}
}

// decodeMdtaItem decodes an item keyed by the resolved keys-index name rather than a
// four-character atom. An unrepresentable key leaves the item unowned, so it is preserved
// verbatim. Cover art is the one non-text value here.
func decodeMdtaItem(it item) itemResult {
	if isMdtaCoverKey(it.key) {
		return decodeCover(it)
	}
	key, ok := mapping.MP4MdtaKey(it.key)
	if !ok {
		return itemResult{owned: false}
	}
	atoms, ok := parseDataAtoms(it.payload)
	if !ok {
		return itemResult{owned: false}
	}
	var contribs []core.Contribution
	src := "mdta:" + it.key
	for _, a := range atoms {
		if a.typ != typeUTF8 && a.typ != typeImplicit {
			return itemResult{owned: false} // binary payload: preserve verbatim
		}
		if !utf8.Valid(a.value) {
			return itemResult{owned: false}
		}
		// A track or disc entry holds "n/total" as one text, as ffmpeg writes it: two values
		// of two keys, like the trkn atom's.
		if num, total, split := tag.NumberTotalSplit(key, string(a.value)); split {
			if num != "" {
				contribs = append(contribs, core.Contribution{Key: key, Value: num, Source: src})
			}
			if total != "" {
				contribs = append(contribs, core.Contribution{Key: tag.TotalKey(key), Value: total, Source: src})
			}
			continue
		}
		contribs = append(contribs, core.Contribution{Key: key, Value: string(a.value), Source: src})
	}
	return itemResult{contribs: contribs, owned: true}
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

// decodePair decodes a trkn/disk numeric pair: reserved(2), number(2), [total(2),
// [reserved(2)]]. A zero number or total is omitted.
func decodePair(it item, numKey, totKey tag.Key) itemResult {
	atoms, ok := parseDataAtoms(it.payload)
	if !ok {
		return itemResult{owned: false}
	}
	var contribs []core.Contribution
	for _, a := range atoms {
		if len(a.value) < 4 {
			return itemResult{owned: false}
		}
		num := binary.BigEndian.Uint16(a.value[2:4])
		var total uint16
		if len(a.value) >= 6 {
			total = binary.BigEndian.Uint16(a.value[4:6])
		}
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
		p.SniffAuthoritative()
		pics = append(pics, p)
	}
	return itemResult{pics: pics, owned: true}
}

// coverMIME maps a covr data-atom type code to an image MIME, and coverType the reverse.
func coverMIME(typ uint32) string {
	switch typ {
	case typeJPEG:
		return "image/jpeg"
	case typePNG:
		return "image/png"
	case typeBMP:
		return "image/bmp"
	default:
		return ""
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

// coverMIMEs lists the image formats an MP4 covr atom can label: only JPEG, PNG, and BMP
// have covr type codes. The write-time check and the transfer capability both read it.
var coverMIMEs = []string{"image/jpeg", "image/png", "image/bmp"}

// coverMIMESupported reports whether an MP4 covr atom can label this image format.
func coverMIMESupported(mime string) bool {
	return slices.Contains(coverMIMEs, mime)
}

// decodeGnre decodes the legacy numeric genre atom (a 1-based ID3v1 genre index)
// into a genre name, mirroring how iTunes/mutagen fold "gnre" into the text
// genre. A write keeps gnre only when the value is unchanged or NumericGenre is
// set; otherwise it is rewritten as a text "\xa9gen" atom.
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

// decodeInt decodes an iTunes integer atom (stik, rtng, tmpo, ©mvi, ©mvc) into its
// canonical key as the decimal string, not a name, so it round-trips exactly.
func decodeInt(it item, key tag.Key) itemResult {
	atoms, ok := parseDataAtoms(it.payload)
	if !ok || len(atoms) != 1 {
		return itemResult{owned: false}
	}
	if !integerType(atoms[0].typ) {
		return itemResult{owned: false}
	}
	n, ok := intFromBytes(atoms[0].value)
	if !ok {
		return itemResult{owned: false}
	}
	return itemResult{contribs: []core.Contribution{{Key: key, Value: strconv.FormatUint(n, 10), Source: it.id()}}, owned: true}
}

// intFromBytes reads a big-endian unsigned integer from 1-4 bytes (the width an
// iTunes integer atom uses) into a uint64, reporting false for an empty or
// oversized value. uint64 avoids the wraparound a 32-bit int would suffer on a
// 4-byte value with the high bit set.
func intFromBytes(b []byte) (uint64, bool) {
	if len(b) == 0 || len(b) > 4 {
		return 0, false
	}
	var n uint64
	for _, x := range b {
		n = n<<8 | uint64(x)
	}
	return n, true
}

// decodeBool decodes a single-byte boolean atom (cpil, pgap, shwm) into "1"/"0". Like
// decodeInt, only an integer-bearing data type is owned: a text-typed atom would read
// ASCII "0" (0x30, non-zero) as true, so it stays preserved verbatim instead.
func decodeBool(it item, key tag.Key) itemResult {
	atoms, ok := parseDataAtoms(it.payload)
	if !ok || len(atoms) != 1 || len(atoms[0].value) != 1 {
		return itemResult{owned: false}
	}
	if !integerType(atoms[0].typ) {
		return itemResult{owned: false}
	}
	val := "0"
	if atoms[0].value[0] != 0 {
		val = "1"
	}
	return itemResult{contribs: []core.Contribution{{Key: key, Value: val, Source: key.String()}}, owned: true}
}

// decodeFreeform decodes a "----" freeform item. Foreign means and mixed-case
// iTunes-internal names (iTunNORM, ...) are preserved verbatim.
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

// itemKeys returns the keys an item holds values of: its contributions', and the total's when a
// number holds a slash pair.
func itemKeys(r itemResult) []tag.Key {
	var keys []tag.Key
	add := func(k tag.Key) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, c := range r.contribs {
		add(c.Key)
		if _, _, split := tag.NumberTotalSplit(c.Key, c.Value); split {
			add(tag.TotalKey(c.Key))
		}
	}
	return keys
}

// reading is how one ilst's items are read: what each decodes to, its identity and keys, and for
// each key the identity of the item whose values are read for it.
type reading struct {
	items   []item
	results []itemResult
	ids     []itemKey
	keys    [][]tag.Key
	primary map[tag.Key]itemKey
	// values holds what the items read for a date key say, which a bare year elsewhere may echo.
	values map[tag.Key][]string
}

// readItems settles which item is read for each key: the first to hold it, and others of its
// identity with it. An item of another identity is not read for that key; it shows in the families
// and stays in the file until the key is edited. A date is the one exception to "first": an item
// holding only its year gives way to a later one holding the date (Picard writes an originalyear
// atom ahead of originaldate), and a year beside its date is an echo, not a disagreement.
func readItems(items []item) reading {
	n := len(items)
	rd := reading{
		items: items, results: make([]itemResult, n), ids: make([]itemKey, n), keys: make([][]tag.Key, n),
		primary: map[tag.Key]itemKey{}, values: map[tag.Key][]string{},
	}
	for i, it := range items {
		rd.results[i] = decodeItem(it)
		rd.ids[i], rd.keys[i] = itemIdentity(it), itemKeys(rd.results[i])
		for _, k := range rd.keys[i] {
			if !tag.IsDateKey(k) {
				if _, held := rd.primary[k]; !held {
					rd.primary[k] = rd.ids[i]
				}
				continue
			}
			vals := contribValues(rd.results[i], k)
			first, held := rd.primary[k]
			switch {
			case !held, first != rd.ids[i] && echoesOf(rd.values[k], vals):
				rd.primary[k], rd.values[k] = rd.ids[i], vals
			case first == rd.ids[i]:
				rd.values[k] = append(rd.values[k], vals...)
			}
		}
	}
	return rd
}

// read reports whether item i is the one read for key.
func (rd reading) read(i int, key tag.Key) bool { return rd.primary[key] == rd.ids[i] }

// echo reports whether c, of an item not read for its key, only repeats the year of the date read.
func (rd reading) echo(c core.Contribution) bool {
	return tag.IsDateKey(c.Key) && echoesOf([]string{c.Value}, rd.values[c.Key])
}

// shadowed reports whether item i holds values and is read for none of them.
func (rd reading) shadowed(i int) bool {
	return len(rd.keys[i]) > 0 && !slices.ContainsFunc(rd.keys[i], func(k tag.Key) bool { return rd.read(i, k) })
}

// contribValues returns the values r holds of key.
func contribValues(r itemResult, key tag.Key) []string {
	var vals []string
	for _, c := range r.contribs {
		if c.Key == key {
			vals = append(vals, c.Value)
		}
	}
	return vals
}

// echoesOf reports whether every one of years is a bare year that opens one of dates.
func echoesOf(years, dates []string) bool {
	for _, y := range years {
		if !slices.ContainsFunc(dates, func(d string) bool {
			return len(d) > 4 && d[:4] == y && d[4] == '-'
		}) {
			return false
		}
	}
	return true
}

// storeContribs sorts what one ilst's items hold: the contributions read, every contribution for
// the families (but a year that echoes the date read), the pictures, and whether the genre read
// is numeric.
func storeContribs(rd reading) (contribs, all []core.Contribution, pics []core.Picture, numericGenre bool) {
	for i, r := range rd.results {
		if !r.owned {
			continue
		}
		for _, c := range r.contribs {
			switch {
			case rd.read(i, c.Key):
				contribs, all = append(contribs, c), append(all, c)
			case !rd.echo(c):
				all = append(all, c)
			}
		}
		pics = append(pics, r.pics...)
		numericGenre = numericGenre || r.numericGenre && rd.read(i, tag.Genre)
	}
	return contribs, all, pics, numericGenre
}

// storesView is what a document's stores give the canonical view.
type storesView struct {
	contribs []core.Contribution // the values read
	all      []core.Contribution // those, and what the first store holds unread
	// secondary holds what the other stores hold of keys already read, and what the second
	// store holds unread: each shows as a family entry of its own.
	secondary    []core.Contribution
	pics         []core.Picture
	numericGenre bool
	// secondOnly names the keys read from the second store alone. A write leaves them there.
	secondOnly map[tag.Key]bool
}

// viewStores reads the stores in order: the ilst written, the udta text atoms, the second store.
// A value of a later one is promoted only for a key the ones before it do not hold: merging the
// two would fold a disagreement into a multi-value the next write stores as one.
func viewStores(d *doc) storesView {
	var v storesView
	v.contribs, v.all, v.pics, v.numericGenre = storeContribs(d.reading())
	promote := func(from []core.Contribution) (promoted map[tag.Key]bool) {
		held := core.BuildTagSet(v.contribs)
		tag.NormalizeNumberPairs(&held) // a number's "n/total" holds the total too
		promoted = map[tag.Key]bool{}
		for _, c := range from {
			if held.Has(c.Key) {
				v.secondary = append(v.secondary, c)
				continue
			}
			v.contribs, v.all = append(v.contribs, c), append(v.all, c)
			promoted[c.Key] = true
			if _, _, split := tag.NumberTotalSplit(c.Key, c.Value); split {
				promoted[tag.TotalKey(c.Key)] = true
			}
		}
		return promoted
	}
	promote(udtaContributions(d.udtaTexts))
	if d.second != nil {
		read, all, pics, _ := storeContribs(readItems(d.second.items))
		v.secondOnly = promote(read)
		v.secondary = append(v.secondary, unread(all, read)...)
		if len(v.pics) == 0 {
			v.pics = pics
		}
	}
	return v
}

// unread returns the contributions of all that read, a subsequence of it, lacks.
func unread(all, read []core.Contribution) []core.Contribution {
	var out []core.Contribution
	for _, c := range all {
		if len(read) > 0 && read[0] == c {
			read = read[1:]
			continue
		}
		out = append(out, c)
	}
	return out
}

// project derives the canonical view from a parsed (or rewritten) document. It is a
// pure read, so Parse and the post-write result share it regardless of call order.
func project(d *doc) (tags tag.TagSet, pics []core.Picture, families []core.FamilyValue, numericGenre bool) {
	v := viewStores(d)
	tags = core.BuildTagSet(v.contribs)
	// A text item of a number key, a freeform TRACKNUMBER say, may hold "n/total".
	tag.NormalizeNumberPairs(&tags)
	families = core.BuildFamilies(v.all, core.FamilyMP4)
	for _, c := range v.secondary {
		families = append(families, core.FamilyValue{
			Key: c.Key, Family: core.FamilyMP4, Scope: core.ScopeTrack,
			Values: []string{c.Value}, Selected: core.FamilySelected(tags, c.Key, c.Value),
		})
	}
	return tags, v.pics, families, v.numericGenre
}

// invalidKeyWarnings reports the iTunes freeform names the canonical vocabulary cannot
// represent, and the keys items whose value, no text, hides a canonical key's. Such an item is
// preserved verbatim and contributes nothing, so without a warning the value would be absent
// from every canonical view unremarked.
func invalidKeyWarnings(d *doc) []core.Warning {
	var ws []core.Warning
	seen := map[string]bool{}
	for _, it := range slices.Concat(d.items, d.secondItems()) {
		// A keys item that holds no text shows under no key. Where that hides a value a key
		// would show, the file says so; the other binary items a recorder writes are kept
		// without a word.
		if it.mdta() && !seen[it.key] && !owned(it) && hidesKey(it.key) {
			seen[it.key] = true
			ws = core.WarnInvalidKey(ws, it.key)
		}
		if it.mdta() || it.id() != "----" {
			continue
		}
		mean, name, _, ok := parseMeanName(it.payload)
		if !ok || mean != itunesMean || seen[name] {
			continue
		}
		if _, known := mapping.MP4FreeformKey(name); known || tag.Key(name).Valid() {
			continue
		}
		seen[name] = true
		ws = core.WarnInvalidKey(ws, name)
	}
	return ws
}

// appleUserRating is the keys name of QuickTime's star rating, a float no text key can show.
const appleUserRating = "com.apple.quicktime.rating.user"

// hidesKey reports whether an unreadable keys item of this name hides a value a canonical key
// would show.
func hidesKey(name string) bool {
	k, ok := mapping.MP4MdtaKey(name)
	return name == appleUserRating || ok && k.Known()
}

// owned reports whether the canonical rebuild owns an item. Items it does not own
// (unknown atoms, foreign-mean freeforms, parse failures) are preserved verbatim. It is
// recomputed wherever needed, not cached on the item, so projection stays a pure read.
func owned(it item) bool { return decodeItem(it).owned }
