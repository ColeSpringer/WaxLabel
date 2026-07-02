package aiff

import (
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// textTags projects native text chunks into a canonical TagSet, mapping only the
// known identifiers. Items appear in file order; several ANNO chunks contribute
// several Comment values. [tag.TagSet.AddNativeItem] applies the shared IFF first-wins rule
// (see [infoTags]); AIFF maps no number key today, so in practice every mapped chunk projects
// and a duplicate NAME (Title) is kept as a multi-value, preserved by the forced ID3 chunk.
func textTags(items []textItem) tag.TagSet {
	ts := tag.NewTagSet()
	for _, it := range items {
		key, ok := mapping.AIFFTextKey(it.id4())
		if !ok {
			continue
		}
		// Surface a present-empty (genuinely zero-length) text chunk as a present-empty value,
		// not absent, so --set TITLE= round-trips like the other formats (L1). Every chunk in the
		// list is present; an absent key simply has no chunk.
		ts.AddNativeItem(key, it.text())
	}
	// No number-pair normalization here: AIFF's native text chunks map no numeric key
	// (mapping.aiffTextKeys), so a slashed track/disc number cannot occur. If a numeric
	// mapping is ever added, split it with tag.NormalizeNumberPairs like the WAV/Vorbis paths.
	return ts
}

// textFamilies builds AIFF family/source entries from native text chunks,
// marking an entry unselected (a conflict) when its value disagrees with the
// authoritative value for the same key. A duplicate number/total item reads back unselected
// (textTags is first-wins for those); a duplicate text item stays in auth (both values are
// kept), so both entries read selected. AIFF maps no number key today, so in practice every
// entry is selected.
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

// textRepresentable reports whether every key in ts can be stored faithfully in
// the native text chunks: each must map to a native identifier, and only Comment
// (which writes as repeated ANNO chunks) may carry more than one value. A key
// that fails forces the richer ID3 chunk so no value is lost.
func textRepresentable(ts tag.TagSet) bool {
	for _, k := range ts.Keys() {
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

// rebuildText produces the full native-text-chunk set for an edited tag set: one
// chunk per single-valued key present (NAME/AUTH/"(c) "), and one ANNO chunk per
// Comment value. Existing keys keep their original relative order; keys newly
// present in the edited set are appended in the set's order. A present-empty value is
// emitted as a genuinely zero-length chunk (textTags surfaces it as present-empty), so
// --set TITLE= round-trips through the native chunk like the other formats (L1); only an
// absent key emits no chunk.
func rebuildText(orig []textItem, edited tag.TagSet) []outChunk {
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
		if key, ok := mapping.AIFFTextKey(it.id4()); ok {
			emit(it.id, key)
		}
	}
	for _, k := range edited.Keys() {
		if emitted[k] {
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

// textOut builds one native text output chunk holding the raw value bytes. AIFF
// text chunks are plain character runs; the value is written verbatim (no NUL
// terminator) and word-aligned by assemble.
func textOut(id [4]byte, value string) outChunk {
	return outChunk{id: id, role: roleText, body: []byte(value), bodyLen: int64(len(value))}
}

// nativeReducedWarnings notes each multi-valued key reduced to its first value in
// a single-valued native text chunk (NAME/AUTH/"(c) ") while the full set is kept
// in the ID3 chunk written alongside it. Comment is excluded because it maps to
// repeatable ANNO chunks. core.NativeReducedWarnings applies the value-count and
// first-present checks, including the present-empty case, which is dropped rather
// than reduced. The caller invokes this only when both containers are emitted.
func nativeReducedWarnings(ts tag.TagSet) []core.Warning {
	return core.NativeReducedWarnings(ts, "text chunk", func(k tag.Key) bool {
		_, ok := mapping.AIFFKeyText(k)
		return ok && k != tag.Comment
	})
}
