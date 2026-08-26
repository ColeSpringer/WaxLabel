package matroska

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// keyVals is one canonical key with its value list, used to build the base and
// edited sets of a decisions case in a fixed order.
type keyVals struct {
	key  tag.Key
	vals []string
}

func tagSet(kvs ...keyVals) tag.TagSet {
	ts := tag.NewTagSet()
	for _, kv := range kvs {
		ts.Set(kv.key, kv.vals...)
	}
	return ts
}

// simple builds a parsed SimpleTag carrying a string value, the shape the value
// survival pass reasons about. raw stands in for the captured bytes so the
// decisions can be driven without a real parse.
func simple(name, value string) simpleTag {
	return simpleTag{name: name, value: value, hasValue: true, raw: []byte(name)}
}

// albumGroup and trackGroup build the two scopes the decisions distinguish: the
// group buildAlbumGroup syncs into, and a UID-narrowed group that must be able to
// keep a still-wanted value in place.
func albumGroup(tags ...simpleTag) tagGroup {
	return tagGroup{scope: core.ScopeAlbum, targetTypeValue: 50, tags: tags, raw: []byte("album")}
}

func trackGroup(tags ...simpleTag) tagGroup {
	return tagGroup{scope: core.ScopeTrack, targetTypeValue: 50, trackUID: true, tags: tags, raw: []byte("track")}
}

