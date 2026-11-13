package ape

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/waxerr"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// Header/footer flags: bit 31 = has header, bit 29 = this record is the header.
// Bit 30 (no footer) is never set; every written tag has a footer for end-scan.
const (
	flagIsHeader = 1 << 29
	flagNoFooter = 1 << 30
	// shapeFlags state the tag's shape; [Render] sets them from what it writes.
	shapeFlags = flagHasHeader | flagNoFooter | flagIsHeader
	// WriteVersion is the version a new tag is written in: APEv2.
	WriteVersion = 2000
	// headerPresentBit: both records of a headed tag (footer scan steps back).
	headerPresentBit = flagHasHeader
	headerFlagsBits  = flagHasHeader | flagIsHeader
)

// DiffKeys: shared with Vorbis so rebuilds agree on what changed.
func DiffKeys(base, edited tag.TagSet) map[tag.Key]bool { return core.DiffKeys(base, edited) }

// reservedItemNames: APEv2-forbidden names -> magic they would forge.
var reservedItemNames = map[string]string{
	"ID3":  "an ID3v2 header's",
	"TAG":  "an ID3v1 tag's",
	"OggS": "an Ogg page's",
	"MP+":  "a Musepack stream's",
}

// ReservedItemName: case-folded APEv2-reserved names (CLI uppercases keys).
// Value rule, not charset (same split as vorbis reserved namespaces); [InvalidItemName] is the
// length and charset rule.
func ReservedItemName(name string) bool {
	_, ok := reservedMagic(name)
	return ok
}

// reservedMagic: collision target and whether reserved (case-folded scan).
func reservedMagic(name string) (string, bool) {
	for r, magic := range reservedItemNames {
		if strings.EqualFold(name, r) {
			return magic, true
		}
	}
	return "", false
}

// InvalidItemName reports a name the APEv2 specification does not allow for an item, and why: a name
// is 2 to 255 bytes of printable ASCII (0x20 to 0x7E).
func InvalidItemName(name string) (reason string, bad bool) {
	const rule = "an APEv2 item name is 2 to 255 characters of printable ASCII"
	if n := len(name); n < 2 || n > 255 {
		return fmt.Sprintf("%s, and this one is %d long", rule, n), true
	}
	for i := range len(name) {
		if c := name[i]; c < 0x20 || c > 0x7E {
			return fmt.Sprintf("%s, and this one holds byte 0x%02x", rule, c), true
		}
	}
	return "", false
}

// NameDrop is a key [Rebuild] did not write because its item name is outside the specification.
type NameDrop struct {
	Key    tag.Key
	Reason string // [InvalidItemName]'s, for the name the item would have had
}

// nameRefusal is why a text value cannot be written under an item name.
type nameRefusal uint8

const (
	nameWritable nameRefusal = iota
	nameReserved             // the specification reserves it
	nameInvalid              // outside the specification's length or characters
	nameCover                // a cover item's, which taggers type binary
)

// refusal reports why a text item cannot have name, with [InvalidItemName]'s reason for a name
// outside the specification.
func refusal(name string) (nameRefusal, string) {
	if ReservedItemName(name) {
		return nameReserved, ""
	}
	if reason, bad := InvalidItemName(name); bad {
		return nameInvalid, reason
	}
	if IsCoverKey(name) {
		return nameCover, ""
	}
	return nameWritable, ""
}

// heldName is the item a rewrite of a key keeps the name and flags of.
type heldName struct {
	name  string
	flags uint32
}

// heldNames maps each key the text items hold to the item a rewrite of the key keeps the name of:
// the first that holds it under a name a text item may have, else the first that holds it.
func heldNames(items []Item) map[tag.Key]heldName {
	m := map[tag.Key]heldName{}
	settled := map[tag.Key]bool{}
	for _, it := range items {
		if it.NonText() {
			continue
		}
		key, ok := mapping.CanonicalAPE(it.Key)
		if !ok || settled[key] {
			continue
		}
		w, _ := refusal(it.Key)
		if _, seen := m[key]; !seen || w == nameWritable {
			m[key] = heldName{it.Key, it.Flags}
		}
		settled[key] = w == nameWritable
	}
	return m
}

