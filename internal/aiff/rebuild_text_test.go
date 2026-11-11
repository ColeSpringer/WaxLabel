package aiff

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

func titem(id, val string) textItem {
	var it textItem
	copy(it.id[:], id)
	it.raw = []byte(val)
	return it
}

// chunkStrings renders the chunks a plan leaves the file with, a kept one marked.
func chunkStrings(p textPlan, orig []textItem) []string {
	var s []string
	for i, d := range p.at {
		if d.keep {
			s = append(s, "kept "+string(orig[i].id[:])+"="+string(orig[i].raw))
		}
		for _, it := range d.out {
			s = append(s, string(it.id[:])+"="+string(it.raw))
		}
	}
	for _, it := range p.created {
		s = append(s, "new "+string(it.id[:])+"="+string(it.raw))
	}
	return s
}

func TestRebuildTextKeepsUntouchedChunksVerbatim(t *testing.T) {
	orig := []textItem{titem("NAME", "Aiff Title"), titem("NAME", "Second"), titem("ANNO", "a1"), titem("ANNO", "a2")}
	edited := tag.NewTagSet()
	edited.Set(tag.Title, "Id3 Title")
	edited.Set(tag.Copyright, "ID3 only") // untouched and absent from the chunks: must not be added
	edited.Set(tag.Comment, "a1", "a2")
	edited.Set(tag.Artist, "Art")
	got := chunkStrings(rebuildText(orig, edited, map[tag.Key]bool{tag.Artist: true}), orig)
	want := []string{"kept NAME=Aiff Title", "kept NAME=Second", "kept ANNO=a1", "kept ANNO=a2", "new AUTH=Art"}
	if !slices.Equal(got, want) {
		t.Errorf("rebuildText = %v, want %v", got, want)
	}
}

func TestRebuildTextChangedKeyCollapsesToEditedValues(t *testing.T) {
	orig := []textItem{titem("NAME", "a"), titem("NAME", "b"), titem("ANNO", "x")}
	edited := tag.NewTagSet()
	edited.Set(tag.Title, "New")
	edited.Set(tag.Comment, "y", "z")
	p := rebuildText(orig, edited, map[tag.Key]bool{tag.Title: true, tag.Comment: true})
	want := []string{"NAME=New", "ANNO=y", "ANNO=z"}
	if got := chunkStrings(p, orig); !slices.Equal(got, want) {
		t.Errorf("rebuildText = %v, want %v", got, want)
	}
	if len(p.at[1].out) != 0 || p.at[1].keep {
		t.Errorf("the second NAME = %+v, want it dropped", p.at[1])
	}
}

// A changed key's chunks take its values one by one: a chunk already holding its value is kept
// whole, bytes behind its NUL included, a value past the chunks follows the last of them, and a
// chunk past the values goes.
func TestRebuildTextMatchesChunksToValuesInOrder(t *testing.T) {
	x := textItem{id: [4]byte{'A', 'N', 'N', 'O'}, raw: []byte("x"), tail: 4}
	orig := []textItem{titem("NAME", "T"), x, titem("ANNO", "y")}
	for _, c := range []struct {
		name     string
		comments []string
		want     []string
		rewrites bool
		lost     int64
	}{
		{"the values the chunks hold", []string{"x", "y"}, []string{"kept NAME=T", "kept ANNO=x", "kept ANNO=y"}, false, 0},
		{"one value changed", []string{"x", "z"}, []string{"kept NAME=T", "kept ANNO=x", "ANNO=z"}, true, 0},
		{"a value more", []string{"x", "y", "w"}, []string{"kept NAME=T", "kept ANNO=x", "kept ANNO=y", "ANNO=w"}, true, 0},
		{"a value fewer", []string{"x"}, []string{"kept NAME=T", "kept ANNO=x"}, true, 0},
		{"the first value changed", []string{"q", "y"}, []string{"kept NAME=T", "ANNO=q", "kept ANNO=y"}, true, 4},
		{"none", nil, []string{"kept NAME=T"}, true, 4},
	} {
		edited := tag.NewTagSet()
		edited.Set(tag.Title, "T")
		if c.comments != nil {
			edited.Set(tag.Comment, c.comments...)
		}
		p := rebuildText(orig, edited, map[tag.Key]bool{tag.Comment: true})
		if got := chunkStrings(p, orig); !slices.Equal(got, c.want) || p.rewrites() != c.rewrites || p.lostTail(orig) != c.lost {
			t.Errorf("%s: %v (rewrites %v, lost %d); want %v (%v, %d)", c.name, got, p.rewrites(), p.lostTail(orig), c.want, c.rewrites, c.lost)
		}
	}
}
