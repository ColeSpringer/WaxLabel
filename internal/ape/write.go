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

// Header and footer flag bits. Bit 31 says the tag has a header record and bit 29
// marks the record itself as the header rather than the footer. Bit 30, "the tag has
// no footer", is never set: every tag WaxLabel writes has one, since that is what a
// reader scanning backward from the end of a file finds it by.
const (
	flagIsHeader = 1 << 29
	writeVersion = 2000 // APEv2
	// headerPresentBit is set on both records of a tag that has a header, so a reader
	// scanning backward from the footer knows to step back over it.
	headerPresentBit = flagHasHeader
	headerFlagsBits  = flagHasHeader | flagIsHeader
)

// NewEmpty returns an empty APEv2 tag with a header, the base a codec rebuilds onto
// when the file carries no tag yet and the shape every current tagger writes.
func NewEmpty() *Tag { return &Tag{Version: writeVersion, HasHeader: true} }

// DiffKeys returns the canonical keys whose values differ between base and edited. It
// is the shared definition, so an APE rebuild and a Vorbis one cannot disagree about
// what an edit changed.
func DiffKeys(base, edited tag.TagSet) map[tag.Key]bool { return core.DiffKeys(base, edited) }

// reservedItemNames are the four item names the APEv2 specification forbids, mapped to what
// each would collide with: writing one puts a false signature inside the tag of a file the
// very readers that scan for it will open. One table, so a fifth entry cannot be dropped
// correctly while the warning misattributes it.
var reservedItemNames = map[string]string{
	"ID3":  "an ID3v2 header's",
	"TAG":  "an ID3v1 tag's",
	"OggS": "an Ogg page's",
	"MP+":  "a Musepack stream's",
}

// ReservedItemName reports whether name is one the APEv2 specification reserves. The
// comparison is case-folded because the canonical key arrives uppercased (the CLI
// uppercases keys, so "OggS" reaches the writer as "OGGS") and because a reader scanning
// for the magic is not case-sensitive about the hazard either.
//
// This is a value-level rule about four specific strings, not a charset, so it lives here
// rather than in tag.Key's byte predicate - the same split internal/vorbis makes for its
// reserved namespaces.
//
// The specification's companion 2-to-255 character length rule is deliberately NOT enforced
// alongside it, and the two should not be folded together by a later pass: a reserved name
// is a collision hazard (ID3 and TAG are actual tag magic, so writing one plants a false
// signature inside the file), while a one-character name is not. --set X=1 writes fine today
// and real APEv2 readers accept it, so enforcing the bound would newly drop a working key
// for no interop gain.
func ReservedItemName(name string) bool {
	_, ok := reservedMagic(name)
	return ok
}

// reservedMagic returns what a reserved item name would collide with, and whether the name is
// reserved at all. The comparison folds case, so the lookup is a scan rather than a map hit.
func reservedMagic(name string) (string, bool) {
	for r, magic := range reservedItemNames {
		if strings.EqualFold(name, r) {
			return magic, true
		}
	}
	return "", false
}

// RebuildInfo records what [Rebuild] could not write, for the caller to surface as
// warnings. Returning it rather than warning in place keeps Rebuild free of core.Warning
// assembly, mirroring internal/vorbis.
type RebuildInfo struct {
	// ReservedKeys lists the canonical keys whose item name the specification reserves, in
	// the order the rebuild reached them. Their values are not written.
	ReservedKeys []tag.Key
	// SlotDroppedCovers lists the roles of the pictures the two-name Cover Art convention
	// had no item left for ([PartitionCoverSlots]), in set order. They are not written; a
	// transfer filters them out before the writer, so this fires for direct edits.
	SlotDroppedCovers []core.PictureType
	// MalformedCoversReplaced lists the names of undecodable cover items dropped because
	// this picture edit wrote a decodable cover under the same item name. Keeping both
	// would break APEv2 name uniqueness, and the edit targeted that very slot.
	MalformedCoversReplaced []string
	// CoverNameKeys lists the keys refused because their item name belongs to the Cover
	// Art convention, which types those items binary: a text item there would collide
	// with any cover and mislead a reader that looks the name up. Like a reserved name,
	// the value is not written and a pre-existing item under the name is preserved -
	// unless the same edit also writes a cover under that name, which then displaces the
	// preserved text item too (recorded in CoverTextReplaced), since the name can hold
	// only the cover.
	CoverNameKeys []tag.Key
	// NonTextReplaced lists the keys whose freshly authored text item displaced a
	// preserved non-text item of the same name. The names fold case, as readers compare
	// them; a text item and a non-text one already sharing a name in the source is
	// pre-existing state and is left alone.
	NonTextReplaced []tag.Key
	// CoverTextReplaced lists the projected keys of text items that squatted on a Cover
	// Art name this picture edit wrote its cover under, so the text item was dropped
	// rather than emitted as a duplicate of the cover item.
	CoverTextReplaced []tag.Key
}

