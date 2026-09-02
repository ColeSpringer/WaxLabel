// Package ape implements APEv1/APEv2 tags for internal codecs, both reading and
// writing. It plays two roles.
//
// In MP3 an APE tag is a foreign/legacy container that shows up trailing some
// files: WaxLabel surfaces its values in the family/source view and preserves its
// bytes verbatim, but the native ID3 tag stays authoritative.
//
// In WavPack, Monkey's Audio, and Musepack it is the native tag store, so this
// package is also a writer: [Project] gives the canonical view, [Rebuild] applies
// an edit with minimal change, and [Render] emits the tag bytes.
//
// APE is free-form UTF-8 key/value with no registry, so the conventions it stores
// are only the ones third-party tools already read. Cover art is in (the
// "Cover Art (Front)" binary item foobar2000 and Mp3tag write); chapters and synced
// lyrics have no APE convention and are not invented here.
//
// It is reimplemented from the public APE tag specification.
package ape

import (
	"encoding/binary"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// preamble marks an APE header or footer.
const preamble = "APETAGEX"

// footerLen is the fixed size of the APE header and footer records.
const footerLen = 32

// footer flag bits.
const (
	flagHasHeader = 1 << 31 // the tag is prefixed by a header record
)

// Tag is a parsed APE tag: its decoded items and the byte extent it occupies
// (header, items, and footer) so a container codec can preserve or strip it.
type Tag struct {
	Version int
	Items   []Item
	Offset  int64 // absolute start of the tag (header if present, else first item)
	Size    int64 // total bytes occupied, including any header and the footer
	// HasHeader records that a header record was found ahead of the items, so a
	// rewrite keeps the shape the file had rather than adding or dropping one.
	HasHeader bool
	// Truncated records that the item list was cut short by the element cap, so Items
	// is not the whole tag. A codec that rebuilds the tag from Items must refuse to
	// write rather than silently dropping what it could not see.
	Truncated bool
}

// Item is one APE key/value pair.
//
// Data holds the payload exactly as it sits in the file, for every item type. A
// parsed item is written back from those bytes, so an item WaxLabel did not edit
// round-trips byte for byte even when its text is not valid UTF-8 (APEv1 was
// effectively Latin-1, and such items still turn up in APEv2 tags).
//
// Value is the decoded text view of a text item, NUL-separated for a multi-valued
// key, and empty for a non-text item (binary, external/locator, reserved). An item
// built by the writer sets Value and leaves Data nil; [Item.Payload] resolves either
// shape.
//
// Flags is the item's whole 32-bit flag field, kept verbatim. Only bits 0-2 are
// defined (bit 0 read-only, bits 1-2 the item type), but a rewrite that re-renders
// a preserved item must not quietly clear the read-only bit or an undefined one -
// preserve-unknown has to hold at the byte level, not just for the payload.
type Item struct {
	Key   string
	Value string
	Data  []byte
	Flags uint32
}

// Item flag bits. Bits 1-2 are the item type: 0 is UTF-8 text and everything else
// (binary, external/locator, reserved) is not projected as a value.
const (
	flagReadOnly  = 1 << 0
	itemTypeShift = 1
	itemTypeMask  = 3

	itemTypeText   = 0
	itemTypeBinary = 1
)

// NonText reports whether the item carries something other than UTF-8 text, and so
// is preserved but not projected as a tag value.
func (i Item) NonText() bool { return (i.Flags>>itemTypeShift)&itemTypeMask != itemTypeText }

// ReadOnly reports the item's read-only bit, which some taggers set on items a user
// should not edit. WaxLabel preserves it rather than acting on it.
func (i Item) ReadOnly() bool { return i.Flags&flagReadOnly != 0 }

// Payload returns the item's bytes: the ones parsed from the file when it has them,
// so a preserved item is re-rendered exactly as it was found, and otherwise the
// encoding of Value, for an item the writer built.
func (i Item) Payload() []byte {
	if i.Data != nil {
		return i.Data
	}
	return []byte(i.Value)
}

// Clone deep-copies the tag so a native document holding one stays detached.
func (t *Tag) Clone() *Tag {
	if t == nil {
		return nil
	}
	c := *t
	c.Items = make([]Item, len(t.Items))
	for i, it := range t.Items {
		it.Data = slices.Clone(it.Data)
		c.Items[i] = it
	}
	return &c
}

// ParseAt looks for an APE footer ending at endOff (the file size, or the start
// of a trailing ID3v1 tag) and decodes the tag if present. ok is false when
// there is no APE tag there. maxElements caps the decoded item list; callers
// preserve the raw tag bytes separately.
func ParseAt(src core.ReaderAtSized, endOff, limit int64, maxElements int) (*Tag, bool, error) {
	if endOff < footerLen {
		return nil, false, nil
	}
	foot, err := bits.ReadSlice(src, endOff-footerLen, footerLen, limit)
	if err != nil || string(foot[:8]) != preamble {
		return nil, false, nil //nolint:nilerr // absence is not an error
	}
	if binary.LittleEndian.Uint32(foot[20:24])&flagIsHeader != 0 {
		return nil, false, nil // this record marks itself a header, so it is not the footer
	}
	version := int(binary.LittleEndian.Uint32(foot[8:12]))
	tagSize := int64(binary.LittleEndian.Uint32(foot[12:16])) // items + footer
	itemCount := binary.LittleEndian.Uint32(foot[16:20])
	flags := binary.LittleEndian.Uint32(foot[20:24])

	if tagSize < footerLen || tagSize > endOff {
		return nil, false, nil
	}
	itemsStart := endOff - tagSize
	itemsLen := tagSize - footerLen

	items := []Item{}
	truncated := false
	if itemsLen > 0 {
		raw, err := bits.ReadSlice(src, itemsStart, itemsLen, limit)
		if err != nil {
			return nil, false, nil //nolint:nilerr
		}
		items, truncated = parseItems(raw, itemCount, maxElements)
	}

	// The has-header flag decides where the tag STARTS, which for the codecs that own
	// an APE tag is also where the verbatim audio copy ends. Trusting the bit alone lets
	// a file that merely sets it move the boundary 32 bytes into the last audio block,
	// so the rewrite writes the tag over audio - and --verify certifies the result,
	// because both sides derive the essence extent from the same wrong offset. Confirm
	// an APETAGEX record is really there before believing it.
	offset := itemsStart
	size := tagSize
	hasHeader := false
	if flags&flagHasHeader != 0 && itemsStart >= footerLen {
		if head, err := bits.ReadSlice(src, itemsStart-footerLen, footerLen, limit); err == nil &&
			string(head[:8]) == preamble {
			offset -= footerLen
			size += footerLen
			hasHeader = true
		}
	}
	return &Tag{
		Version: version, Items: items, Offset: offset, Size: size,
		HasHeader: hasHeader, Truncated: truncated,
	}, true, nil
}

// parseItems decodes up to count items from the item region. It stops on malformed
// input and caps the decoded list at maxElements, reporting truncated when the cap
// cut the list short.
//
// The cap is not fatal here because MP3 preserves the raw APE region separately and
// only reads this decoded view. The codecs that own an APE tag rebuild the whole tag
// from Items, so for them a truncated list would be silent data loss; they refuse to
// write a tag flagged [Tag.Truncated] instead.
func parseItems(raw []byte, count uint32, maxElements int) (items []Item, truncated bool) {
	pos := 0
	for range count {
		if pos+8 > len(raw) {
			break
		}
		size := int(binary.LittleEndian.Uint32(raw[pos : pos+4]))
		flags := binary.LittleEndian.Uint32(raw[pos+4 : pos+8])
		pos += 8
		key, n := cutKey(raw[pos:])
		if n < 0 {
			break
		}
		pos += n
		// Compare against len(raw)-pos instead of pos+size. On 32-bit builds, a
		// crafted size near 2 GiB can overflow pos+size before the bounds check.
		// pos is already within raw, and size < 0 catches uint32 values whose high
		// bit becomes negative after int conversion.
		if size < 0 || size > len(raw)-pos {
			break
		}
		value := raw[pos : pos+size]
		pos += size
		// Apply the cap after malformed-item checks so short or corrupt input still
		// exits through the lenient parse path. Hitting the cap just stops decoding;
		// raw bytes are kept elsewhere.
		if bits.CheckElementCap(len(items), maxElements, "APE items") != nil {
			return items, true // cap reached: the caller decides whether that is fatal
		}
		it := Item{Key: key, Flags: flags, Data: value}
		if !it.NonText() {
			it.Value = decodeText(value)
		}
		items = append(items, it)
	}
	return items, false
}

// ParseItemRun decodes a bare run of up to count items: the shape a Musepack SV8 chapter
// packet carries after its preamble-less header record. It is parseItems itself, so a
// chapter tag's items decode exactly as a trailing tag's do, and it reports whether the
// element cap cut the list short.
func ParseItemRun(raw []byte, count uint32, maxElements int) ([]Item, bool) {
	return parseItems(raw, count, maxElements)
}

// decodeText renders a text item's bytes as a string: as UTF-8 when they are valid
// (what APEv2 requires and what every modern tagger writes), else as Latin-1, so an
// APEv1 item - or an out-of-spec APEv2 one - yields a usable value instead of a
// string the canonical model will later refuse. This mirrors the RIFF LIST/INFO read
// path, which faces the same legacy code page. [InvalidUTF8Warnings] reports the
// files this fires on, and the item's raw bytes are preserved either way.
func decodeText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c) // Latin-1: each byte is its own code point
	}
	return string(r)
}

