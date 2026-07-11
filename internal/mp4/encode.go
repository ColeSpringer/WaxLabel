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

// buildItems renders the edited canonical tags into ilst items, appends the
// pre-resolved cover item(s) (see coverItemsToWrite - either the re-encoded edited
// pictures or the parsed covr carried verbatim), then the preserved items (unknown
// atoms and foreign freeforms kept verbatim) in their original order. The canonical
// items come first in a stable order derived from the tag set's key order, so the same
// input and edit produce the same bytes. Number/total pairs (trkn, disk) are
// special-cased; everything else is a text atom (known four-cc) or a freeform.
func buildItems(edited tag.TagSet, covr []item, preserved []item, numericGenre bool) []item {
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

	out = append(out, covr...)
	return dedupePreservedItems(out, preserved)
}

// dedupePreservedItems appends the preserved items (unknown atoms and foreign freeforms kept
// verbatim) after the rebuilt canonical items, dropping any preserved item whose atom identity a
// rebuilt item already emits. A known atom with malformed content is owned:false, so it is kept
// verbatim by preservedItems while buildItems re-emits the fresh canonical atom of the same type;
// without this de-duplication the output would carry two atoms of one identity (e.g. a corrupt
// \xa9nam beside the re-rendered one). It reasons about the whole rebuilt output, so a file with a
// valid and a malformed covr, edited with pictures unchanged, carries the valid covr via covrItems
// and the malformed duplicate is dropped - a conformant file has one covr, no canonical data lost.
func dedupePreservedItems(built, preserved []item) []item {
	emitted := map[itemKey]bool{}
	for _, it := range built {
		emitted[itemIdentity(it)] = true
	}
	out := built
	for _, it := range preserved {
		if !emitted[itemIdentity(it)] {
			out = append(out, it)
		}
	}
	return out
}

// itemKey is an ilst item's de-duplication identity: the four-cc name, plus the mean and name of a
// freeform "----" atom. Keeping mean/name as their own comparable fields avoids both a separator
// hack and cloning the (potentially large) payload into a string just to key a map.
type itemKey struct {
	name    [4]byte
	mean    string
	subName string
}

// itemIdentity keys an ilst item by the atom slot a rebuilt canonical item would occupy. For most
// atoms that is the four-cc alone, but a freeform "----" atom must be keyed by its mean+name too:
// several legitimate foreign freeforms share the "----" four-cc and differ only there, so keying on
// the four-cc alone would wrongly drop them. A malformed freeform leaves mean/name empty, which
// cannot match a rebuilt (always parseable, always com.apple.iTunes-mean) item, so it is preserved.
func itemIdentity(it item) itemKey {
	if it.id() == "----" {
		mean, name, _, _ := parseMeanName(it.payload)
		return itemKey{name: it.name, mean: mean, subName: name}
	}
	return itemKey{name: it.name}
}

// covrItems returns the owned (successfully decoded) covr item(s) among parsed ilst items, so an
// edit that leaves the picture set unchanged can carry the original cover verbatim. A malformed
// covr whose data atoms fail to parse is not owned, so preservedItems already carries it;
// returning it here too would append it twice, once as a cover and once as a preserved item, and
// duplicate it further on every later edit. A conformant file has at most one covr; owned ones
// are returned in document order.
func covrItems(items []item) []item {
	var out []item
	for _, it := range items {
		if it.name == atomName("covr") && owned(it) {
			out = append(out, it)
		}
	}
	return out
}

// coverItemsToWrite resolves the covr ilst item(s) to emit past the fast path. When the picture
// set changed it re-encodes the edited pictures via coverItem, whose format checkCoverFormats has
// already validated in Plan (that guard runs only under picturesChanged). When the picture set
// did not change it carries the parsed covr item(s) verbatim. That keeps a cover the covr atom
// cannot re-label faithfully (a GIF or WebP the read path now sniffs to its true MIME)
// byte-for-byte under its original type code, instead of rewriting it under coverType's JPEG
// default and stamping a JPEG type code over non-JPEG bytes on an unrelated tag- or chapter-only
// edit.
func coverItemsToWrite(pics []core.Picture, parsed []item, picturesChanged bool) []item {
	if !picturesChanged {
		return covrItems(parsed)
	}
	if len(pics) > 0 {
		return []item{coverItem(pics)}
	}
	return nil
}