// RebuildWarnings appends the write-time warnings for what [Rebuild] recorded: a key
// whose item name is reserved, or is a Cover Art name the convention types binary, was
// dropped rather than written (WarnValueDropped); a picture was left without a cover
// item (WarnPictureUnsupported); an undecodable cover item was replaced by a decodable
// one under its name (WarnMalformedTagEntryDropped); a non-text item lost its name to an
// authored text item, or a text item lost a Cover Art name to a written cover
// (WarnTagStructureDropped / WarnValueDropped). Every code escalates the CLI's --strict
// gate, so none of the losses passes silently.
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
		// WarnMalformedTagEntryDropped, not the read path's WarnInvalidPicture: the item's
		// bytes are gone from the written file, destruction by this write, which is the
		// class that code exists to escalate under --strict (a --force'd unrecognized
		// cover also warns invalid-picture at plan time, so escalating that code would
		// turn an opted-in embed into a refusal).
		prior = core.Warn(prior, core.WarnMalformedTagEntryDropped,
			fmt.Sprintf("an undecodable %s item was replaced by this picture edit", name))
	}
	for _, k := range info.NonTextReplaced {
		// The message does not name the key: the CLI's --strict reason prefixes the
		// warning's Keys itself for this code, matching the Matroska emitter's shape.
		prior = core.WarnKeyed(prior, core.WarnTagStructureDropped,
			"a non-text item under this name was replaced by the edited value; APEv2 item names are unique, so its payload could not be kept", k)
	}
	for _, k := range info.CoverTextReplaced {
		prior = core.WarnKeyed(prior, core.WarnValueDropped,
			fmt.Sprintf("%s: a text item held this Cover Art name; the picture edit wrote its cover there, so the text value could not be kept", k), k)
	}
	return prior
}

// Rebuild produces the new item list with minimal change: item order is preserved,
// unknown and non-text items keep their bytes and their flags (including the
// read-only bit, which a naive re-render would clear), an item whose canonical key
// did not change is left byte-identical, and multiple values are NUL-joined into one
// item the way APE stores them.
//
// pictures is the edited cover set; it is applied only when picturesChanged, so a
// tag-only edit leaves the source's cover items untouched rather than re-encoding
// them under a synthesized file name.
//
// A key whose item name is reserved ([ReservedItemName]) or belongs to the Cover Art
// convention ([IsCoverKey], whose items are binary pictures, not text) is not written,
// and is recorded in the returned RebuildInfo so the caller can warn. A pre-existing item
// with such a name is preserved either way - whether this edit leaves it alone or tries
// to change it - so a refused write never costs the user the value the file already had
// on top of the one it could not store.
//
// APEv2 item names are unique within a tag, and readers compare them case-insensitively,
// so the rebuild never authors a duplicate: a freshly authored text item displaces a
// preserved non-text item of the same name, and a written cover item displaces a text
// item squatting on its name - each recorded for a warning. A collision the source
// already carried is the file's own state and is preserved as found.
func Rebuild(orig []Item, base, edited tag.TagSet, pictures []core.Picture, picturesChanged bool) ([]Item, RebuildInfo) {
	changed := DiffKeys(base, edited)
	emitted := map[tag.Key]bool{}
	// nameRefused records the keys emit refused (a reserved or Cover Art item name), so
	// the orig loop can put the item the file already had back rather than leaving the
	// user with neither the new value nor the old one. emit marks a key emitted before it
	// decides, which is what keeps the append loop from retrying it.
	nameRefused := map[tag.Key]bool{}
	// origTextFold holds the case-folded names of the source's text items. An emit under
	// a name found here re-renders an existing item, so any same-named non-text item is a
	// pre-existing collision to preserve; a name not found here is freshly authored and
	// claims the name (authoredText), displacing a non-text holder in the final pass.
	// authoredCovers is the cover-name analogue, filled by emitPictures.
	origTextFold := map[string]bool{}
	authoredText := map[string]tag.Key{}
	authoredCovers := map[string]bool{}
	out := make([]Item, 0, len(orig)+len(pictures))
	picturesEmitted := false
	var info RebuildInfo

	emitPictures := func() {
		// One item per cover name: APEv2 item names are unique within a tag, and the
		// convention has exactly two, so the slot assignment decides who is written and
		// under which name. The editor and a transfer already resolved the set through
		// the same engine, so a drop here is a backstop; it is still recorded for
		// RebuildWarnings. Slots an undecodable item holds are passed as blocked, so a
		// spilling role picks a free name over displacing junk when it can.
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
			// An undecodable cover is not in the edited set, so it is normally preserved -
			// still the user's bytes. But when this edit writes a decodable cover under the
			// very name the undecodable item holds, keeping both would emit a duplicate
			// name; the edit claimed that slot, so the junk is replaced, and warned.
			if authoredCovers[strings.ToLower(coverKey(coverPictureType(it.Key)))] {
				info.MalformedCoversReplaced = append(info.MalformedCoversReplaced, it.Key)
				continue
			}
			out = append(out, it)
		}
		picturesEmitted = true
	}
	// emit writes one item for a canonical key under the given name, joining a
	// multi-valued key with NULs. A key whose value set is now empty writes nothing,
	// which is how a --clear removes the item. flags carries the source item's flag
	// word so an edited value keeps its read-only bit and any undefined bits; a newly
	// created item passes 0.
	//
	// A lone empty value is stored rather than dropped: an APEv2 item may hold a
	// zero-length value, so a present [""] (what `set KEY=` produces) writes an empty
	// item instead of removing the key, the same distinction the editor draws before the
	// codec sees the set and the same one LIST/INFO makes for its ZSTR items. Within a
	// multi-value set an empty value is dropped, because the NUL join has no way to
	// express it: this is the exact inverse of splitItemValues, so every item written
	// here reads back as the value set that produced it.
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
			// The convention types this item binary; authoring it as text would collide
			// with any cover item and mislead a reader that looks the name up.
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

	// hasNative marks the canonical keys that already own an item. The slash-pair
	// rewrite below re-derives a total from the slash only when its total key has no
	// item of its own; an explicit total is left to the normal loop, which preserves or
	// replaces it in place.
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
		// A slashed Track/Disc item natively holds both the number and a derived total;
		// the read path splits "3/12" into TRACKNUMBER=3 + TRACKTOTAL=12. When either key
		// changed, rewrite the number from the edited value and drop the slash, or a
		// cleared or edited total would resurface when the preserved "3/12" is
		// re-projected. Marking BOTH keys emitted is what keeps an unrelated edit from
		// appending a stray total item for a value that is already in the slash. This
		// mirrors internal/vorbis, which faces the identical convention.
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
		// Re-render in place under the item's own spelling, so an edit to a file
		// written by another tagger does not rename its items.
		emit(key, it.Key, it.Flags)
		if nameRefused[key] {
			// The new value could not be written, so keep the bytes the file came with:
			// refusing to author the hazard is not a licence to delete an item the user
			// already had, and dropping both would leave neither value.
			out = append(out, it)
		}
	}

	// Keys the edit added, in the canonical order the tag set reports, under the
	// conventional APE spelling.
	for _, k := range edited.Keys() {
		if !emitted[k] {
			emit(k, mapping.APEName(k), 0)
		}
	}
	if picturesChanged && !picturesEmitted {
		emitPictures()
	}
	// The name-uniqueness pass: a preserved item whose name this edit's fresh output now
	// holds is displaced, whatever order the two landed in out. authoredText can never
	// hold a cover name (emit refuses those), so a non-text item displaced here is never
	// a cover this edit just wrote; the text items a written cover displaces are the ones
	// squatting on its name. Collisions the source already carried are in neither map.
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