// cutKey reads a NUL-terminated ASCII key, returning it and the number of bytes
// consumed (including the terminator), or n<0 if no terminator is found.
func cutKey(b []byte) (string, int) {
	for i, c := range b {
		if c == 0 {
			return string(b[:i]), i + 1
		}
	}
	return "", -1
}

// Pairs returns the canonical key/value pairs the APE tag supplies (text items
// only), in item order, for the family/source view.
func (t *Tag) Pairs() []struct {
	Key   tag.Key
	Value string
} {
	type kv = struct {
		Key   tag.Key
		Value string
	}
	var out []kv
	for _, it := range t.Items {
		if it.NonText() {
			continue
		}
		key, ok := mapping.CanonicalAPE(it.Key)
		if !ok {
			continue
		}
		for _, v := range splitItemValues(it.Value) {
			// Skip an empty value here, unlike Project. This is the legacy-container view
			// (an MP3's trailing APEv2 alongside its authoritative ID3v2), where an empty
			// item carries nothing to preserve: counting it would make the key look
			// legacy-only and stop lint --fix from stripping an otherwise redundant
			// container. ID3v1's Pairs skips its blank fields for the same reason.
			if v == "" {
				continue
			}
			out = append(out, kv{key, v})
		}
	}
	return out
}

// splitItemValues decodes one text item's value into its canonical values. APEv2 stores
// multiple values for one key as NUL-separated runs inside a single item, so the split is
// the multi-value decode, not a heuristic.
//
// A wholly empty item yields one empty value: an APEv2 item may hold a zero-length value,
// and that is what `set KEY=` writes, so dropping it would report the key absent on a file
// that carries it. Empty runs inside a multi-run value are dropped instead, because a
// trailing or doubled NUL there is a writer's terminator rather than a value the file means
// to carry - and because the writer cannot express such a value through the NUL join, so
// keeping it would report a value no rewrite can store.
func splitItemValues(value string) []string {
	if value == "" {
		return []string{""}
	}
	var out []string
	for v := range strings.SplitSeq(value, "\x00") {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
