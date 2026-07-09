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
	out = append(out, preserved...)
	return out
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
// represent and would otherwise silently drop. ZeroUnset marks the one drop that is not a
// hard rejection: a literal 0 in a trkn/disk slot, whose bytes ARE written (0/N) but which
// decodePair reads back as absent (its num>0/total>0 guards treat 0 as unset), so it is a
// round-trip loss rather than an unrepresentable value. The write.go warning wording keys off
// it; a uint16-overflow or non-numeric drop leaves it false.
type droppedValue struct {
	Key       tag.Key
	Value     string
	ZeroUnset bool
	// Normalized is the canonical on-disk form of a coerced trkn/disk number slot ("03" becomes
	// "3"), set only by appendSlotCoerced, so the coercion warning can show what was actually
	// stored rather than just that the value changed. It is empty for a COMPILATION coercion, whose
	// stored form is always "0 (false)", and for every drop, where the value is lost rather than
	// normalized.
	Normalized string
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

// coercedValues returns the canonical values this edit stores in a normalized form because
// the iTunes atom cannot hold the literal. Unlike droppedValues, these ARE written (the
// change set shows the stored value); the warning only tells the user the literal was
// normalized. It reuses the droppedValue (key, value) carrier. Two cases:
//   - COMPILATION: cpil is a single boolean byte, so boolItem coerces a non-boolean like "maybe"
//     to 0 (false) and writes it, rather than dropping it.
//   - a trkn/disk number slot stored non-canonically ("03", "+3"): the uint16 atom holds the
//     integer 3, so the leading zero or sign is normalized away and reads back as "3". Reporting it
//     keeps the direct-set write path in step with the copy grade (WithValueReduction), so a bare
//     set TRACKNUMBER=03 emits a coercion note just as a copy of it grades Lossy.
//
// An empty value is exempt because it stores nothing. A dropped slot never appears here: drop and
// reduce are mutually exclusive per slot, since a reduced value is numeric, positive, and in range,
// so no drop predicate fires on it.
func coercedValues(ts tag.TagSet) []droppedValue {
	var out []droppedValue
	if v, ok := ts.First(tag.Compilation); ok && compilationValueDropped(v) {
		out = append(out, droppedValue{Key: tag.Compilation, Value: strings.TrimSpace(v)})
	}
	out = appendCoercedPair(out, ts, tag.TrackNumber, tag.TrackTotal)
	out = appendCoercedPair(out, ts, tag.DiscNumber, tag.DiscTotal)
	return out
}

// resolvePairSlots resolves the two slots a trkn/disk number/total pair edits, the same way the
// encoder's numTotal/tag.ParseNumPair do: split the number field on "/" (tag.SplitNumberTotal), then
// let a present, raw-non-empty explicit total key override the "/total" tail. Like ParseNumPair, the
// override gates on the raw string rather than the trimmed one, so a whitespace-only total (which
// ParseNumPair reads as 0 and whose tail it discards) overrides here too and leaves no stale tail.
// The drop report (appendDroppedPair) and the coercion report (appendCoercedPair) share this one
// resolution, so they cannot drift on the "3/09" split rule.
func resolvePairSlots(ts tag.TagSet, numKey, totKey tag.Key) (numPart, totPart string) {
	numStr, _ := ts.First(numKey)
	totStr, _ := ts.First(totKey)
	numPart, totPart = tag.SplitNumberTotal(numStr)
	if totStr != "" {
		totPart = strings.TrimSpace(totStr)
	}
	return numPart, totPart
}

// appendCoercedPair adds the normalized slot(s) of one trkn/disk pair, resolving the two slots via
// resolvePairSlots (shared with appendDroppedPair) so it names the same slots the encoder reads. It
// is the reduce-side parallel of appendDroppedPair.
func appendCoercedPair(out []droppedValue, ts tag.TagSet, numKey, totKey tag.Key) []droppedValue {
	numPart, totPart := resolvePairSlots(ts, numKey, totKey)
	out = appendSlotCoerced(out, numKey, numPart)
	out = appendSlotCoerced(out, totKey, totPart)
	return out
}

// appendSlotCoerced records one trkn/disk slot the encoder stores in a normalized form (a leading
// zero or sign the uint16 atom drops), carrying the canonical integer it becomes so the warning can
// show the on-disk value. It is the reduce-side parallel of appendSlotDrop. reducedSlotValue does
// the single parse and hands back both the trimmed literal and its canonical form, so this needs no
// second parse and no discarded error coupled to the predicate's internals.
func appendSlotCoerced(out []droppedValue, key tag.Key, slot string) []droppedValue {
	if trimmed, canonical, ok := reducedSlotValue(slot); ok {
		return append(out, droppedValue{Key: key, Value: trimmed, Normalized: canonical})
	}
	return out
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

// reducedSlotValue reports whether a resolved trkn/disk slot is representable but stored in a
// normalized form and, when so, hands back both the trimmed input and the canonical integer it
// becomes. A slot is reduced when it is a positive integer within the uint16 range whose canonical
// decimal differs from the input, i.e. a leading zero or an explicit sign ("03", "+3") stored as 3.
// It is the single parse both the reduction predicate (slotValueReduced) and the writer's coercion
// note (appendSlotCoerced) read, so they cannot drift on what "normalized" means, and the note needs
// no second parse with a discarded error.
//
// The n > 0 guard matters here: a representable zero ("0"/"00"/"+0") fits uint16 yet reads back
// absent, because decodePair treats a 0 slot as unset. That makes it a drop (isRepresentableZero /
// slotValueDropped), not a reduction. dispose checks the drop predicate before this one, so a 0
// never reaches here anyway, but the guard is what keeps it graded Dropped rather than mislabeled
// Lossy. Non-numeric and out-of-uint16 values are the drop predicate's job too, not this one.
func reducedSlotValue(s string) (trimmed, canonical string, reduced bool) {
	trimmed = strings.TrimSpace(s)
	n, err := strconv.Atoi(trimmed)
	if err != nil || n <= 0 || n > 0xFFFF {
		return trimmed, "", false
	}
	canonical = strconv.Itoa(n)
	return trimmed, canonical, canonical != trimmed
}

// slotValueReduced is the bool-only reduction predicate the transfer grading (WithValueReduction)
// and numberComponentReduced consume; it delegates to reducedSlotValue.
func slotValueReduced(s string) bool {
	_, _, reduced := reducedSlotValue(s)
	return reduced
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

// numberComponentDropped is the transfer-layer value-drop predicate for TRACKNUMBER and
// DISCNUMBER. It judges only the number side of a possible "n/total" value; the embedded
// total is graded separately. It does not reproduce pairItem's pair-level zero collapse
// because that rule depends on the sibling total slot.
func numberComponentDropped(s string) bool {
	num, _ := tag.SplitNumberTotal(s)
	return slotValueDropped(num)
}

// numberComponentReduced is the transfer-layer value-reduction predicate for TRACKNUMBER and
// DISCNUMBER, the reduce-side parallel of numberComponentDropped. It judges only the number side of
// a possible "n/total" value (the embedded total is graded separately by its own total key) via
// slotValueReduced.
func numberComponentReduced(s string) bool {
	num, _ := tag.SplitNumberTotal(s)
	return slotValueReduced(num)
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
