package aiff

import (
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/mapping"
	"github.com/colespringer/waxlabel/tag"
)

// textTags projects native text chunks into a canonical TagSet, mapping only the
// known identifiers. Items appear in file order; several ANNO chunks contribute
// several Comment values.
func textTags(items []textItem) tag.TagSet {
	ts := tag.NewTagSet()
	for _, it := range items {
		key, ok := mapping.AIFFTextKey(it.id4())
		if !ok {
			continue
		}
		if v := it.text(); v != "" {
			ts.Add(key, v)
		}
	}
	return ts
}

// textFamilies builds AIFF family/source entries from native text chunks,
// marking an entry unselected (a conflict) when its value disagrees with the
// authoritative value for the same key. When the native chunks are themselves
// authoritative, auth is their own projection, so every entry is selected.
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
// present in the edited set are appended in the set's order. Present-but-empty
// values normalize to absent (the chunk is dropped), matching formats that cannot
// store an empty value.
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
				if v != "" {
					out = append(out, textOut(id, v))
				}
			}
			return
		}
		if v, ok := edited.First(key); ok && v != "" {
			out = append(out, textOut(id, v))
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
