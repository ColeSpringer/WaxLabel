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
//
// unread is how many bytes of body the item model does not carry, so the caller can report
// what a rewrite destroys rather than losing it silently: the region past the last item,
// plus anything an item declared after its ZSTR terminator, which renderInfo will not write
// back. padRescued reports that re-synchronizing over a missing word-alignment pad byte
// actually recovered an item; a list whose only unpadded item is its last parses whole
// either way, so it stays quiet.
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
			// The spec puts a word-alignment pad byte after an odd-size item. Step over it
			// when the file actually has one - the byte is the zero a writer pads with, or
			// something readable follows it - and stay put when it is missing, which is a
			// writer bug that desynchronizes every item after this one. Staying both
			// re-synchronizes the walk and keeps the unread count off a byte that is
			// really there; stepping keeps a genuine pad byte out of it.
			if pos < len(body) && (body[pos] == 0 || plausibleInfoItem(body, pos+1)) {
				pos++
			} else {
				fellBack = true
			}
		}
	}
	return items, unread + unreadableBytes(body[pos:]), padRescued, nil
}

// unreadableBytes reports how many of b's bytes a rewrite would destroy. An all-zero run is
// alignment padding a writer added and renderInfo re-adds, so it costs nothing and is not
// reported - without this every LIST whose writer rounded its size up to four would draw a
// warning and fail --strict. Anything else is content the item model cannot carry.
func unreadableBytes(b []byte) int {
	for _, c := range b {
		if c != 0 {
			return len(b)
		}
	}
	return 0
}

// plausibleInfoItem reports whether p could begin another INFO item, or is the clean end
// of the list. An item is a printable-ASCII 4CC - every real INFO identifier is one -
// whose declared size fits the rest of the body. It is both the walk's gate, so a run of
// alignment zeros is not read as an item whose 4CC no reader can print, and the tie-break
// between the two candidate positions after an odd-size item: the spec-correct padded one
// and the unpadded one a writer that omitted the pad byte leaves behind. p can be
// len(body)+1 (the padded candidate after a final unpadded odd item), which is neither an
// item nor a clean end.
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
	// verbatim while ID3/MP4 split it - normalize here so every read path agrees. The
	// write side recombines the pair into one IPRT (infoValue), so the split costs the
	// file neither an id3 chunk nor a byte on round-trip.
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
//
// TrackTotal is the one key with no identifier of its own that can still be representable:
// infoTags splits a slashed IPRT into number and total on read, so infoValue recombines
// them into the one item they came from. Without this a plain IPRT="4/9" file could not
// be rewritten without spawning an id3 chunk to hold the half it had just invented.
// DiscNumber and DiscTotal stay unrepresentable - RIFF INFO has no disc identifier at
// all, so there is no item for them to ride on.
func infoRepresentable(ts tag.TagSet) bool {
	for _, k := range ts.Keys() {
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

// rebuildInfo produces the INFO item list for an edited tag set. Unmapped items
// (IENG, ILNG, ISBJ, ...) are preserved verbatim in place; mapped items are
// re-rendered from the edited set or dropped when their key is now absent; keys
// newly present in the edited set are appended in the set's order. Multi-value
// mapped keys (which also forced an id3 chunk) store their first value here, INFO
// being single-valued. An emptied list then drops the LIST chunk via the caller's
// len check.
//
// stripStamp drops a transcoder-stamp ISFT item. The test is on the ITEM, not on the
// canonical ENCODER value: with an id3 chunk present that value is the chunk's TSSE, so
// judging by it would delete a clean user ISFT on the strength of a stamp in a different
// container, and would leave a stamped ISFT in place when the TSSE is clean. Marking the
// key emitted is what stops the append loop writing the stamp straight back from the same
// value. The caller decides when the flag applies (see Plan).
func rebuildInfo(orig []infoItem, edited tag.TagSet, stripStamp bool) []infoItem {
	out := make([]infoItem, 0, len(orig))
	emitted := map[tag.Key]bool{}
	for _, it := range orig {
		key, ok := mapping.RIFFInfoKey(it.id4())
		if !ok {
			out = append(out, it) // unmapped: preserve the raw bytes verbatim
			continue
		}
		if stripStamp && isTranscoderISFT(it) {
			emitted[key] = true
			continue // the stamp is what the strip targets; do not re-render it
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
//
// TrackNumber recombines with TrackTotal into the "4/9" IPRT reads, so the split infoTags
// performs is undone by the write and a read-write round trip is byte-stable. TrackTotal
// itself is never asked for: it has no identifier, and rebuildInfo only queries keys that
// map to one.
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

// infoTrackPair returns the single IPRT value that stores both TrackNumber and TrackTotal,
// and whether one exists. It is the shared decision behind infoRepresentable (may the total
// ride on the number's item?) and infoValue (what does that item hold?), so the writer
// cannot claim a pair is storable and then store something else.
//
// The join must survive the read that will undo it, which is why it is checked against
// [tag.NumberTotalSplit] - the very rule infoTags applies - rather than assumed. A
// non-numeric number ("A1") composes to "A1/9", which reads back as one literal value with
// the total merged in and lost; the pair is unrepresentable there, so the total forces the
// id3 chunk instead, and ID3's own writer refuses the same composition for the same reason.
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

// unmappedInfoIDs lists, in file order and without repeats, the INFO identifiers that project
// to no canonical key (IENG, ISBJ, IKEY, ...). Everywhere else those items are preserved
// verbatim; a LegacyStrip drops the whole LIST chunk, which is the one path that destroys
// them, and there is no id3 frame for them to move into because they have no canonical key.
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

// isTranscoderISFT reports whether it is an ISFT software item carrying an
// inherited transcoder stamp ("Lavf..." from ffmpeg). It is the single predicate shared by
// encoderNoise (which warns about it), hasTranscoderISFT (which tells Plan a strip would
// change the file), and rebuildInfo (which drops it under WithStripEncoderStamp), so the
// stamp the warning flags is exactly the one the strip removes - and a user's own ISFT,
// which is not a stamp, is left alone by the option.
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