// TestComputeEditDecisionsValueSurvival covers the value-level outcome of an edit
// against a scoped tag tree: which parsed SimpleTags survive at their own scope,
// and which values are left for the album-scope re-emit.
func TestComputeEditDecisionsValueSurvival(t *testing.T) {
	cases := []struct {
		name      string
		groups    []tagGroup
		base      []keyVals
		edited    []keyVals
		wantDrops [][2]int
		wantAlbum []keyVals
	}{
		{
			// The edit keeps the track-scoped value, so that tag stays
			// put and only the album copy is dropped. Nothing re-emits at album scope.
			name: "exact keep consumes the value",
			groups: []tagGroup{
				albumGroup(simple("ARTIST", "AA"), simple("ENCODER", "album-enc")),
				trackGroup(simple("ENCODER", "track-enc")),
			},
			base:      []keyVals{{tag.Artist, []string{"AA"}}, {tag.Encoder, []string{"album-enc", "track-enc"}}},
			edited:    []keyVals{{tag.Artist, []string{"AA"}}, {tag.Encoder, []string{"track-enc"}}},
			wantDrops: [][2]int{{0, 1}},
			wantAlbum: []keyVals{{tag.Encoder, nil}},
		},
		{
			// The track ARTIST differs from the album one only by case, so the reader
			// already suppresses it as a cross-scope echo. Appending a value keeps that
			// echo covered by the album emit, so the tag survives untouched.
			name: "suppressed echo survives while its fold stays covered",
			groups: []tagGroup{
				albumGroup(simple("ARTIST", "X")),
				trackGroup(simple("ARTIST", "x")),
			},
			base:      []keyVals{{tag.Artist, []string{"X"}}},
			edited:    []keyVals{{tag.Artist, []string{"X", "Y"}}},
			wantDrops: [][2]int{{0, 0}},
			wantAlbum: []keyVals{{tag.Artist, []string{"X", "Y"}}},
		},
		{
			// A fold-duplicated value list cannot keep a scoped copy: the reader's echo
			// suppression would halve the multiplicity, so both values collapse to album.
			name: "fold-duplicated values collapse to album scope",
			groups: []tagGroup{
				albumGroup(simple("ARTIST", "AA")),
				trackGroup(simple("ARTIST", "x")),
			},
			base:      []keyVals{{tag.Artist, []string{"AA", "x"}}},
			edited:    []keyVals{{tag.Artist, []string{"X", "x"}}},
			wantDrops: [][2]int{{0, 0}, {1, 0}},
			wantAlbum: []keyVals{{tag.Artist, []string{"X", "x"}}},
		},
		{
			// One SimpleTag carries both halves of a slash number. The number half is
			// kept exactly, but the total half is edited away, so the whole tag dies and
			// the number the keep had consumed is released back to the album re-emit.
			name: "slash conjunction releases the claimed half",
			groups: []tagGroup{
				albumGroup(simple("PART_NUMBER", "7")),
				trackGroup(simple("PART_NUMBER", "2/10")),
			},
			base:      []keyVals{{tag.TrackNumber, []string{"7", "2"}}, {tag.TrackTotal, []string{"10"}}},
			edited:    []keyVals{{tag.TrackNumber, []string{"2"}}, {tag.TrackTotal, []string{"12"}}},
			wantDrops: [][2]int{{0, 0}, {1, 0}},
			wantAlbum: []keyVals{{tag.TrackNumber, []string{"2"}}, {tag.TrackTotal, []string{"12"}}},
		},
		{
			// Only the number half is edited and the track tag already holds the wanted
			// value, so the tag survives and carries the untouched total along with it.
			name: "slash tag survives when the edited half still matches",
			groups: []tagGroup{
				albumGroup(simple("PART_NUMBER", "7")),
				trackGroup(simple("PART_NUMBER", "2/10")),
			},
			base:      []keyVals{{tag.TrackNumber, []string{"7", "2"}}, {tag.TrackTotal, []string{"10"}}},
			edited:    []keyVals{{tag.TrackNumber, []string{"2"}}, {tag.TrackTotal, []string{"10"}}},
			wantDrops: [][2]int{{0, 0}},
			wantAlbum: []keyVals{{tag.TrackNumber, nil}},
		},
		{
			// A cleared key is removed from every scope, echo or not.
			name: "cleared key drops every scoped copy",
			groups: []tagGroup{
				albumGroup(simple("ENCODER", "album-enc")),
				trackGroup(simple("ENCODER", "track-enc"), simple("COMPOSER", "TC")),
			},
			base:      []keyVals{{tag.Composer, []string{"TC"}}, {tag.Encoder, []string{"album-enc", "track-enc"}}},
			edited:    []keyVals{{tag.Composer, []string{"TC"}}},
			wantDrops: [][2]int{{0, 0}, {1, 0}},
			wantAlbum: []keyVals{{tag.Encoder, nil}},
		},
		{
			// A binary- or nested-only SimpleTag projects nothing, so an edit to its key
			// cannot drop it: it is preserved verbatim instead of flattened away.
			name: "a tag with no projecting contribution never drops",
			groups: []tagGroup{
				albumGroup(simpleTag{name: "ARTIST", binary: 4, raw: []byte("bin")}),
			},
			base:      nil,
			edited:    []keyVals{{tag.Artist, []string{"New"}}},
			wantDrops: nil,
			wantAlbum: []keyVals{{tag.Artist, []string{"New"}}},
		},
		{
			// A claim released by a doomed tag is re-offered to a denied twin: the
			// slash tag's number half claims "2" first but dies with its cleared
			// total, so the plain PART_NUMBER=2 keeps its scope instead of being
			// deleted and re-synthesized at album scope.
			name: "released claim re-offers to a denied twin",
			groups: []tagGroup{
				trackGroup(simple("TOTAL_PARTS", "4")),
				trackGroup(simple("PART_NUMBER", "2/4")),
				trackGroup(simple("PART_NUMBER", "1"), simple("PART_NUMBER", "2")),
			},
			base:      []keyVals{{tag.TrackNumber, []string{"2", "1", "2"}}, {tag.TrackTotal, []string{"4", "4"}}},
			edited:    []keyVals{{tag.TrackNumber, []string{"2"}}},
			wantDrops: [][2]int{{0, 0}, {1, 0}, {2, 0}},
			wantAlbum: []keyVals{{tag.TrackNumber, nil}, {tag.TrackTotal, nil}},
		},
		{
			// An echo whose fold is kept in place at an earlier scope stays
			// suppressed on re-read, so its tag survives even though the album
			// emit carries nothing.
			name: "echo covered by a value kept at an earlier scope",
			groups: []tagGroup{
				albumGroup(simple("ARTIST", "X")),
				trackGroup(simple("ARTIST", "Y")),
				trackGroup(simple("ARTIST", "Y")),
			},
			base:      []keyVals{{tag.Artist, []string{"X", "Y"}}},
			edited:    []keyVals{{tag.Artist, []string{"Y"}}},
			wantDrops: [][2]int{{0, 0}},
			wantAlbum: []keyVals{{tag.Artist, nil}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ed := computeEditDecisions(c.groups, albumGroupIndex(c.groups), tagSet(c.base...), tagSet(c.edited...))
			var gotDrops [][2]int
			for gi, g := range c.groups {
				for ti := range g.tags {
					if ed.dropped(gi, ti) {
						gotDrops = append(gotDrops, [2]int{gi, ti})
					}
				}
			}
			if !slices.Equal(gotDrops, c.wantDrops) {
				t.Errorf("drops = %v, want %v", gotDrops, c.wantDrops)
			}
			if len(ed.albumVals) != len(c.wantAlbum) {
				t.Errorf("albumVals has %d keys, want %d (%v)", len(ed.albumVals), len(c.wantAlbum), ed.albumVals)
			}
			for _, kv := range c.wantAlbum {
				if got := ed.albumVals[kv.key]; !slices.Equal(got, kv.vals) {
					t.Errorf("albumVals[%s] = %v, want %v", kv.key, got, kv.vals)
				}
			}
		})
	}
}
