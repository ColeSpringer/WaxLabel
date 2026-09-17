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

// parseInfo decodes a LIST chunk body into INFO items.
func parseInfo(body []byte, maxElements int) (items []infoItem, unread int, padRescued bool, err error) {
	if len(body) < 4 || string(body[0:4]) != "INFO" {
		return nil, 0, false, nil
	}
	pos := 4
	fellBack := false
	for pos+8 <= len(body) && plausibleInfoItem(body, pos) {
		var id [4]byte
		copy(id[:], body[pos:pos+4])
		// plausibleInfoItem already rejected a size that is negative (the int(uint32)
		// hazard on a 32-bit platform) or runs past the body, so the slice below is safe.
		size := int(binary.LittleEndian.Uint32(body[pos+4 : pos+8]))
		start := pos + 8
		// ZSTR: the value ends at the first NUL. Cutting there (rather than only
		// trimming trailing NULs) means an interior NUL cannot survive into the
		// canonical string and later truncate an id3 text frame. Clone so the item
		// does not alias the larger body buffer.
		content := body[start : start+size]
		if i := bytes.IndexByte(content, 0); i >= 0 {
			// renderInfo writes the cut value plus one NUL, so whatever the item declared
			// past its terminator has nowhere to go on a rewrite.
			unread += unreadableBytes(content[i+1:])
			content = content[:i]
		}
		// Cap the item count before appending so a hostile LIST full of zero-length
		// items cannot balloon allocation - stopping on an implausible header stays
		// benign, only a genuine cap breach is fatal.
		if err := bits.CheckElementCap(len(items), maxElements, "RIFF INFO items"); err != nil {
			return nil, 0, false, err
		}
		items = append(items, infoItem{id: id, raw: slices.Clone(content)})
		if fellBack {
			padRescued = true // the resynchronization above recovered this item
			fellBack = false
		}
		pos = start + size
		if size&1 == 1 {
			// The spec puts a word-alignment pad byte after an odd-size item.
			if pos < len(body) && (body[pos] == 0 || plausibleInfoItem(body, pos+1)) {
				pos++
			} else {
				fellBack = true
			}
		}
	}
	return items, unread + unreadableBytes(body[pos:]), padRescued, nil
}

// unreadableBytes reports how many of b's bytes a rewrite would destroy. Anything else
// is content the item model cannot carry.
func unreadableBytes(b []byte) int {
	for _, c := range b {
		if c != 0 {
			return len(b)
		}
	}
	return 0
}

// plausibleInfoItem reports whether p could begin another INFO item, or is the clean
// end of the list.
func plausibleInfoItem(body []byte, p int) bool {
	if p == len(body) {
		return true
	}
	if p < 0 || p+8 > len(body) {
		return false
	}
	for _, c := range body[p : p+4] {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	// int(uint32) can be negative on a 32-bit platform, the same hazard the item loop
	// guards; a negative size is not a plausible item.
	size := int(binary.LittleEndian.Uint32(body[p+4 : p+8]))
	return size >= 0 && size <= len(body)-(p+8)
}

// infoTags projects INFO items into a canonical TagSet, mapping only the known
// identifiers.
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
	tag.NormalizeNumberPairs(&ts)
	return ts
}