// textName returns the item name a text value of key k is written under: held, the name the file
// holds the key by ([heldNames], "" for none), where a text item may have it, else the key's own
// name ([mapping.APEName]). When neither will do, why and reason say what is wrong with the key's
// own. Shared by the writer and the transfer classifier.
func textName(k tag.Key, held string) (name string, why nameRefusal, reason string) {
	if held != "" {
		if w, _ := refusal(held); w == nameWritable {
			return held, nameWritable, ""
		}
	}
	name = mapping.APEName(k)
	why, reason = refusal(name)
	return name, why, reason
}

// DropsTrailingEmpty reports values an item cannot hold whole: a NUL joins the values of an item,
// and one at the end reads as the terminator some taggers write, so an empty value after another
// value is lost. An empty value ahead of or between others is stored.
func DropsTrailingEmpty(vals []string) bool {
	return len(vals) > 1 && vals[len(vals)-1] == ""
}

// TrailingEmptyReason: shared by the transfer grade and the write warning.
const TrailingEmptyReason = "an empty value at the end of an APEv2 item reads as a terminator, so the empty values after the last value are dropped"

// RebuildInfo: what [Rebuild] could not write (caller surfaces warnings).
type RebuildInfo struct {
	// ReservedKeys: reserved item names, rebuild order; not written.
	ReservedKeys []tag.Key
	// InvalidNames: names outside the specification, rebuild order; not written.
	InvalidNames []NameDrop
	// TrailingEmptyKeys: keys whose empty values at the end were not written ([DropsTrailingEmpty]).
	TrailingEmptyKeys []tag.Key
	// SlotDroppedCovers: roles of the pictures left without an item name ([PartitionCovers]).
	SlotDroppedCovers []core.PictureType
	// MalformedCoversReplaced: undecodable covers displaced by a written cover same name.
	MalformedCoversReplaced []string
	// CoverNameKeys: text refused under Cover Art names; pre-existing preserved unless
	// a cover write displaces it (CoverTextReplaced).
	CoverNameKeys []tag.Key
	// NonTextReplaced: authored text displaced same-name non-text (case-folded).
	NonTextReplaced []tag.Key
	// CoverTextReplaced: text squatting on a Cover Art name this edit wrote.
	CoverTextReplaced []tag.Key
}

// RebuildWarnings: reserved/Cover Art text drops, slot drops, malformed-cover
// replace, non-text/cover-text displace. All escalate --strict.
func RebuildWarnings(prior []core.Warning, info RebuildInfo) []core.Warning {
	for _, k := range info.ReservedKeys {
		magic, _ := reservedMagic(string(k)) // k reached ReservedKeys through the same lookup
		prior = core.WarnKeyed(prior, core.WarnValueDropped,
			fmt.Sprintf("%s is an item name the APEv2 specification reserves (it is %s magic) and cannot be written", k, magic), k)
	}
	for _, d := range info.InvalidNames {
		prior = core.WarnKeyed(prior, core.WarnValueDropped,
			fmt.Sprintf("%s cannot be written: %s", core.WarnSnippet(string(d.Key)), d.Reason), d.Key)
	}
	for _, k := range info.TrailingEmptyKeys {
		prior = core.WarnKeyed(prior, core.WarnValueDropped,
			fmt.Sprintf("%s: %s", core.WarnSnippet(string(k)), TrailingEmptyReason), k)
	}
	for _, k := range info.CoverNameKeys {
		prior = core.WarnKeyed(prior, core.WarnValueDropped,
			fmt.Sprintf("%s is a cover art item name, which taggers type binary; a text value cannot be written (cover art is edited as a picture)", k), k)
	}
	for _, t := range info.SlotDroppedCovers {
		prior = core.Warn(prior, core.WarnPictureUnsupported,
			fmt.Sprintf("the %s picture was dropped: %s", t, CoverSlotsReason))
	}
	for _, name := range info.MalformedCoversReplaced {
		// Write destruction => WarnMalformedTagEntryDropped (not WarnInvalidPicture:
		// --force covers warn invalid-picture at plan; escalating that would refuse).
		prior = core.Warn(prior, core.WarnMalformedTagEntryDropped,
			fmt.Sprintf("an undecodable %s item was replaced by this picture edit", name))
	}
	for _, k := range info.NonTextReplaced {
		// Key goes in Keys (--strict prefixes); match Matroska emitter shape.
		prior = core.WarnKeyed(prior, core.WarnTagStructureDropped,
			"a non-text item under this name was replaced by the edited value; APEv2 item names are unique, so its payload could not be kept", k)
	}
	for _, k := range info.CoverTextReplaced {
		prior = core.WarnKeyed(prior, core.WarnValueDropped,
			fmt.Sprintf("%s: a text item held this Cover Art name; the picture edit wrote its cover there, so the text value could not be kept", k), k)
	}
	return prior
}

