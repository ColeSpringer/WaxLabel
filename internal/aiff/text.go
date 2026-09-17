package aiff

import (
	"bytes"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// textTags projects native text chunks into a canonical TagSet, mapping only the known
// identifiers.
func textTags(items []textItem) tag.TagSet {
	ts := tag.NewTagSet()
	for _, it := range items {
		key, ok := mapping.AIFFTextKey(it.id4())
		if !ok {
			continue
		}
		// Surface a present-empty (genuinely zero-length) text chunk as a present-empty value,
		// not absent, so --set TITLE= round-trips like the other formats. Every chunk in the
		// list is present; an absent key simply has no chunk.
		ts.AddNativeItem(key, it.text())
	}
	// No number-pair normalization here: AIFF's native text chunks map no numeric key
	// (mapping.aiffTextKeys), so a slashed track/disc number cannot occur. If a numeric
	// mapping is ever added, split it with tag.NormalizeNumberPairs like the WAV/Vorbis paths.
	return ts
}

// textFamilies builds AIFF family/source entries from native text chunks, marking an
// entry unselected (a conflict) when its value disagrees with the authoritative value
// for the same key. A duplicate number/total item reads back unselected (textTags is
// first-wins for those);
func textFamilies(auth tag.TagSet, items []textItem) []core.FamilyValue {
	var out []core.FamilyValue
	for _, it := range items {
		key, ok := mapping.AIFFTextKey(it.id4())
		if !ok {
			continue
		}
		v := it.text()
		if v == "" {
			continue
		}
		out = append(out, core.FamilyValue{
			Key: key, Family: core.FamilyAIFF, Scope: core.ScopeTrack,
			Values: []string{v}, Selected: core.FamilySelected(auth, key, v),
		})
	}
	return out
}

// textRepresentable reports whether every key in ts can be stored faithfully in the
// native text chunks: each must map to a native identifier, and only Comment (which
// writes as repeated ANNO chunks) may carry more than one value.
func textRepresentable(ts tag.TagSet, changed map[tag.Key]bool) bool {
	for _, k := range ts.Keys() {
		if !changed[k] {
			continue
		}
		if _, ok := mapping.AIFFKeyText(k); !ok {
			return false
		}
		if k != tag.Comment {
			if vs, _ := ts.Get(k); len(vs) > 1 {
				return false
			}
		}
	}
	return true
}

// rebuildText re-renders only the chunks whose canonical key this edit changed and
// copies every other chunk verbatim, so a duplicate NAME and a value the ID3 chunk
// disagrees with both survive an unrelated edit.
func rebuildText(orig []textItem, edited tag.TagSet, changed map[tag.Key]bool) []outChunk {
	var out []outChunk
	emitted := map[tag.Key]bool{}

	emit := func(id [4]byte, key tag.Key) {
		if emitted[key] {
			return
		}
		emitted[key] = true
		if key == tag.Comment {
			vals, _ := edited.Get(key)
			for _, v := range vals {
				out = append(out, textOut(id, v)) // emit each value, including a present-empty (zero-length) ANNO
			}
			return
		}
		if v, ok := edited.First(key); ok {
			out = append(out, textOut(id, v)) // present (even empty) -> a chunk; a present-empty is zero-length
		}
	}

	for _, it := range orig {
		key, ok := mapping.AIFFTextKey(it.id4())
		if !ok {
			// Unreachable: parse collects a chunk into texts only when it maps. An unmapped
			// chunk is an ordinary chunk, preserved verbatim by planChunks, which is why this
			// drops rather than carrying raw bytes the way WAV's open INFO vocabulary must.
			continue
		}
		if !changed[key] {
			// Untouched: the file's own bytes, whatever the projection holds.
			out = append(out, outChunk{id: it.id, role: roleText, body: it.raw, bodyLen: int64(len(it.raw))})
			continue
		}
		emit(it.id, key)
	}
	for _, k := range edited.Keys() {
		// The changed gate keeps a no-op a no-op: without it, an AIFF whose text chunks do
		// not mirror its ID3 chunk would gain a chunk on every write.
		if !changed[k] || emitted[k] {
			continue
		}
		if id, ok := mapping.AIFFKeyText(k); ok {
			var id4 [4]byte
			copy(id4[:], id)
			emit(id4, k)
		}
	}
	return out
}

// equalTextChunks reports whether the rebuilt chunks carry the same identifiers and bytes,
// in the same order, as the ones the file already holds.
func equalTextChunks(out []outChunk, orig []textItem) bool {
	return slices.EqualFunc(out, orig, func(x outChunk, y textItem) bool {
		return x.id == y.id && bytes.Equal(x.body, y.raw)
	})
}

// textBytesChange reports whether re-emitting the native text chunks will change the
// bytes they occupy.
func textBytesChange(d *doc, newText []outChunk) bool {
	if !equalTextChunks(newText, d.texts) {
		return true
	}
	for j, idx := range d.textIdx {
		if d.chunks[idx].bodyLen != int64(len(d.texts[j].raw)) {
			return true // bytes past an interior NUL have nowhere to go
		}
		if j > 0 && idx != d.textIdx[j-1]+1 {
			return true // the group is not contiguous, so regrouping moves chunks
		}
	}
	return false
}

// textConflictKeys lists the keys this write re-renders whose AIFF family entry disagreed
// with the projection: the conflicting chunks the write replaces. It is keyed on the same
// change set rebuildText is, so the report cannot claim more or less than the rewrite does.
func textConflictKeys(fams []core.FamilyValue, changed map[tag.Key]bool) []tag.Key {
	var out []tag.Key
	seen := map[tag.Key]bool{}
	for _, f := range fams {
		if f.Family == core.FamilyAIFF && !f.Selected && changed[f.Key] && !seen[f.Key] {
			seen[f.Key] = true
			out = append(out, f.Key)
		}
	}
	return out
}

// strippedTextKeys lists the canonical keys whose native text chunk holds a value that
// is going nowhere: the projection did not select it (the ID3 chunk disagreed, or it
// duplicates a value the canonical set does not carry), and this edit did not write it
// either, so no frame in the ID3 chunk will hold it.
func strippedTextKeys(fams []core.FamilyValue, edited tag.TagSet) []tag.Key {
	var out []tag.Key
	seen := map[tag.Key]bool{}
	kept := core.FamilySelector(edited)
	for _, f := range fams {
		if f.Family != core.FamilyAIFF || f.Selected || seen[f.Key] || len(f.Values) == 0 {
			continue
		}
		if kept(f.Key, f.Values[0]) {
			continue // this edit wrote the value the chunk held; the ID3 chunk takes it
		}
		seen[f.Key] = true
		out = append(out, f.Key)
	}
	return out
}

// textOut builds one native text output chunk holding the raw value bytes. AIFF
// text chunks are plain character runs; the value is written verbatim (no NUL
// terminator) and word-aligned by assemble.
func textOut(id [4]byte, value string) outChunk {
	return outChunk{id: id, role: roleText, body: []byte(value), bodyLen: int64(len(value))}
}

// nativeReducedWarnings notes each multi-valued key reduced to its first value in a
// single-valued native text chunk (NAME/AUTH/"(c) ") while the full set is kept in the
// ID3 chunk written alongside it.
func nativeReducedWarnings(ts tag.TagSet, changed map[tag.Key]bool) []core.Warning {
	return core.NativeReducedWarnings(ts, "text chunk", func(k tag.Key) bool {
		_, ok := mapping.AIFFKeyText(k)
		return ok && k != tag.Comment && changed[k]
	})
}
