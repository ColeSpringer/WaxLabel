// Package ape implements read-only parsing of APEv1/APEv2 tags for internal
// codecs. An APE tag is a foreign/legacy container in the formats WaxLabel
// writes (it shows up trailing some MP3s): WaxLabel surfaces its values in the
// family/source view and preserves its bytes verbatim, but the native tag stays
// authoritative. It is reimplemented from the public APE tag specification.
package ape

import (
	"encoding/binary"
	"strings"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
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
}

// Item is one APE key/value pair. Text values may carry NUL-separated multiple
// values; NonText marks binary, external/locator, and reserved items - anything
// that is not a UTF-8 text value - which are preserved but not projected.
type Item struct {
	Key     string
	Value   string
	NonText bool
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
	if itemsLen > 0 {
		raw, err := bits.ReadSlice(src, itemsStart, itemsLen, limit)
		if err != nil {
			return nil, false, nil //nolint:nilerr
		}
		items = parseItems(raw, itemCount, maxElements)
	}

	offset := itemsStart
	size := tagSize
	if flags&flagHasHeader != 0 {
		offset -= footerLen
		size += footerLen
	}
	if offset < 0 {
		return nil, false, nil // a header flag with too small a tagSize; malformed
	}
	return &Tag{Version: version, Items: items, Offset: offset, Size: size}, true, nil
}

// parseItems decodes up to count items from the item region. It stops on
// malformed input and caps the decoded list at maxElements. MP3 writes preserve
// the raw APE region separately, so truncating this decoded view does not drop
// bytes on write.
func parseItems(raw []byte, count uint32, maxElements int) []Item {
	var items []Item
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
			break // cap reached: stop decoding; the raw region is preserved elsewhere
		}
		items = append(items, Item{
			Key:   key,
			Value: string(value),
			// Item-type bits (1-2): 0 == UTF-8 text. Everything else (binary,
			// external/locator, reserved) is non-text and not projected.
			NonText: (flags>>1)&3 != 0,
		})
	}
	return items
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

// apeKeys folds common APE item names onto canonical keys. Names not listed pass
// through as the uppercased key. Matching is case-insensitive.
var apeKeys = map[string]tag.Key{
	"title":        tag.Title,
	"artist":       tag.Artist,
	"album":        tag.Album,
	"album artist": tag.AlbumArtist,
	"composer":     tag.Composer,
	"lyricist":     tag.Lyricist,
	"genre":        tag.Genre,
	"track":        tag.TrackNumber,
	"disc":         tag.DiscNumber,
	"year":         tag.RecordingDate,
	"comment":      tag.Comment,
	"lyrics":       tag.Lyrics,
	"isrc":         tag.ISRC,
	"catalog":      tag.CatalogNumber,
	"label":        tag.Label,
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
		if it.NonText {
			continue
		}
		key, ok := apeKeys[strings.ToLower(it.Key)]
		if !ok {
			k, err := tag.ParseKey(it.Key)
			if err != nil {
				continue
			}
			key = k
		}
		for v := range strings.SplitSeq(it.Value, "\x00") {
			if v != "" {
				out = append(out, kv{key, v})
			}
		}
	}
	return out
}
