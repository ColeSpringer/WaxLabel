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

// chunkStrings renders the fields the rewrite depends on.
func chunkStrings(out []outChunk) []string {
	s := make([]string, len(out))
	for i, c := range out {
		s[i] = string(c.id[:]) + "=" + string(c.body)
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
	got := rebuildText(orig, edited, map[tag.Key]bool{tag.Artist: true})
	want := []string{"NAME=Aiff Title", "NAME=Second", "ANNO=a1", "ANNO=a2", "AUTH=Art"}
	if !slices.Equal(chunkStrings(got), want) {
		t.Errorf("rebuildText = %v, want %v", chunkStrings(got), want)
	}
}

func TestRebuildTextChangedKeyCollapsesToEditedValues(t *testing.T) {
	orig := []textItem{titem("NAME", "a"), titem("NAME", "b"), titem("ANNO", "x")}
	edited := tag.NewTagSet()
	edited.Set(tag.Title, "New")
	edited.Set(tag.Comment, "y", "z")
	got := rebuildText(orig, edited, map[tag.Key]bool{tag.Title: true, tag.Comment: true})
	want := []string{"NAME=New", "ANNO=y", "ANNO=z"}
	if !slices.Equal(chunkStrings(got), want) {
		t.Errorf("rebuildText = %v, want %v", chunkStrings(got), want)
	}
}
