package core

import (
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

// Contribution is one decoded canonical value with a source label for conflict detection.
type Contribution struct {
	Key    tag.Key
	Value  string
	Source string
}

// BuildTagSet assembles the authoritative TagSet from contributions, preserving
// their order.
func BuildTagSet(contribs []Contribution) tag.TagSet {
	ts := tag.NewTagSet()
	for _, c := range contribs {
		ts.Add(c.Key, c.Value)
	}
	return ts
}

// BuildFamilies groups contributions by key; unselected when distinct sources disagree.
func BuildFamilies(contribs []Contribution, family Family) []FamilyValue {
	index := map[tag.Key]int{}
	srcs := map[tag.Key]map[string]bool{}
	var fams []FamilyValue
	for _, c := range contribs {
		if i, ok := index[c.Key]; ok {
			fams[i].Values = append(fams[i].Values, c.Value)
		} else {
			index[c.Key] = len(fams)
			srcs[c.Key] = map[string]bool{}
			fams = append(fams, FamilyValue{
				Key: c.Key, Family: family, Scope: ScopeTrack,
				Values: []string{c.Value}, Selected: true,
			})
		}
		srcs[c.Key][c.Source] = true
	}
	for key, i := range index {
		if len(srcs[key]) > 1 && distinctValues(fams[i].Values) > 1 {
			fams[i].Selected = false
		}
	}
	return fams
}

// distinctValues counts fold-distinct values (same rule as dump duplicates).
func distinctValues(vals []string) int { return tag.DistinctValues(vals) }

// DiffKeys returns keys added, removed, or changed between base and edited.
func DiffKeys(base, edited tag.TagSet) map[tag.Key]bool {
	changed := map[tag.Key]bool{}
	for _, k := range base.Keys() {
		bv, _ := base.Get(k)
		ev, has := edited.Get(k)
		if !has || !slices.Equal(bv, ev) {
			changed[k] = true
		}
	}
	for _, k := range edited.Keys() {
		if !base.Has(k) {
			changed[k] = true
		}
	}
	return changed
}

// ChangedKeys is DiffKeys plus touched keys (for native stores that can disagree with projection).
func ChangedKeys(base, edited tag.TagSet, touched map[tag.Key]bool) map[tag.Key]bool {
	changed := DiffKeys(base, edited)
	for k := range touched {
		changed[k] = true
	}
	return changed
}

// StripDroppedMessage words a LegacyStrip data loss. A preset strips too, so the remedy is the
// flag that overrides both.
func StripDroppedMessage(container string, lost []string) string {
	return "the legacy strip removed the " + container + " along with " + strings.Join(lost, " and ") +
		"; --legacy preserve keeps the " + container
}

// LegacyStripDroppedMessage is [StripDroppedMessage] for legacy containers.
func LegacyStripDroppedMessage(keys []tag.Key, opaque bool) string {
	var lost []string
	if len(keys) > 0 {
		lost = append(lost, "values held only there ("+joinKeys(keys)+")")
	}
	if opaque {
		lost = append(lost, "content the canonical view does not carry")
	}
	return StripDroppedMessage("legacy container", lost)
}

// StrippedValuesLost words the keys [StrippedFamilyKeys] returns for [StripDroppedMessage].
func StrippedValuesLost(keys []tag.Key) string {
	return "values the canonical set does not carry (" + joinKeys(keys) + ")"
}

func joinKeys(keys []tag.Key) string {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

// StrippedFamilyKeys lists the keys whose fam entry holds a value that dies with a stripped native
// container: the projection did not select it (the ID3 chunk disagreed) and edited does not hold it.
func StrippedFamilyKeys(fams []FamilyValue, fam Family, edited tag.TagSet) []tag.Key {
	var out []tag.Key
	seen := map[tag.Key]bool{}
	kept := FamilySelector(edited)
	for _, f := range fams {
		if f.Family != fam || f.Selected || seen[f.Key] || len(f.Values) == 0 || kept(f.Key, f.Values[0]) {
			continue
		}
		seen[f.Key] = true
		out = append(out, f.Key)
	}
	return out
}

// LegacyOnlyKeys returns legacy-only keys not in auth. auth is caller-chosen authority
// (parsed file vs pending write).
func LegacyOnlyKeys(fams []FamilyValue, auth tag.TagSet) []tag.Key {
	var out []tag.Key
	seen := make(map[tag.Key]bool)
	for _, f := range fams {
		if f.Legacy && !auth.Has(f.Key) && !seen[f.Key] {
			seen[f.Key] = true
			out = append(out, f.Key)
		}
	}
	return out
}

// DuplicateContent describes what a duplicate container a rewrite drops holds: its values, which
// [AppendDuplicateBlockDropped] grades against the parsed and written sets, and the pictures,
// chapters and synced lyric sets the file holds nowhere else. Other counts what else it holds that
// the file holds nowhere else, by kind: content the canonical view does not carry (`"POPM"
// frame(s)`) or could not read (`unreadable ID3v2.5 tag(s)`).
type DuplicateContent struct {
	Tags         tag.TagSet
	Pictures     int
	Chapters     int
	SyncedLyrics int
	Other        []ContentCount
}

// ContentCount is N items of one kind, the kind as a warning names it.
type ContentCount struct {
	Kind string
	N    int64
}

// Tally counts content by kind in the order the kinds first appear, as [DuplicateContent.Other]
// lists it. The zero value is empty and ready to use.
type Tally struct {
	counts []ContentCount
	at     map[string]int
}

// Add counts n more of kind.
func (t *Tally) Add(kind string, n int64) {
	if i, ok := t.at[kind]; ok {
		t.counts[i].N += n
		return
	}
	if t.at == nil {
		t.at = map[string]int{}
	}
	t.at[kind] = len(t.counts)
	t.counts = append(t.counts, ContentCount{Kind: kind, N: n})
}

// Counts lists the kinds counted, nil when none.
func (t *Tally) Counts() []ContentCount { return t.counts }

// UnsubsumedKeys returns, in losing's key order, the canonical keys for which losing holds a value
// none of holders does.
func UnsubsumedKeys(losing tag.TagSet, holders ...tag.TagSet) []tag.Key {
	var out []tag.Key
	for _, k := range losing.Keys() {
		kept := map[string]bool{}
		for _, h := range holders {
			vals, _ := h.Get(k)
			for _, v := range vals {
				kept[v] = true
			}
		}
		lost, _ := losing.Get(k)
		for _, v := range lost {
			if !kept[v] {
				out = append(out, k)
				break
			}
		}
	}
	return out
}

// LegacyFamilies projects legacy pairs into family entries without promoting to canonical tags.
func LegacyFamilies(auth tag.TagSet, family Family, pairs []Contribution) []FamilyValue {
	out := make([]FamilyValue, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, FamilyValue{
			Key: p.Key, Family: family, Scope: ScopeTrack,
			Values: []string{p.Value}, Selected: FamilySelected(auth, p.Key, p.Value), Legacy: true,
		})
	}
	return out
}
