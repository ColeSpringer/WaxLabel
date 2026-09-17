package ape

import (
	"encoding/binary"
	"fmt"
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
	writeVersion = 2000 // APEv2
	// headerPresentBit: both records of a headed tag (footer scan steps back).
	headerPresentBit = flagHasHeader
	headerFlagsBits  = flagHasHeader | flagIsHeader
)

// NewEmpty: APEv2 with header (current tagger shape; rebuild base when none).
func NewEmpty() *Tag { return &Tag{Version: writeVersion, HasHeader: true} }

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
// Value rule, not charset (same split as vorbis reserved namespaces).
// Spec length 2-255 is NOT enforced: reserved is a collision hazard; short names work.
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

// RebuildInfo: what [Rebuild] could not write (caller surfaces warnings).
type RebuildInfo struct {
	// ReservedKeys: reserved item names, rebuild order; not written.
	ReservedKeys []tag.Key
	// SlotDroppedCovers: roles with no free Cover Art name ([PartitionCoverSlots]).
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
	for _, k := range info.CoverNameKeys {
		prior = core.WarnKeyed(prior, core.WarnValueDropped,
			fmt.Sprintf("%s is a Cover Art item name, which the convention types binary; a text value cannot be written (cover art is edited as a picture)", k), k)
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
// multi). Pictures applied only when picturesChanged. Reserved/[IsCoverKey] text not
// written (recorded); pre-existing such names preserved. Unique names: authored text
// displaces same-name non-text; written cover displaces squatting text. Source
// collisions preserved as found.
func Rebuild(orig []Item, base, edited tag.TagSet, pictures []core.Picture, picturesChanged bool) ([]Item, RebuildInfo) {
	changed := DiffKeys(base, edited)
	emitted := map[tag.Key]bool{}
	// nameRefused: emit refused; orig loop restores prior bytes. emit marks before decide.
	nameRefused := map[tag.Key]bool{}
	// origTextFold: source text names. Hit => re-render; miss => authoredText claims name.
	// authoredCovers: cover-name analogue from emitPictures.
	origTextFold := map[string]bool{}
	authoredText := map[string]tag.Key{}
	authoredCovers := map[string]bool{}
	out := make([]Item, 0, len(orig)+len(pictures))
	picturesEmitted := false
	var info RebuildInfo

	emitPictures := func() {
		// Slot assign (two names); drop is backstop. blocked = undecodable slots.
		malformed := malformedCovers(orig)
		blocked := map[string]bool{}
		for _, it := range malformed {
			blocked[coverKey(coverPictureType(it.Key))] = true
		}
		keptIdx, names := assignCoverSlots(pictures, nil, blocked)
		kept := make([]bool, len(pictures))
		for j, i := range keptIdx {
			kept[i] = true
			authoredCovers[strings.ToLower(names[j])] = true
			out = append(out, encodeCoverAs(pictures[i], names[j]))
		}
		for i, p := range pictures {
			if !kept[i] {
				info.SlotDroppedCovers = append(info.SlotDroppedCovers, p.Type)
			}
		}
		for _, it := range malformed {
			// Preserve undecodable unless this edit writes under the same name.
			if authoredCovers[strings.ToLower(coverKey(coverPictureType(it.Key)))] {
				info.MalformedCoversReplaced = append(info.MalformedCoversReplaced, it.Key)
				continue
			}
			out = append(out, it)
		}
		picturesEmitted = true
	}
	// emit: one item under name; empty set => --clear. Keep source flags.
	// Lone [""] stored; empty within multi dropped (inverse of splitItemValues).
	emit := func(k tag.Key, name string, flags uint32) {
		emitted[k] = true
		vals, ok := edited.Get(k)
		if !ok || len(vals) == 0 {
			return
		}
		if ReservedItemName(name) {
			info.ReservedKeys = append(info.ReservedKeys, k)
			nameRefused[k] = true
			return
		}
		if IsCoverKey(name) {
			// Cover Art names are binary; refuse text.
			info.CoverNameKeys = append(info.CoverNameKeys, k)
			nameRefused[k] = true
			return
		}
		boolean := tag.IsBooleanKey(k)
		kept := make([]string, 0, len(vals))
		for _, v := range vals {
			if boolean {
				v = tag.CanonicalBoolValue(v)
			}
			if v == "" && len(vals) > 1 {
				continue
			}
			kept = append(kept, v)
		}
		if len(kept) == 0 {
			return
		}
		if fold := strings.ToLower(name); !origTextFold[fold] {
			authoredText[fold] = k // a fresh name: it displaces a non-text holder below
		}
		out = append(out, Item{Key: name, Value: strings.Join(kept, "\x00"), Flags: flags})
	}

	// hasNative: slash-pair total only when no own total item.
	hasNative := map[tag.Key]bool{}
	for _, it := range orig {
		if it.NonText() {
			continue
		}
		origTextFold[strings.ToLower(it.Key)] = true
		if k, ok := mapping.CanonicalAPE(it.Key); ok {
			hasNative[k] = true
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
			if _, _, split := tag.NumberTotalSplit(key, it.Value); split {
				totKey := tag.TotalKey(key)
				if changed[key] || changed[totKey] {
					if !emitted[key] {
						emit(key, it.Key, it.Flags) // number only, keeping the file's spelling
					}
					if !hasNative[totKey] && !emitted[totKey] {
						emit(totKey, mapping.APEName(totKey), 0) // derived total with no item of its own
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
		if emitted[key] {
			continue // a duplicate item for an edited key collapses into the first
		}
		// Keep source spelling.
		emit(key, it.Key, it.Flags)
		if nameRefused[key] {
			// Refused write: keep prior bytes.
			out = append(out, it)
		}
	}

	// New keys in tag-set order under conventional APE spelling.
	for _, k := range edited.Keys() {
		if !emitted[k] {
			emit(k, mapping.APEName(k), 0)
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

// malformedCovers: undecodable covers to preserve on picture edit (like FLAC).
func malformedCovers(orig []Item) []Item {
	var out []Item
	for _, it := range orig {
		if it.NonText() && IsCoverKey(it.Key) {
			if _, err := DecodeCover(it.Key, it.Payload()); err != nil {
				out = append(out, it)
			}
		}
	}
	return out
}

// Render limits: ffmpeg APE reader caps; maxItemBytes is the 32-bit length field.
const (
	maxTagBytes  = 16 << 20
	maxItems     = 1 << 16
	maxItemBytes = 1<<32 - 1
)

// Render: optional header, items, footer. Size = items+footer. Keep source
// version/hasHeader; new tags are APEv2+header ([NewEmpty]).
func Render(items []Item, version int, hasHeader bool) ([]byte, error) {
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
		return nil, fmt.Errorf("%w: APE tag is %s (max %s; readers refuse a larger tag outright, losing every item)",
			waxerr.ErrPictureTooLarge, bits.HumanBytes(int64(tagSize)), bits.HumanBytes(int64(maxTagBytes)))
	}
	if hasHeader {
		total += footerLen
	}

	out := make([]byte, 0, total)
	if hasHeader {
		out = append(out, record(tagSize, len(items), version, headerFlagsBits)...)
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
	footerFlags := uint32(0)
	if hasHeader {
		footerFlags = headerPresentBit
	}
	return append(out, record(tagSize, len(items), version, footerFlags)...), nil
}

// checkRenderLimits: refuse lists past reader/field limits.
func checkRenderLimits(items []Item) error {
	if len(items) > maxItems {
		return fmt.Errorf("%w: %d APE items exceeds the %d readers accept", waxerr.ErrSizeTooLarge, len(items), maxItems)
	}
	for _, it := range items {
		if n := len(it.Payload()); int64(n) > maxItemBytes {
			return fmt.Errorf("%w: APE item %q is %s, past the 32-bit item-length field",
				waxerr.ErrSizeTooLarge, it.Key, bits.HumanBytes(int64(n)))
		}
	}
	return nil
}

// record: one 32-byte header or footer.
func record(tagSize, itemCount, version int, flags uint32) []byte {
	b := make([]byte, footerLen)
	copy(b[0:8], preamble)
	binary.LittleEndian.PutUint32(b[8:12], uint32(version))
	binary.LittleEndian.PutUint32(b[12:16], uint32(tagSize))
	binary.LittleEndian.PutUint32(b[16:20], uint32(itemCount))
	binary.LittleEndian.PutUint32(b[20:24], flags)
	return b // b[24:32] is the reserved field, which must be zero
}
