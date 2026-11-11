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
		// A zero-length text chunk projects as a present-empty value, not absent, so
		// --set TITLE= round-trips like the other formats.
		ts.AddNativeItem(key, it.text())
	}
	// No number-pair normalization: AIFF's native text chunks map no numeric key
	// (mapping.aiffTextKeys). A future numeric mapping needs tag.NormalizeNumberPairs.
	return ts
}

// textFamilies builds AIFF family/source entries from native text chunks, marking an
// entry unselected (a conflict) when its value disagrees with the authoritative value
// for the same key.
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

// textRepresentable reports whether every changed key in ts can be stored in the
// native text chunks: each must map to a native identifier, and only Comment (written
// as repeated ANNO chunks) may carry more than one value.
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

// textPlan is what a rewrite does with the native text chunks: the fate of each one the file
// holds, in file order, and the chunks created for keys it held none for.
type textPlan struct {
	at      []textDecision
	created []textItem
}

// textDecision is one chunk's fate: copied whole when keep is set, and followed, or else replaced,
// by out. Neither drops it.
type textDecision struct {
	keep bool
	out  []textItem
}

// rebuildText re-renders only the chunks whose canonical key this edit changed and keeps every
// other chunk whole where it stands, so a duplicate NAME and a value the ID3 chunk disagrees
// with both survive an unrelated edit. A changed key's chunks take its edited values in order:
// one already holding its value is kept too, values past the chunks follow the last of them,
// and chunks past the values go.
func rebuildText(orig []textItem, edited tag.TagSet, changed map[tag.Key]bool) textPlan {
	p := textPlan{at: make([]textDecision, len(orig))}
	// A present-empty value is a zero-length chunk. Only ANNO repeats.
	values := func(key tag.Key) []string {
		vals, _ := edited.Get(key)
		if key != tag.Comment && len(vals) > 1 {
			vals = vals[:1]
		}
		return vals
	}
	taken := map[tag.Key]int{} // how many of a changed key's values its chunks hold so far
	last := map[tag.Key]int{}  // the last chunk holding one

	for i, it := range orig {
		// parse collects a chunk into texts only when it maps.
		key, _ := mapping.AIFFTextKey(it.id4())
		if !changed[key] {
			p.at[i].keep = true
			continue
		}
		vals, n := values(key), taken[key]
		taken[key] = n + 1
		if n >= len(vals) {
			continue
		}
		last[key] = i
		if v := []byte(vals[n]); bytes.Equal(v, it.raw) {
			p.at[i].keep = true
		} else {
			p.at[i].out = []textItem{{id: it.id, raw: v}}
		}
	}
	for key, i := range last {
		vals := values(key)
		for _, v := range vals[min(taken[key], len(vals)):] {
			p.at[i].out = append(p.at[i].out, textItem{id: orig[i].id, raw: []byte(v)})
		}
	}
	for _, k := range edited.Keys() {
		// The changed gate keeps a no-op a no-op: without it, an AIFF whose text chunks do
		// not mirror its ID3 chunk would gain a chunk on every write.
		if _, held := taken[k]; !changed[k] || held {
			continue
		}
		if id, ok := mapping.AIFFKeyText(k); ok {
			var id4 [4]byte
			copy(id4[:], id)
			for _, v := range values(k) {
				p.created = append(p.created, textItem{id: id4, raw: []byte(v)})
			}
		}
	}
	return p
}

// holds reports whether the output holds any text chunk.
func (p textPlan) holds() bool {
	return len(p.created) > 0 || slices.ContainsFunc(p.at, func(d textDecision) bool { return d.keep || len(d.out) > 0 })
}

// rewrites reports whether the plan replaces, drops or adds a chunk.
func (p textPlan) rewrites() bool {
	return len(p.created) > 0 || slices.ContainsFunc(p.at, func(d textDecision) bool { return !d.keep || len(d.out) > 0 })
}

// lostTail counts the bytes behind a value's NUL in the chunks the plan does not keep.
func (p textPlan) lostTail(orig []textItem) (n int64) {
	for i, d := range p.at {
		if !d.keep {
			n += orig[i].tail
		}
	}
	return n
}

// textConflictKeys lists the keys this write re-renders whose AIFF family entry
// disagreed with the projection: the conflicting chunks the write replaces. It uses
// the same change set as rebuildText.
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

// textOut builds one native text output chunk: the value bytes verbatim, with no NUL
// terminator; assemble word-aligns it.
func textOut(it textItem) outChunk {
	return outChunk{id: it.id, body: it.raw, bodyLen: int64(len(it.raw)), text: &it}
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
