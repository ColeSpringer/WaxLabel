package aiff

import (
	"slices"
	"strconv"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

func titem(id, val string) textItem {
	var it textItem
	copy(it.id[:], id)
	it.raw = []byte(val)
	return it
}

// chunkStrings renders the fields the rewrite depends on. The role is included because
// assemble records a chunk in the result document's text index by role alone: a copied chunk
// that lost roleText still writes the right bytes, so only this catches it.
func chunkStrings(out []outChunk) []string {
	s := make([]string, len(out))
	for i, c := range out {
		role := "text"
		if c.role != roleText {
			role = "role" + strconv.Itoa(int(c.role))
		}
		s[i] = role + ":" + string(c.id[:]) + "=" + string(c.body)
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
	want := []string{"text:NAME=Aiff Title", "text:NAME=Second", "text:ANNO=a1", "text:ANNO=a2", "text:AUTH=Art"}
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
	want := []string{"text:NAME=New", "text:ANNO=y", "text:ANNO=z"}
	if !slices.Equal(chunkStrings(got), want) {
		t.Errorf("rebuildText = %v, want %v", chunkStrings(got), want)
	}
}