// Rebuild: minimal change (order, flags, byte-identical untouched items; NUL-join
// multi). Pictures applied only when picturesChanged ([emitPictures]). Reserved/[IsCoverKey]
// text not written (recorded); pre-existing such names preserved. Unique names: authored text
// displaces same-name non-text; a cover written anew displaces squatting text. Source
// collisions preserved as found.
func Rebuild(orig []Item, base, edited tag.TagSet, pictures []core.Picture, picturesChanged bool) ([]Item, RebuildInfo) {
	changed := DiffKeys(base, edited)
	emitted := map[tag.Key]bool{}
	// nameRefused: emit refused; orig loop restores prior bytes. emit marks before decide.
	nameRefused := map[tag.Key]bool{}
	// origTextFold: source text names. Hit => re-render; miss => authoredText claims name.
	// authoredCovers: cover-name analogue from emitPictures.
	held := heldNames(orig)
	origTextFold := map[string]bool{}
	authoredText := map[string]tag.Key{}
	var authoredCovers map[string]bool
	out := make([]Item, 0, len(orig)+len(pictures))
	picturesEmitted := false
	var info RebuildInfo

	emitPictures := func() {
		var written []Item
		written, authoredCovers = emitPictures(orig, pictures, &info)
		out = append(out, written...)
		picturesEmitted = true
	}
	// emit: one item for k, under the name the file holds it by where a text item may have that
	// name, else under the key's own ([heldNames], [textName]); empty set => --clear. Keep source
	// flags. The values are joined with NULs, empty ones too, but for those at the end (inverse of
	// splitItemValues).
	emit := func(k tag.Key) {
		emitted[k] = true
		vals, ok := edited.Get(k)
		if !ok || len(vals) == 0 {
			return
		}
		name, why, reason := textName(k, held[k].name)
		flags := held[k].flags
		switch why {
		case nameReserved:
			info.ReservedKeys = append(info.ReservedKeys, k)
		case nameInvalid:
			info.InvalidNames = append(info.InvalidNames, NameDrop{k, reason})
		case nameCover:
			info.CoverNameKeys = append(info.CoverNameKeys, k)
		}
		if why != nameWritable {
			nameRefused[k] = true
			return
		}
		kept := slices.Clone(vals)
		if tag.IsBooleanKey(k) {
			for i, v := range kept {
				kept[i] = tag.CanonicalBoolValue(v)
			}
		}
		if DropsTrailingEmpty(kept) {
			info.TrailingEmptyKeys = append(info.TrailingEmptyKeys, k)
			for len(kept) > 1 && kept[len(kept)-1] == "" {
				kept = kept[:len(kept)-1]
			}
		}
		if fold := strings.ToLower(name); !origTextFold[fold] {
			authoredText[fold] = k // a fresh name: it displaces a non-text holder below
		}
		out = append(out, Item{Key: name, Value: strings.Join(kept, "\x00"), Flags: flags})
	}

	for _, it := range orig {
		if !it.NonText() {
			origTextFold[strings.ToLower(it.Key)] = true
		}
	}

	for _, it := range orig {
		if it.NonText() {
			if IsCoverKey(it.Key) {
				if picturesChanged {
					if !picturesEmitted {
						emitPictures()
					}
					continue // the source cover items are replaced by the edited set
				}
			}
			out = append(out, it) // preserved verbatim, flags included
			continue
		}
		key, ok := mapping.CanonicalAPE(it.Key)
		if !ok {
			out = append(out, it) // no canonical key: nothing can have edited it
			continue
		}
		// Slash holds number+total; on either change rewrite number without slash and
		// mark both emitted (else unrelated edit appends stray total). Same as vorbis.
		if key == tag.TrackNumber || key == tag.DiscNumber {
			// The value as it reads: a terminator some tagger wrote hides no slash.
			if vals := splitItemValues(it.Value); len(vals) == 1 && slashed(key, vals[0]) {
				totKey := tag.TotalKey(key)
				if changed[key] || changed[totKey] {
					if !emitted[key] {
						emit(key) // number only, keeping the file's spelling
					}
					if _, own := held[totKey]; !own && !emitted[totKey] {
						emit(totKey) // derived total with no item of its own
					}
					continue
				}
				emitted[totKey] = true // preserved in the slash below; do not append a copy
			}
		}
		if !changed[key] {
			out = append(out, it)
			emitted[key] = true
			continue
		}
		if !emitted[key] {
			emit(key) // a later item of the key collapses into this one
		}
		if nameRefused[key] {
			out = append(out, it) // refused write: every item of the key keeps its bytes
		}
	}

	// New keys in tag-set order under conventional APE spelling.
	for _, k := range edited.Keys() {
		if !emitted[k] {
			emit(k)
		}
	}
	if picturesChanged && !picturesEmitted {
		emitPictures()
	}
	// Displace preserved items whose name fresh output now holds. Source collisions untouched.
	if len(authoredText) > 0 || len(authoredCovers) > 0 {
		kept := out[:0]
		for _, it := range out {
			fold := strings.ToLower(it.Key)
			if it.NonText() {
				if k, ok := authoredText[fold]; ok {
					info.NonTextReplaced = append(info.NonTextReplaced, k)
					continue
				}
			} else if authoredCovers[fold] {
				k, ok := mapping.CanonicalAPE(it.Key)
				if !ok {
					k = tag.Key(strings.ToUpper(it.Key))
				}
				info.CoverTextReplaced = append(info.CoverTextReplaced, k)
				continue
			}
			kept = append(kept, it)
		}
		out = kept
	}
	return out, info
}