// multiValueDataKeys returns the canonical keys this edit writes as more than one MP4 data atom.
// The iTunes ilst stores a multi-valued field as several data atoms under one item, which
// round-trips through WaxLabel but which many third-party readers show only the first atom of, so
// the writer notes the interop risk. Every field that reaches buildItems' default branch emits one
// data atom per value - a known text atom (textItem), a numeric-genre atom (gnreItem), and a custom
// freeform ---- atom (freeformItem) all do - so the only single-atom fields are the structured slots
// (trkn/disk pack the pair into one atom; cpil/stik are one atom). Excluding exactly those slots and
// taking any remaining key with more than one value names precisely the multi-atom keys, without
// re-deriving which sub-encoder buildItems picks. It must stay in step with buildItems' switch.
func multiValueDataKeys(edited tag.TagSet) []tag.Key {
	var keys []tag.Key
	for _, key := range edited.Keys() {
		switch key {
		case tag.TrackNumber, tag.TrackTotal, tag.DiscNumber, tag.DiscTotal, tag.Compilation, tag.MediaType:
			continue // structured single-atom slots, handled by pairItem/boolItem/mediaTypeItem
		}
		if vals, _ := edited.Get(key); len(vals) > 1 {
			keys = append(keys, key)
		}
	}
	return keys
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

// mediaTypeItem builds the iTunes "stik" media-kind atom from the canonical MediaType value (a
// decimal integer). The iTunes stik vocabulary is a single byte (the defined media kinds are 0-14),
// so a value past 255 is rejected (item{}, false) rather than widening the atom to 2 or 4 bytes;
// a non-numeric value is likewise skipped. This must agree with ValidMediaTypeValue, which reports
// the same value as dropped so the writer's value-dropped warning fires for it.
func mediaTypeItem(vals []string) (item, bool) {
	if len(vals) == 0 {
		return item{}, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(vals[0]), 10, 32)
	if err != nil || n > 0xFF {
		return item{}, false
	}
	return item{name: atomName("stik"), payload: renderData(typeSignedInt, []byte{byte(n)})}, true
}

// boolItem builds a single-byte boolean atom (cpil) from a canonical boolean
// value (parsed the same way as the typed projection, so "TRUE"/" yes " agree).
func boolItem(name string, vals []string) (item, bool) {
	// Drop a present-empty value rather than fabricating a definite 0. An empty COMPILATION=
	// carries no boolean intent, so writing a concrete false (which reads back as a real,
	// strict-clean 0) would invent data the edit never supplied. This matches the MP4
	// empty-number drop; unlike a text format such as FLAC, which stores the empty value
	// verbatim, MP4 has no empty-boolean representation, so the atom is omitted. The drop is
	// unwarned, mirroring the empty-number drop.
	if len(vals) == 0 || strings.TrimSpace(vals[0]) == "" {
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

// numTotal resolves the number/total a trkn/disk atom encodes. It consumes the same slots
// resolvePairSlots resolves - a genuine "n/total" pair is split on "/", a malformed or
// non-numeric value (e.g. "1/2/3", "3/abc") is kept whole on the number slot, and an explicit
// total key wins - then parses each resolved slot as a plain integer (any parse failure, including
// a whole non-numeric value, reads as 0) and clamps to the 16-bit range the atom stores. Sharing
// one resolution with the drop report (appendDroppedPair) keeps storage and the warning in
// lockstep, so a value the report names dropped is not silently stored as a lenient partial.
func numTotal(ts tag.TagSet, numKey, totKey tag.Key) (num, total uint16) {
	numPart, totPart := resolvePairSlots(ts, numKey, totKey)
	return clampUint16(slotInt(numPart)), clampUint16(slotInt(totPart))
}

// slotInt parses a resolved trkn/disk slot as a plain integer (trimmed), treating any parse
// failure as 0. A whole non-numeric value resolvePairSlots kept intact ("1/2/3", "3/abc") fails
// to parse and so reads as 0, which pairItem renders as an absent slot rather than a lenient
// partial extracted from the first '/'.
func slotInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// clampUint16 returns n as a uint16, or 0 if it is out of range.
func clampUint16(n int) uint16 {
	if n < 0 || n > 0xFFFF {
		return 0
	}
	return uint16(n)
}

// droppedValue records a canonical (key, value) the iTunes atom encoders cannot
// represent and would otherwise silently drop. ZeroUnset marks the one drop that is not a
// hard rejection: a literal 0 in a trkn/disk slot, whose bytes ARE written (0/N) but which
// decodePair reads back as absent (its num>0/total>0 guards treat 0 as unset), so it is a
// round-trip loss rather than an unrepresentable value. The write.go warning wording keys off
// it; a uint16-overflow or non-numeric drop leaves it false.
type droppedValue struct {
	Key       tag.Key
	Value     string
	ZeroUnset bool
}

// droppedValues returns the canonical values this edit would genuinely lose at the iTunes
// encode site (the value is not written at all): a trkn/disk slot outside the uint16 the atom
// holds (numTotal -> clampUint16), or a stik media kind strconv.ParseUint cannot read
// (mediaTypeItem, which returns no atom for a non-numeric value). It reads the same raw
// canonical strings those encoders consume - so it names exactly what buildItems drops and
// cannot desync from it - and treats a literal 0 (the pair encoder's "absent") and an
// absent/empty slot as no loss. COMPILATION is not here: boolItem coerces a non-boolean to 0
// and writes it, so it is a coercion (see coercedValues), not a drop. The encoders stay the
// authority on the written value; this is only which were lost.
func droppedValues(ts tag.TagSet) []droppedValue {
	var out []droppedValue
	out = appendDroppedPair(out, ts, tag.TrackNumber, tag.TrackTotal)
	out = appendDroppedPair(out, ts, tag.DiscNumber, tag.DiscTotal)
	// MEDIATYPE (stik) uses the same value-drop predicate exposed to transfer. An empty value
	// is exempt because it intentionally stores nothing; a non-numeric one is a genuine drop
	// because mediaTypeItem returns no atom for it.
	if v, ok := ts.First(tag.MediaType); ok && mediaTypeValueDropped(v) {
		out = append(out, droppedValue{Key: tag.MediaType, Value: strings.TrimSpace(v)})
	}
	return out
}

// coercedValues returns the canonical values this edit stores in a normalized form because the
// iTunes atom cannot hold the literal. Unlike droppedValues, these ARE written (the change set
// shows the stored value); the warning only tells the user the literal was normalized. It reuses
// the droppedValue (key, value) carrier. Only COMPILATION applies: cpil is a single boolean byte,
// so boolItem coerces a non-boolean like "maybe" to 0 (false) and writes it, rather than dropping
// it. A trkn/disk number stored non-canonically ("03", "+3") is NOT a coercion worth warning: the
// uint16 atom holds the same integer, so the leading zero or sign is a numerically-lossless
// canonicalization that a copy grades Carried and diff treats as no change, not a reported loss.
// An empty value is exempt because it stores nothing.
func coercedValues(ts tag.TagSet) []droppedValue {
	var out []droppedValue
	if v, ok := ts.First(tag.Compilation); ok && compilationValueDropped(v) {
		out = append(out, droppedValue{Key: tag.Compilation, Value: strings.TrimSpace(v)})
	}
	return out
}

// resolvePairSlots resolves the two slots a trkn/disk number/total pair edits, through the guarded
// tag.NumberTotalSplit so it matches the canonical model and restoreUnstorablePairSlots rather than
// leniently cutting on the first '/'. A genuine numeric pair ("3/12", "3/09") splits into its number
// and total; a value NumberTotalSplit declines to split (no '/', or a malformed/non-numeric pair like
// "1/2/3" or "3/abc") is kept whole (trimmed) on the number slot with no total, so it is judged - and
// dropped - against the number slot instead of fabricating a phantom total from the tail. A present,
// raw-non-empty explicit total key then overrides the tail; like ParseNumPair the override gates on
// the raw string, so a whitespace-only total overrides too and leaves no stale tail. The encoder
// (numTotal) and the drop report (appendDroppedPair) share this one resolution, so storage and the
// warning cannot drift on the split rule.
func resolvePairSlots(ts tag.TagSet, numKey, totKey tag.Key) (numPart, totPart string) {
	numStr, _ := ts.First(numKey)
	totStr, _ := ts.First(totKey)
	if num, tot, split := tag.NumberTotalSplit(numKey, numStr); split {
		numPart, totPart = num, tot
	} else {
		numPart, totPart = strings.TrimSpace(numStr), ""
	}
	if totStr != "" {
		totPart = strings.TrimSpace(totStr)
	}
	return numPart, totPart
}

// appendDroppedPair adds the dropped slot(s) of one trkn/disk pair. It resolves the two slots via
// resolvePairSlots (shared with appendCoercedPair) so drop and coercion name the same slots the
// encoder reads. TRACKNUMBER and TRACKTOTAL share one trkn atom, so each slot is judged against its
// own source string (TRACKTOTAL=abc names TRACKTOTAL, not the merged pair).
func appendDroppedPair(out []droppedValue, ts tag.TagSet, numKey, totKey tag.Key) []droppedValue {
	numPart, totPart := resolvePairSlots(ts, numKey, totKey)
	// decodePair drops a literal 0 in EITHER slot on read (its num>0/total>0 guards treat 0 as
	// unset), so a 0 written to a slot never round-trips - report it per slot, not only when the
	// whole pair collapses to absent. TRACKNUMBER=0 with TRACKTOTAL=12 still loses the 0 on read.
	out = appendSlotDrop(out, numKey, numPart)
	out = appendSlotDrop(out, totKey, totPart)
	return out
}

// appendSlotDrop records one trkn/disk slot the encoder would lose: either a value the uint16
// atom cannot represent, or a literal 0 (which decodePair drops on read, so it never
// round-trips - MP4 keeps its 0-as-unset write semantics, this only makes the loss visible). A
// slot is reported at most once. The 0 case sets ZeroUnset so the warning can say "reads back as
// absent" rather than the hard-rejection "cannot be represented".
func appendSlotDrop(out []droppedValue, key tag.Key, slot string) []droppedValue {
	switch {
	case uint16ValueDropped(slot):
		return append(out, droppedValue{Key: key, Value: slot})
	case isRepresentableZero(slot):
		return append(out, droppedValue{Key: key, Value: strings.TrimSpace(slot), ZeroUnset: true})
	}
	return out
}

// isRepresentableZero reports whether a slot holds a present, parseable numeric zero
// such as "0", " 0 ", "+0", or "-0". That value fits uint16, but pairItem may still
// drop it by treating the whole pair as absent.
func isRepresentableZero(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	n, err := strconv.Atoi(s)
	return err == nil && n == 0
}

// slotValueDropped reports whether a resolved trkn/disk slot value is lost on write: a value the
// uint16 atom cannot hold, or a literal 0 (decodePair drops a 0 slot on read, so it never
// round-trips). It is the shared slot-level predicate so the writer's dropped-value report
// (appendSlotDrop, whose two-case switch is this same disjunction) and the transfer capability
// grading stay in lockstep on which slot values MP4 drops - otherwise a copy of TRACKNUMBER=0 (or
// 0/total) would be graded carried yet the writer drops it and it reads back absent.
func slotValueDropped(s string) bool {
	return uint16ValueDropped(s) || isRepresentableZero(s)
}

// uint16ValueDropped reports whether the trimmed slot string holds a value the
// uint16 trkn/disk atom cannot represent: a non-numeric value, a negative, or one
// past 65535. An empty slot is not a drop. A literal 0 also passes this check because
// it fits uint16; appendSlotDrop handles the separate case where pairItem drops 0 by
// treating the whole pair as absent.
//
// Transfer uses this same predicate for standalone total slots, keeping write behavior and
// transfer grading aligned.
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

// pairSlotKeys are the trkn/disk pair slots stored as fixed binary uint16 fields, in the
// order restoreUnstorablePairSlots scans them.
var pairSlotKeys = []tag.Key{tag.TrackNumber, tag.TrackTotal, tag.DiscNumber, tag.DiscTotal}

// restoreUnstorablePairSlots returns a copy of edited with any trkn/disk slot whose edited
// value is genuinely unstorable restored from base, plus whether any slot was restored. MP4
// homes a track/disc number in a fixed binary uint16, so a value the atom cannot hold would
// clear the slot and erase a good existing value; restoring keeps the old value rather than
// deleting it. A slot is restored only when the edited value is unstorable (uint16ValueDropped:
// non-numeric, negative, or past 65535) AND base holds a storable, present value for it.
// Deliberately excluded: a representable literal 0 (the separate ZeroUnset case, written as
// 0/N and read back absent by design - uint16ValueDropped is false for it), an empty or
// cleared slot (also false), and a base slot that is itself unstorable (nothing worth
// restoring). The caller runs the value-dropped warning pass on the pre-restore tags, so the
// warning still fires; restoring base's value then lets an edit that changed nothing else
// collapse to a true no-op that still carries the warning forward.
func restoreUnstorablePairSlots(base, edited tag.TagSet) (tag.TagSet, bool) {
	out := edited
	restored := false
	for _, key := range pairSlotKeys {
		ev, _ := edited.First(key)
		if !uint16ValueDropped(ev) {
			continue // storable, empty, or a representable 0: leave the edited slot as-is
		}
		bv, ok := base.First(key)
		if !ok || bv == "" || uint16ValueDropped(bv) {
			continue // base has no storable value to restore for this slot
		}
		if !restored {
			out = edited.Clone()
			restored = true
		}
		out.Set(key, bv)
	}
	return out, restored
}

// numberComponentDropped builds the transfer-layer value-drop predicate for a TRACKNUMBER or
// DISCNUMBER key. WithValueDrop fixes the predicate signature to func(string) bool with no key, but
// the guarded split needs one, so this is a closure factory keyed on the number key (mirroring
// vocabValueDropped below). It resolves the number side through the same guarded tag.NumberTotalSplit
// the writer uses: a genuine "n/total" pair is judged on its number substring, while a malformed or
// non-numeric value ("1/2/3", "3/abc") is kept whole and judged (and dropped) as one value, so copy
// grading stays in lockstep with the writer instead of grading a lenient partial carried. The
// embedded total is graded separately; the pair-level zero collapse is not reproduced here because
// that rule depends on the sibling total slot.
func numberComponentDropped(k tag.Key) func(string) bool {
	return func(s string) bool {
		num, _, _ := tag.NumberTotalSplit(k, s)
		return slotValueDropped(num)
	}
}

// vocabValueDropped builds the value-drop predicate for a vocabulary atom such as stik or
// cpil. The transfer layer passes raw values for these keys, so trim here and exempt empty
// values, which intentionally store nothing.
func vocabValueDropped(k tag.Key, valid func(tag.Key, string) bool) func(string) bool {
	return func(val string) bool {
		v := strings.TrimSpace(val)
		return v != "" && !valid(k, v)
	}
}

// mediaTypeValueDropped and compilationValueDropped are shared by transfer grading and the
// writer's dropped-value report.
var (
	mediaTypeValueDropped   = vocabValueDropped(tag.MediaType, tag.ValidMediaTypeValue)
	compilationValueDropped = vocabValueDropped(tag.Compilation, tag.ValidBooleanValue)
)

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
