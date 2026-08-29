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

// Rebuild produces the new item list with minimal change: item order is preserved,
// unknown and non-text items keep their bytes and their flags (including the
// read-only bit, which a naive re-render would clear), an item whose canonical key
// did not change is left byte-identical, and multiple values are NUL-joined into one
// item the way APE stores them.
//
// pictures is the edited cover set; it is applied only when picturesChanged, so a
// tag-only edit leaves the source's cover items untouched rather than re-encoding
// them under a synthesized file name.
func Rebuild(orig []Item, base, edited tag.TagSet, pictures []core.Picture, picturesChanged bool) []Item {
	changed := DiffKeys(base, edited)
	emitted := map[tag.Key]bool{}
	out := make([]Item, 0, len(orig)+len(pictures))
	picturesEmitted := false

	emitPictures := func() {
		for _, p := range pictures {
			out = append(out, EncodeCover(p))
		}
		for _, it := range malformedCovers(orig) {
			out = append(out, it) // undecodable, so not in the edited set; still the user's bytes
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
	return out
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