// slashed reports a number that holds its total behind a slash.
func slashed(key tag.Key, value string) bool {
	_, _, split := tag.NumberTotalSplit(key, value)
	return split
}

// emitPictures renders the cover items of a picture edit, in picture order ([assignCovers]): a
// picture the file already holds is the item that holds it, as it is, and any other is written
// anew. The undecodable cover items of orig follow, but for one whose name a picture written anew
// took. It returns the items and the names written anew, lower case, and records in info the
// pictures left without a name and the undecodable items replaced.
func emitPictures(orig []Item, pictures []core.Picture, info *RebuildInfo) (items []Item, anew map[string]bool) {
	anew = map[string]bool{}
	stored := make([]bool, len(pictures))
	for _, s := range assignCovers(pictures, nil, orig) {
		stored[s.pic] = true
		if s.from >= 0 {
			items = append(items, orig[s.from])
			continue
		}
		anew[strings.ToLower(s.name)] = true
		items = append(items, encodeCoverAs(pictures[s.pic], s.name))
	}
	for i, p := range pictures {
		if !stored[i] {
			info.SlotDroppedCovers = append(info.SlotDroppedCovers, p.Type)
		}
	}
	for _, it := range malformedCovers(orig) {
		if anew[strings.ToLower(it.Key)] {
			info.MalformedCoversReplaced = append(info.MalformedCoversReplaced, it.Key)
			continue
		}
		items = append(items, it)
	}
	return items, anew
}

