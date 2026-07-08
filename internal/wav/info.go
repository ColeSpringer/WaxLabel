package wav

import (
	"bytes"
	"encoding/binary"
	"slices"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// parseInfo decodes a LIST chunk body into INFO items. The body begins with the
// 4-byte list type; only "INFO" lists are decoded (the sole caller pre-confirms
// the INFO type, so a non-INFO body yields no items). It tolerates
// truncation, stopping at the first malformed sub-chunk and returning the items
// gathered so far with a nil error. maxElements caps the item count via
// bits.CheckElementCap - the one hard error this returns - so a crafted multi-MB
// LIST cannot amplify allocation past the WithLimits knob, matching every sibling
// parser.
func parseInfo(body []byte, maxElements int) (items []infoItem, err error) {
	if len(body) < 4 || string(body[0:4]) != "INFO" {
		return nil, nil
	}
	pos := 4
	for pos+8 <= len(body) {
		var id [4]byte
		copy(id[:], body[pos:pos+4])
		size := int(binary.LittleEndian.Uint32(body[pos+4 : pos+8]))
		start := pos + 8
		// Phrase the bound as a subtraction so a hostile size cannot overflow the
		// addition start+size into a negative int on a 32-bit platform (which would
		// slip past the guard and panic on the slice below). start <= len(body)
		// holds from the loop condition, so len(body)-start is non-negative.
		if size < 0 || size > len(body)-start {
			break // truncated item; stop rather than over-read (tolerated, nil error)
		}
		// ZSTR: the value ends at the first NUL. Cutting there (rather than only
		// trimming trailing NULs) means an interior NUL cannot survive into the
		// canonical string and later truncate an id3 text frame. Clone so the item
		// does not alias the larger body buffer.
		content := body[start : start+size]
		if i := bytes.IndexByte(content, 0); i >= 0 {
			content = content[:i]
		}
		// Cap the item count before appending so a hostile LIST full of zero-length
		// items cannot balloon allocation - the truncation break above stays benign,
		// only a genuine cap breach is fatal.
		if err := bits.CheckElementCap(len(items), maxElements, "RIFF INFO items"); err != nil {
			return nil, err
		}
		items = append(items, infoItem{id: id, raw: slices.Clone(content)})
		pos = start + size
		if size&1 == 1 {
			pos++ // word-alignment pad byte
		}
	}
	return items, nil
}

// infoTags projects INFO items into a canonical TagSet, mapping only the known
// identifiers. Items appear in file order. [tag.TagSet.AddNativeItem] applies the shared IFF
// first-wins rule: a duplicate number key (two IPRT, both TrackNumber) keeps the first, since a
// phantom multi-value TRACKNUMBER no writer can store would diff as a spurious change and trip
// a false native-value-reduced warning; a duplicate text key (two INAM) accumulates, because
// the ID3 chunk the writer forces preserves both.
func infoTags(items []infoItem) tag.TagSet {
	ts := tag.NewTagSet()
	for _, it := range items {
		key, ok := mapping.RIFFInfoKey(it.id4())
		if !ok {
			continue
		}
		// Surface a present-empty INFO item (a size-1 NUL, text() == "") as a present-empty
		// value, not absent, so --set TITLE= round-trips like the other formats. Every item
		// in the list is present; an absent key simply has no item.
		ts.AddNativeItem(key, it.text())
	}
	// IPRT/ITRK map to TrackNumber, so a non-standard IPRT="4/9" would otherwise read
	// verbatim while ID3/MP4 split it - normalize here so every read path agrees.
	// INFO has no TrackTotal slot (mapping/riff.go), so a subsequent edit spills the
	// derived total into the forced id3 chunk; a plain read->write stays byte-identical.
	tag.NormalizeNumberPairs(&ts)
	return ts
}

// infoFamilies builds RIFF family/source entries from INFO items, marking an
// entry unselected (a conflict) when its value disagrees with the authoritative
// value for the same key. When INFO is itself authoritative, auth holds only the first
// value of each number/total key (infoTags is first-wins for those), so a duplicate item for
// such a key reads back unselected - exposing the conflict without polluting the canonical
// set. A duplicate text item is kept in auth (both selected), since both values survive.
func infoFamilies(auth tag.TagSet, items []infoItem) []core.FamilyValue {
	var out []core.FamilyValue
	add := func(key tag.Key, v string) {
		out = append(out, core.FamilyValue{
			Key: key, Family: core.FamilyRIFF, Scope: core.ScopeTrack,
			Values: []string{v}, Selected: core.FamilySelected(auth, key, v),
		})
	}
	for _, it := range items {
		key, ok := mapping.RIFFInfoKey(it.id4())
		if !ok {
			continue
		}
		v := it.text()
		if v == "" {
			continue
		}
		// Split a slashed track/disc number the same way infoTags does, so the family value
		// matches the (normalized) authoritative tag instead of being falsely graded a
		// conflict - a raw "4/9" compared against TrackNumber=4 would read unselected and
		// surface a spurious conflicting-families finding. This mirrors the ID3/Matroska read
		// paths, which already contribute the split number and total to their family views.
		if num, total, split := tag.NumberTotalSplit(key, v); split {
			if num != "" {
				add(key, num)
			}
			if total != "" {
				add(tag.TotalKey(key), total)
			}
			continue
		}
		add(key, v)
	}
	return out
}

// infoRepresentable reports whether every key in ts can be stored faithfully in
// LIST/INFO: each must map to an INFO identifier and carry at most one value (a
// present-but-empty value is representable - stored as a size-1 NUL INFO item, see infoValue).
// A key that fails forces the richer id3 chunk so no value is lost.
func infoRepresentable(ts tag.TagSet) bool {
	for _, k := range ts.Keys() {
		if _, ok := mapping.RIFFKeyInfo(k); !ok {
			return false
		}
		if vs, _ := ts.Get(k); len(vs) > 1 {
			return false
		}
	}
	return true
}

// rebuildInfo produces the INFO item list for an edited tag set. Unmapped items
// (IENG, ILNG, the ISFT encoder stamp, ...) are preserved verbatim in place;
// mapped items are re-rendered from the edited set or dropped when their key is
// now absent; keys newly present in the edited set are appended in the set's
// order. Multi-value mapped keys (which also forced an id3 chunk) store their
// first value here, INFO being single-valued. When stripStamp is set, a
// transcoder-stamp ISFT item (the WAV encoder leftover) is dropped rather than
// preserved; it is the one removable native stamp a canonical ENCODER edit cannot
// reach. An emptied list then drops the LIST chunk via the caller's len check.
func rebuildInfo(orig []infoItem, edited tag.TagSet, stripStamp bool) []infoItem {
	out := make([]infoItem, 0, len(orig))
	emitted := map[tag.Key]bool{}
	for _, it := range orig {
		key, ok := mapping.RIFFInfoKey(it.id4())
		if !ok {
			if stripStamp && isTranscoderISFT(it) {
				continue // drop the inherited encoder stamp instead of preserving it
			}
			out = append(out, it) // unmapped: preserve the raw bytes verbatim
			continue
		}
		if emitted[key] {
			continue // a non-conformant file with duplicate mapped items: keep one
		}
		if v, ok := infoValue(edited, key); ok {
			out = append(out, infoItem{id: it.id, raw: []byte(v)})
			emitted[key] = true
		}
		// else: key absent in the edited set - drop the item.
	}
	for _, k := range edited.Keys() {
		if emitted[k] {
			continue
		}
		id, ok := mapping.RIFFKeyInfo(k)
		if !ok {
			continue
		}
		if v, ok := infoValue(edited, k); ok {
			var id4 [4]byte
			copy(id4[:], id)
			out = append(out, infoItem{id: id4, raw: []byte(v)})
			emitted[k] = true
		}
	}
	return out
}

// infoValue returns the value INFO should store for key - the first value, since INFO is
// single-valued - or ok=false only when the key is absent. A present value, including a
// present-empty one (--set TITLE=), is stored: INFO items are ZSTR (NUL-terminated), so an
// empty value is a size-1 NUL item (renderInfo writes len(raw)+1 bytes), distinct from an
// absent key (no item at all). This lets a present-empty value round-trip through INFO like the
// other formats, rather than being dropped and relying on a forced ID3 chunk.
func infoValue(ts tag.TagSet, key tag.Key) (string, bool) {
	return ts.First(key)
}

// nativeReducedWarnings notes each multi-valued key reduced to its first value in
// the single-valued LIST/INFO chunk while the full set is kept in the ID3 chunk
// written alongside it. Every RIFF INFO slot is single-valued, so any mapped key
// qualifies. core.NativeReducedWarnings applies the value-count and first-present
// checks, matching infoValue's treatment of present-empty values. The caller
// invokes this only when both containers are emitted.
func nativeReducedWarnings(ts tag.TagSet) []core.Warning {
	return core.NativeReducedWarnings(ts, "LIST/INFO", func(k tag.Key) bool {
		_, ok := mapping.RIFFKeyInfo(k)
		return ok
	})
}

// renderInfo serializes INFO items into a LIST chunk body: the "INFO" list type
// followed by each item as 4CC + little-endian size + NUL-terminated value, word
// aligned. The returned bytes are the chunk body (the caller prepends the "LIST"
// header).
func renderInfo(items []infoItem) []byte {
	out := []byte("INFO")
	for _, it := range items {
		val := make([]byte, len(it.raw)+1) // raw value bytes + NUL terminator
		copy(val, it.raw)
		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], uint32(len(val)))
		out = append(out, it.id[:]...)
		out = append(out, sz[:]...)
		out = append(out, val...)
		if len(val)&1 == 1 {
			out = append(out, 0) // word-alignment pad (not counted in the size)
		}
	}
	return out
}

// isTranscoderISFT reports whether it is an ISFT software item carrying an
// inherited transcoder stamp ("Lavf..." from ffmpeg). It is the single predicate
// shared by encoderNoise (which warns about it) and rebuildInfo (which drops it
// under WithStripEncoderStamp), so the stamp the warning flags is exactly the one
// the strip removes.
func isTranscoderISFT(it infoItem) bool {
	return it.id4() == "ISFT" && core.IsTranscoderStamp(it.text())
}

// hasTranscoderISFT reports whether items contains a strippable transcoder-stamp
// ISFT. The WAV Plan uses it to know a strip would change the file, so a
// WithStripEncoderStamp edit of an otherwise-unchanged file is not a no-op.
func hasTranscoderISFT(items []infoItem) bool {
	for _, it := range items {
		if isTranscoderISFT(it) {
			return true
		}
	}
	return false
}

// encoderNoise flags an inherited transcoder stamp: the ISFT software item
// ("Lavf..." from ffmpeg) is the WAV analogue of an "encoder=" comment.
func encoderNoise(items []infoItem) []core.Warning {
	var ws []core.Warning
	for _, it := range items {
		if isTranscoderISFT(it) {
			ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder stamp: "+it.text())
		}
	}
	return ws
}
