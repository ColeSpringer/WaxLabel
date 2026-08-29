package core

import (
	"slices"
	"strings"

	"github.com/colespringer/waxlabel/tag"
)

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
// values for one key - a conflict (e.g. an ID3 TYER vs TDRC recording date, or an
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

// distinctValues counts case- and space-insensitive distinct values using the
// same fold rule as dump duplicate markers.
func distinctValues(vals []string) int { return tag.DistinctValues(vals) }

// DiffKeys returns the canonical keys whose values differ between base and edited -
// added, removed, or modified. It is the change set every minimal-change rebuild
// consults to decide which native entries to re-render and which to leave verbatim,
// shared so the Vorbis and APE writers cannot come to different verdicts about the
// same edit.
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

// StripDroppedMessage is the wording for a LegacyStrip write that destroyed data held only in
// the container it removed. container names what went ("legacy container", "LIST/INFO chunk")
// and lost describes what went with it; the skeleton is shared so the two producers - the
// editor, for a legacy container, and the WAV codec, for the native chunk that policy also
// removes - cannot describe one policy two ways. It names the remedy, since a warning about a
// flag the user typed is only useful if it says what to type instead.
func StripDroppedMessage(container string, lost []string) string {
	return "--legacy strip removed the " + container + " along with " + strings.Join(lost, " and ") +
		"; omit it to keep the " + container
}

// LegacyStripDroppedMessage is [StripDroppedMessage] for the legacy containers: the keys whose
// sole copy was there, the opaque non-tag content the projection cannot fold in, or both. The
// two halves share one message because a write has one remedy for them.
func LegacyStripDroppedMessage(keys []tag.Key, opaque bool) string {
	var lost []string
	if len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = string(k)
		}
		lost = append(lost, "values held only there ("+strings.Join(names, ", ")+")")
	}
	if opaque {
		lost = append(lost, "content the canonical view does not carry")
	}
	return StripDroppedMessage("legacy container", lost)
}

// LegacyOnlyKeys returns, in family order, the canonical keys that exist only in a legacy
// container (MP3 ID3v1/APEv2, FLAC's leading ID3v2 or trailing ID3v1) and not in auth.
// These are exactly the values a legacy strip destroys.
//
// auth is an explicit argument rather than being read off a Media because the two callers
// ask about different authorities and both are right: [Document.LegacyOnlyKeys] asks what
// the parsed file holds, so dump and the safe auto-fix can see values the canonical set
// omits, while the editor asks what the pending write holds, so a strip that is also
// setting ALBUM does not claim to be losing ALBUM. Same rule, two authorities.
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

// LegacyFamilies projects a legacy container's key/value pairs into family entries.
// media.Tags stays whatever the format's authoritative store holds, so this surfaces a
// value living only in a preserved container - and flags it when it disagrees - without
// promoting it into the canonical set. Every Legacy entry is what the editor's
// legacy-conflict warning fires on, so building them in one place is what keeps that
// warning from either missing a container or firing on a format's own store.
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
