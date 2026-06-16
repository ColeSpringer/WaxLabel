package core

import "github.com/colespringer/waxlabel/tag"

// Contribution is one canonical value decoded from one native entry, tagged with
// a source label so conflicts between distinct entries for the same key surface.
// It is the shared input to [BuildTagSet] and [BuildFamilies], used by the codecs
// that decode several native entries into the canonical model (ID3 frames, MP4
// ilst atoms) so the conflict rule lives in one place.
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

// BuildFamilies groups contributions by key into family entries for the given
// family, marking an entry unselected when distinct sources supplied distinct
// values for one key — a conflict (e.g. an ID3 TYER vs TDRC recording date, or an
// MP4 legacy gnre vs text genre).
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

// distinctValues counts case- and space-insensitive distinct values.
func distinctValues(vals []string) int {
	seen := map[string]bool{}
	for _, v := range vals {
		seen[Fold(v)] = true
	}
	return len(seen)
}
