// Package ape implements APEv1/APEv2 tags for internal codecs.
//
// In MP3, APE is a trailing foreign/legacy container: family view + verbatim
// preserve; ID3 stays authoritative.
//
// In WavPack, Monkey's Audio, and Musepack it is the native store: [Project],
// [Rebuild], [Render]. Free-form UTF-8; only third-party conventions (e.g. Cover
// Art). No chapter or synced-lyrics convention.
//
// Reimplemented from the public APE tag specification.
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

// Tag is a parsed APE tag: items and byte extent (header/items/footer).
type Tag struct {
	Version int
	Items   []Item
	Offset  int64 // absolute start (header if present, else first item)
	Size    int64 // total bytes including header and footer
	// HasHeader: rewrite keeps the on-disk shape.
	HasHeader bool
	// Truncated: Items is incomplete (element cap). Rebuild must refuse.
	Truncated bool
}

// Item is one APE key/value pair.
//
// Data is the on-disk payload (all types); untouched items re-render from it
// (including non-UTF-8 APEv1/Latin-1). Value is decoded text (NUL-separated multi);
// empty for non-text. Writer-built items set Value and leave Data nil; [Item.Payload]
// resolves either. Flags is the full 32-bit word (preserve-unknown at byte level).
type Item struct {
	Key   string
	Value string
	Data  []byte
	Flags uint32
}

// Item flag bits. Bits 1-2: type (0 = text; else not projected).
const (
	flagReadOnly  = 1 << 0
	itemTypeShift = 1
	itemTypeMask  = 3

	itemTypeText   = 0
	itemTypeBinary = 1
)

// NonText: preserved, not projected as a tag value.
func (i Item) NonText() bool { return (i.Flags>>itemTypeShift)&itemTypeMask != itemTypeText }

// ReadOnly reports the read-only bit (preserved, not enforced).
func (i Item) ReadOnly() bool { return i.Flags&flagReadOnly != 0 }

// Payload: on-disk Data if present, else Value bytes.
func (i Item) Payload() []byte {
	if i.Data != nil {
		return i.Data
	}
	return []byte(i.Value)
}

// Clone deep-copies so Document accessors stay detached.
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

// ParseAt finds an APE footer ending at endOff. ok is false if none.
// maxElements caps Items; callers keep raw bytes separately.
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

	// Has-header moves the audio/tag boundary; confirm APETAGEX is present before
	// trusting the bit (a lying flag would overwrite audio and pass --verify).
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

// parseItems decodes up to count items; stops on malformed input; truncated if
// maxElements cuts the list. Cap is not fatal here (MP3 keeps raw bytes). Codecs
// that rebuild from Items refuse [Tag.Truncated].
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
		// Use len(raw)-pos (not pos+size): on 32-bit, large size can overflow.
		if size < 0 || size > len(raw)-pos {
			break
		}
		value := raw[pos : pos+size]
		pos += size
		// Cap after malformed checks so short input stays lenient; raw bytes kept elsewhere.
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

// ParseItemRun decodes a bare item run (Musepack SV8 chapter tag after preamble-less
// header). Same as parseItems; reports element-cap truncation.
func ParseItemRun(raw []byte, count uint32, maxElements int) ([]Item, bool) {
	return parseItems(raw, count, maxElements)
}

// decodeText: UTF-8 when valid, else Latin-1 (APEv1 / out-of-spec APEv2).
// Same legacy path as RIFF LIST/INFO. [InvalidUTF8Warnings] reports; raw bytes kept.
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

// cutKey: NUL-terminated ASCII key and bytes consumed, or n<0.
func cutKey(b []byte) (string, int) {
	for i, c := range b {
		if c == 0 {
			return string(b[:i]), i + 1
		}
	}
	return "", -1
}

// Pairs returns text-item canonical pairs in item order (family/source view).
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
			// Skip empty (unlike Project): legacy view; empty would block lint --fix.
			// Same as ID3v1 Pairs.
			if v == "" {
				continue
			}
			out = append(out, kv{key, v})
		}
	}
	return out
}

// splitItemValues: NUL-separated multi-value decode.
// Wholly empty item => one ""; empty runs inside multi are dropped (writer cannot
// express them via NUL join; trailing NULs are terminators).
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
