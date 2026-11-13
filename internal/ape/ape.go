// Package ape implements APEv1/APEv2 tags for internal codecs.
//
// In MP3, raw AAC and FLAC, APE is a trailing foreign/legacy container: family
// view + verbatim preserve; the format's own tags stay authoritative.
//
// In WavPack, Monkey's Audio, and Musepack it is the native store: [Project],
// [Rebuild], [Render]. Free-form UTF-8; only third-party conventions (e.g. Cover
// Art). No chapter or synced-lyrics convention.
//
// Reimplemented from the public APE tag specification.
package ape

import (
	"bytes"
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
	// Flags is the footer's flag word, with the bits the header record sets beside its own. A
	// rewrite carries it into both records but for the bits that state the tag's shape, so a tag
	// marked read-only in either stays marked; the mark does not stop an edit.
	Flags uint32
	// UnreadTail counts the item bytes no item was read from: those past the items the footer
	// counts, and everything from an item that does not parse. A rewrite drops them.
	UnreadTail int
}

// ReadOnly reports the tag's read-only flag (preserved, not enforced).
func (t *Tag) ReadOnly() bool { return t.Flags&flagReadOnly != 0 }

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

// Item flag bits. Bits 1-2: type (0 = text; else not projected). Bit 0 means the same in a tag's
// header and footer flags.
const (
	flagReadOnly  = 1 << 0
	itemTypeShift = 1
	itemTypeMask  = 3

	itemTypeText    = 0
	itemTypeBinary  = 1
	itemTypeLocator = 2
)

// NonText: preserved, not projected as a tag value.
func (i Item) NonText() bool { return (i.Flags>>itemTypeShift)&itemTypeMask != itemTypeText }

// ReadOnly reports the read-only bit (preserved, not enforced).
func (i Item) ReadOnly() bool { return i.Flags&flagReadOnly != 0 }

// TypeNote says what the item's flags make of it, for the native view: nothing for text, "binary",
// "external locator" or "reserved type 3", and "read-only" where the bit is set.
func (i Item) TypeNote() string {
	var notes []string
	switch (i.Flags >> itemTypeShift) & itemTypeMask {
	case itemTypeText:
	case itemTypeBinary:
		notes = append(notes, "binary")
	case itemTypeLocator:
		notes = append(notes, "external locator")
	default:
		notes = append(notes, "reserved type 3")
	}
	if i.ReadOnly() {
		notes = append(notes, "read-only")
	}
	return strings.Join(notes, ", ")
}

// Payload: on-disk Data if present, else Value bytes.
func (i Item) Payload() []byte {
	if i.Data != nil {
		return i.Data
	}
	return []byte(i.Value)
}