// malformedCovers: undecodable covers to preserve on picture edit (like FLAC).
func malformedCovers(orig []Item) []Item {
	var out []Item
	for _, it := range orig {
		if UndecodableCover(it) {
			out = append(out, it)
		}
	}
	return out
}

// UndecodableCover reports a cover item no picture can be read from: one with no file name
// terminator or no image bytes. A picture edit keeps it; a clear of the pictures removes it.
func UndecodableCover(it Item) bool {
	if !it.NonText() || !IsCoverKey(it.Key) {
		return false
	}
	_, _, ok := coverImage(it)
	return !ok
}

// CheckCoverFormats refuses a link picture: a cover item holds image bytes, so the URL would be
// written as a cover and read back as one. A transfer drops such a picture before the write
// (the pictures capability's PictureMIMEs).
func CheckCoverFormats(pics []core.Picture) error {
	for _, p := range pics {
		if p.MIME == core.LinkMIME {
			return fmt.Errorf("%w: an APEv2 cover art item holds an image, not a link to one (got a URL picture)", waxerr.ErrUnsupportedTag)
		}
	}
	return nil
}

// Render limits: ffmpeg APE reader caps; maxItemBytes is the 32-bit length field.
const (
	maxTagBytes  = 16 << 20
	maxItems     = 1 << 16
	maxItemBytes = 1<<32 - 1
)

// Render: optional header, items, footer. Size = items+footer. Keep source
// version/hasHeader; new tags are APEv2+header. extraFlags are the flags the source tag carried
// ([Tag.Flags]), written into both records but for the bits that state the shape.
func Render(items []Item, version int, hasHeader bool, extraFlags uint32) ([]byte, error) {
	if err := checkRenderLimits(items); err != nil {
		return nil, err
	}
	// Pre-size for cover payloads.
	total := footerLen
	for _, it := range items {
		total += 8 + len(it.Key) + 1 + len(it.Payload())
	}
	tagSize := total // items + footer, per the specification
	if tagSize > maxTagBytes {
		got, max := bits.SizeOverLimit(int64(tagSize), maxTagBytes)
		return nil, fmt.Errorf("%w: APE tag is %s, max %s (readers refuse a larger tag outright, losing every item)",
			waxerr.ErrPictureTooLarge, got, max)
	}
	if hasHeader {
		total += footerLen
	}

	extraFlags &^= shapeFlags
	out := make([]byte, 0, total)
	if hasHeader {
		out = append(out, record{version: version, size: int64(tagSize), count: uint32(len(items)), flags: headerFlagsBits | extraFlags}.encode()...)
	}
	for _, it := range items {
		payload := it.Payload()
		var head [8]byte
		binary.LittleEndian.PutUint32(head[0:4], uint32(len(payload)))
		binary.LittleEndian.PutUint32(head[4:8], it.Flags)
		out = append(out, head[:]...)
		out = append(out, it.Key...)
		out = append(out, 0)
		out = append(out, payload...)
	}
	footerFlags := extraFlags
	if hasHeader {
		footerFlags |= headerPresentBit
	}
	return append(out, record{version: version, size: int64(tagSize), count: uint32(len(items)), flags: footerFlags}.encode()...), nil
}

// checkRenderLimits: refuse lists past reader/field limits.
func checkRenderLimits(items []Item) error {
	if len(items) > maxItems {
		return fmt.Errorf("%w: %d APE items exceeds the %d readers accept", waxerr.ErrSizeTooLarge, len(items), maxItems)
	}
	for _, it := range items {
		if n := len(it.Payload()); int64(n) > maxItemBytes {
			got, max := bits.SizeOverLimit(int64(n), maxItemBytes)
			return fmt.Errorf("%w: APE item %q is %s, past the %s of the 32-bit item-length field",
				waxerr.ErrSizeTooLarge, it.Key, got, max)
		}
	}
	return nil
}