// malformedCovers returns the cover items whose payload did not decode. A picture
// edit re-emits the decoded set, which by definition excludes these, so without this
// they would be dropped - destroying bytes the read path promised to preserve. FLAC
// keeps its undecodable PICTURE blocks for the same reason.
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

// Limits on a rendered tag. maxTagBytes and maxItems match what ffmpeg's APE reader
// accepts: a tag past either is not read at all, so a single oversized cover would
// take the title and artist down with it. maxItemBytes is the 32-bit item-length
// field, past which a size silently wraps into a structurally corrupt tag.
const (
	maxTagBytes  = 16 << 20
	maxItems     = 1 << 16
	maxItemBytes = 1<<32 - 1
)

// Render encodes items as a complete APE tag: an optional header record, the items,
// and a footer record. Both records carry the same version, size, and item count;
// they differ only in the header-marker flag. The size field counts the items plus
// the footer, not the header, which is what a reader scanning backward from the end
// of a file expects.
//
// version and hasHeader come from the tag being rewritten, so an APEv1 tag is not
// silently relabelled APEv2 (whose UTF-8 requirement its bytes may not meet) and a
// footer-only tag does not grow a header. A tag created from scratch is APEv2 with a
// header, which is what every current tagger writes; see [NewEmpty].
func Render(items []Item, version int, hasHeader bool) ([]byte, error) {
	if err := checkRenderLimits(items); err != nil {
		return nil, err
	}
	// Size the buffer up front: the payloads dominate, and a cover-bearing tag would
	// otherwise be grown and copied repeatedly on the way out.
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

// checkRenderLimits rejects an item list no APE reader would accept, rather than
// emitting a tag whose 32-bit length fields have silently wrapped.
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

// record renders one 32-byte header or footer.
func record(tagSize, itemCount, version int, flags uint32) []byte {
	b := make([]byte, footerLen)
	copy(b[0:8], preamble)
	binary.LittleEndian.PutUint32(b[8:12], uint32(version))
	binary.LittleEndian.PutUint32(b[12:16], uint32(tagSize))
	binary.LittleEndian.PutUint32(b[16:20], uint32(itemCount))
	binary.LittleEndian.PutUint32(b[20:24], flags)
	return b // b[24:32] is the reserved field, which must be zero
}