// SameItems reports whether a and b hold the same items in the same order: name, flags and
// payload bytes.
func SameItems(a, b []Item) bool {
	return slices.EqualFunc(a, b, func(x, y Item) bool {
		return x.Key == y.Key && x.Flags == y.Flags && bytes.Equal(x.Payload(), y.Payload())
	})
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

// ParseAt finds an APE footer ending at endOff. ok is false if none; a footer or item run over
// limit is refused with [waxerr.ErrSizeTooLarge], and a read that fails inside the file is an error.
// maxElements caps Items; callers keep raw bytes separately.
func ParseAt(src core.ReaderAtSized, endOff, limit int64, maxElements int) (*Tag, bool, error) {
	foot, ok, err := footerAt(src, endOff, limit)
	if err != nil || !ok {
		return nil, false, err
	}
	head, headed, err := headerRecord(src, foot, endOff, limit)
	if err != nil {
		return nil, false, err
	}
	raw, ok, err := readItems(src, foot, endOff, limit)
	if err != nil || !ok {
		return nil, false, err
	}
	return tagFrom(foot, head, endOff, raw, headed, maxElements), true, nil
}

// DeclaredStart reads the footer ending at endOff, and the header record it announces, and returns
// where the tag starts; ok is false when no footer ends there. No item is read.
func DeclaredStart(src core.ReaderAtSized, endOff, limit int64) (int64, bool, error) {
	foot, ok, err := footerAt(src, endOff, limit)
	if err != nil || !ok {
		return 0, false, err
	}
	_, headed, err := headerRecord(src, foot, endOff, limit)
	if err != nil {
		return 0, false, err
	}
	start := endOff - foot.size
	if headed {
		start -= footerLen
	}
	return start, true, nil
}

// ParseHeadAt parses the tag that opens with a header record at off and ends by maxEnd. ok is
// false unless the footer where that header ends the tag answers it, announcing a header and
// declaring the same size; both records are read before any item. Reads are refused over limit
// as in [ParseAt].
func ParseHeadAt(src core.ReaderAtSized, off, maxEnd, limit int64, maxElements int) (*Tag, bool, error) {
	if maxEnd-off < 2*footerLen {
		return nil, false, nil
	}
	head, ok, err := readRecord(src, off, limit)
	if err != nil || !ok || !head.isHeader() || head.size < footerLen || off+footerLen+head.size > maxEnd {
		return nil, false, err
	}
	end := off + footerLen + head.size
	foot, ok, err := footerAt(src, end, limit)
	if err != nil || !ok || !foot.headed() || foot.size != head.size {
		return nil, false, err
	}
	raw, ok, err := readItems(src, foot, end, limit)
	if err != nil || !ok {
		return nil, false, err
	}
	return tagFrom(foot, head, end, raw, true, maxElements), true, nil
}

// headerRecord returns the header record the footer foot, ending at endOff, announces, and whether
// it is there. Has-header moves the audio/tag boundary, so APETAGEX must open the 32 bytes before
// the items before the bit is trusted (a lying flag would overwrite audio and pass --verify).
func headerRecord(src core.ReaderAtSized, foot record, endOff, limit int64) (record, bool, error) {
	itemsStart := endOff - foot.size
	if !foot.headed() || itemsStart < footerLen {
		return record{}, false, nil
	}
	return readRecord(src, itemsStart-footerLen, limit)
}

// readItems reads the item bytes of the tag whose footer foot ends at endOff, as [bits.ReadWithin]
// reads: ok is false when they run past the end of the file, and a failed read or one over limit is
// an error.
func readItems(src core.ReaderAtSized, foot record, endOff, limit int64) ([]byte, bool, error) {
	if foot.size == footerLen {
		return nil, true, nil
	}
	raw, err := bits.ReadWithin(src, endOff-foot.size, foot.size-footerLen, limit)
	return raw, raw != nil, err
}

// record is one 32-byte header or footer record.
type record struct {
	version int
	size    int64 // items plus footer
	count   uint32
	flags   uint32
}

// decodeRecord decodes the record b opens with; ok is false without the preamble.
func decodeRecord(b []byte) (record, bool) {
	if len(b) < footerLen || string(b[:8]) != preamble {
		return record{}, false
	}
	return record{
		version: int(binary.LittleEndian.Uint32(b[8:12])),
		size:    int64(binary.LittleEndian.Uint32(b[12:16])),
		count:   binary.LittleEndian.Uint32(b[16:20]),
		flags:   binary.LittleEndian.Uint32(b[20:24]),
	}, true
}

// encode renders the record; its last 8 bytes are the reserved field, which must be zero.
func (r record) encode() []byte {
	b := make([]byte, footerLen)
	copy(b[0:8], preamble)
	binary.LittleEndian.PutUint32(b[8:12], uint32(r.version))
	binary.LittleEndian.PutUint32(b[12:16], uint32(r.size))
	binary.LittleEndian.PutUint32(b[16:20], r.count)
	binary.LittleEndian.PutUint32(b[20:24], r.flags)
	return b
}

// readRecord reads the record at off, as [bits.ReadWithin] reads: ok is false when it runs past the
// end of the file or has no preamble, and a failed read or one over limit is an error.
func readRecord(src core.ReaderAtSized, off, limit int64) (record, bool, error) {
	b, err := bits.ReadWithin(src, off, footerLen, limit)
	if err != nil {
		return record{}, false, err
	}
	r, ok := decodeRecord(b)
	return r, ok, nil
}

// isHeader reports a record that marks itself the header; headed, a footer that announces one.
func (r record) isHeader() bool { return r.flags&flagIsHeader != 0 }
func (r record) headed() bool   { return r.flags&flagHasHeader != 0 }

// start is where a footer ending at endOff places its tag's start, header record included when
// the flags announce one.
func (r record) start(endOff int64) int64 {
	if r.headed() {
		return endOff - r.size - footerLen
	}
	return endOff - r.size
}

// footerAt reads the footer ending at endOff: a record that does not mark itself the header, with
// a size that fits before endOff.
func footerAt(src core.ReaderAtSized, endOff, limit int64) (record, bool, error) {
	if endOff < footerLen {
		return record{}, false, nil
	}
	foot, ok, err := readRecord(src, endOff-footerLen, limit)
	if err != nil || !ok || foot.isHeader() || foot.size < footerLen || foot.size > endOff {
		return record{}, false, err
	}
	return foot, true, nil
}

// tagFrom builds the tag whose footer foot ends at endOff from its item bytes; headed says the
// header record head opens it.
func tagFrom(foot, head record, endOff int64, raw []byte, headed bool, maxElements int) *Tag {
	t := &Tag{Version: foot.version, Items: []Item{}, Offset: endOff - foot.size, Size: foot.size, HasHeader: headed, Flags: foot.flags}
	if headed {
		t.Flags |= head.flags &^ shapeFlags
	}
	if len(raw) > 0 {
		var consumed int
		t.Items, t.Truncated, consumed = parseItems(raw, foot.count, maxElements)
		t.UnreadTail = len(raw) - consumed
	}
	if headed {
		t.Offset -= footerLen
		t.Size += footerLen
	}
	return t
}

// walkItems walks the count items raw declares, handing each to visit when it is not nil. It
// returns how many bytes the well-formed items it walked take, and whether every declared item was
// well formed and together they took every byte of raw.
func walkItems(raw []byte, count uint32, visit func(key, value []byte, flags uint32)) (consumed int, whole bool) {
	pos := 0
	for range count {
		at := pos
		if len(raw)-at < 8 {
			return pos, false
		}
		size := binary.LittleEndian.Uint32(raw[at : at+4])
		flags := binary.LittleEndian.Uint32(raw[at+4 : at+8])
		at += 8
		n := bytes.IndexByte(raw[at:], 0)
		if n < 0 {
			return pos, false
		}
		key := raw[at : at+n]
		at += n + 1
		// Compared as uint64: a 32-bit int cannot hold every declared size.
		if uint64(size) > uint64(len(raw)-at) {
			return pos, false
		}
		if visit != nil {
			visit(key, raw[at:at+int(size)], flags)
		}
		pos = at + int(size)
	}
	return pos, pos == len(raw)
}

// parseItems decodes the items raw declares up to the first malformed one, and returns how many
// bytes of raw they take. An item past maxElements is walked but not decoded, and truncated
// reports it. The cap is not fatal here (MP3 keeps raw bytes); codecs that rebuild from Items
// refuse [Tag.Truncated].
func parseItems(raw []byte, count uint32, maxElements int) (items []Item, truncated bool, consumed int) {
	consumed, _ = walkItems(raw, count, func(key, value []byte, flags uint32) {
		if maxElements > 0 && len(items) >= maxElements {
			truncated = true
			return
		}
		it := Item{Key: string(key), Flags: flags, Data: value}
		if !it.NonText() {
			it.Value = decodeText(value)
		}
		items = append(items, it)
	})
	return items, truncated, consumed
}

// ParseItemRun decodes a bare item run (Musepack SV8 chapter tag after preamble-less
// header). Same as parseItems; reports element-cap truncation.
func ParseItemRun(raw []byte, count uint32, maxElements int) ([]Item, bool) {
	items, truncated, _ := parseItems(raw, count, maxElements)
	return items, truncated
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

// splitItemValues decodes the NUL-separated values of an item. NULs at the end are terminators, as
// some taggers write one; an empty value ahead of or between others is a value, and an item that is
// empty or holds only NULs is one empty value.
func splitItemValues(value string) []string {
	return strings.Split(strings.TrimRight(value, "\x00"), "\x00")
}