// infoFamilies builds RIFF family/source entries from INFO items, marking an entry
// unselected (a conflict) when its value disagrees with the authoritative value for the
// same key.
func infoFamilies(auth tag.TagSet, items []infoItem) []core.FamilyValue {
	var out []core.FamilyValue
	// One selector for the whole list: a LIST at the element cap whose items all map to one
	// key would make a per-item scan of auth quadratic.
	selected := core.FamilySelector(auth)
	add := func(key tag.Key, v string) {
		out = append(out, core.FamilyValue{
			Key: key, Family: core.FamilyRIFF, Scope: core.ScopeTrack,
			Values: []string{v}, Selected: selected(key, v),
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
		// surface a spurious conflicting-families finding.
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
// present-but-empty value is representable - stored as a size-1 NUL INFO item, see
// infoValue).
func infoRepresentable(ts tag.TagSet, changed map[tag.Key]bool) bool {
	for _, k := range ts.Keys() {
		if !changed[k] {
			continue
		}
		if _, ok := mapping.RIFFKeyInfo(k); !ok {
			if k == tag.TrackTotal {
				if _, ok := infoTrackPair(ts); ok {
					continue
				}
			}
			return false
		}
		if vs, _ := ts.Get(k); len(vs) > 1 {
			return false
		}
	}
	return true
}

// rebuildInfo re-renders only the items whose canonical key this edit changed and
// copies every other item verbatim, so a duplicate, a second identifier for the same
// key, and a value the id3 chunk disagrees with all survive an unrelated edit.
func rebuildInfo(orig []infoItem, edited tag.TagSet, changed map[tag.Key]bool, stripStamp bool) []infoItem {
	out := make([]infoItem, 0, len(orig))
	emittedID := map[[4]byte]bool{}
	emittedKey := map[tag.Key]bool{}
	for _, it := range orig {
		key, ok := mapping.RIFFInfoKey(it.id4())
		if !ok {
			out = append(out, it) // unmapped: preserve the raw bytes verbatim
			continue
		}
		if stripStamp && isTranscoderISFT(it) {
			emittedKey[key] = true
			continue // the stamp is what the strip targets; do not re-render it
		}
		if !changed[key] {
			out = append(out, it) // untouched: the file's own bytes, whatever the projection holds
			continue
		}
		if emittedID[it.id] {
			continue // this identifier already took the edited value
		}
		if v, ok := infoValue(edited, key); ok {
			out = append(out, infoItem{id: it.id, raw: []byte(v)})
			emittedID[it.id] = true
			emittedKey[key] = true
		}
		// else: key absent in the edited set - drop the item.
	}
	for _, k := range edited.Keys() {
		// The changed gate keeps a no-op a no-op: without it, a WAV whose INFO does not
		// mirror its id3 chunk would gain an item on every write.
		if !changed[k] || emittedKey[k] {
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
			emittedKey[k] = true
		}
	}
	return out
}

// equalInfoItems reports whether two item lists carry the same identifiers and bytes in
// the same order.
func equalInfoItems(a, b []infoItem) bool {
	return slices.EqualFunc(a, b, func(x, y infoItem) bool { return x.id == y.id && bytes.Equal(x.raw, y.raw) })
}

// infoConflictKeys lists the keys this write re-renders whose RIFF family entry
// disagreed with the projection: the conflicting items the write replaces.
func infoConflictKeys(fams []core.FamilyValue, changed map[tag.Key]bool) []tag.Key {
	var out []tag.Key
	seen := map[tag.Key]bool{}
	for _, f := range fams {
		if f.Family == core.FamilyRIFF && !f.Selected && changed[f.Key] && !seen[f.Key] {
			seen[f.Key] = true
			out = append(out, f.Key)
		}
	}
	return out
}

// infoValue returns the value INFO should store for key - the first value, since INFO
// is single-valued - or ok=false only when the key is absent. This lets a present-empty
// value round-trip through INFO like the other formats, rather than being dropped and
// relying on a forced ID3 chunk.
func infoValue(ts tag.TagSet, key tag.Key) (string, bool) {
	v, ok := ts.First(key)
	if !ok {
		return v, ok
	}
	if key == tag.TrackNumber {
		if joined, ok := infoTrackPair(ts); ok {
			return joined, true
		}
	}
	return v, true
}

// infoTrackPair returns the single IPRT value that stores both TrackNumber and
// TrackTotal, and whether one exists.
func infoTrackPair(ts tag.TagSet) (string, bool) {
	num, ok := ts.First(tag.TrackNumber)
	if !ok {
		return "", false
	}
	total, ok := ts.First(tag.TrackTotal)
	if !ok || total == "" {
		return "", false
	}
	joined := num + "/" + total
	if n, t, split := tag.NumberTotalSplit(tag.TrackNumber, joined); split && n == num && t == total {
		return joined, true
	}
	return "", false
}

// foldInfoTrackPair adds each half of the track number/total pair to a change set that
// names the other.
func foldInfoTrackPair(changed map[tag.Key]bool) map[tag.Key]bool {
	if changed[tag.TrackNumber] || changed[tag.TrackTotal] {
		changed[tag.TrackNumber] = true
		changed[tag.TrackTotal] = true
	}
	return changed
}

// nativeReducedWarnings notes each multi-valued key reduced to its first value in the
// single-valued LIST/INFO chunk while the full set is kept in the ID3 chunk written
// alongside it. an unchanged key keeps its own items verbatim and loses nothing.
func nativeReducedWarnings(ts tag.TagSet, changed map[tag.Key]bool) []core.Warning {
	return core.NativeReducedWarnings(ts, "LIST/INFO", func(k tag.Key) bool {
		_, ok := mapping.RIFFKeyInfo(k)
		return ok && changed[k]
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

// unmappedInfoIDs lists, in file order and without repeats, the INFO identifiers that
// project to no canonical key (ILNG, ISBJ, IKEY, ...). Everywhere else those items are
// preserved verbatim;
func unmappedInfoIDs(items []infoItem) []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		id := it.id4()
		if _, mapped := mapping.RIFFInfoKey(id); mapped || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// isTranscoderISFT reports whether it is an ISFT software item carrying an inherited
// transcoder stamp ("Lavf..." from ffmpeg).
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
			ws = core.Warn(ws, core.WarnInheritedEncoder, "inherited encoder stamp: "+core.WarnSnippet(it.text()))
		}
	}
	return ws
}
