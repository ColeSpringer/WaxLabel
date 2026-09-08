package wav

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

func item(id, val string) infoItem {
	var it infoItem
	copy(it.id[:], id)
	it.raw = []byte(val)
	return it
}

func ids(items []infoItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.id4() + "=" + string(it.raw)
	}
	return out
}

func TestRebuildInfoKeepsUntouchedItemsVerbatim(t *testing.T) {
	orig := []infoItem{item("INAM", "Riff Title"), item("IPRT", "7"), item("ITRK", "9"), item("ICMT", "one"), item("ICMT", "two"), item("IENG", "keep")}
	edited := tag.NewTagSet()
	edited.Set(tag.Title, "Id3 Title")   // the projection's value differs from INFO's
	edited.Set(tag.Artist, "Id3 Artist") // lives only in the id3 chunk: untouched, so INFO must not gain it
	edited.Set(tag.TrackNumber, "7")
	edited.Set(tag.Comment, "one", "two")
	edited.Set(tag.Album, "Z")
	got := rebuildInfo(orig, edited, map[tag.Key]bool{tag.Album: true}, false)
	want := []string{"INAM=Riff Title", "IPRT=7", "ITRK=9", "ICMT=one", "ICMT=two", "IENG=keep", "IPRD=Z"}
	if !slices.Equal(ids(got), want) {
		t.Errorf("rebuildInfo = %v, want %v", ids(got), want)
	}
}

func TestRebuildInfoChangedKeyUpdatesEveryIdentifierOnce(t *testing.T) {
	orig := []infoItem{item("IPRT", "7"), item("ITRK", "9"), item("INAM", "a"), item("INAM", "b")}
	edited := tag.NewTagSet()
	edited.Set(tag.TrackNumber, "3")
	edited.Set(tag.Title, "New")
	got := rebuildInfo(orig, edited, map[tag.Key]bool{tag.TrackNumber: true, tag.Title: true}, false)
	want := []string{"IPRT=3", "ITRK=3", "INAM=New"}
	if !slices.Equal(ids(got), want) {
		t.Errorf("rebuildInfo = %v, want %v", ids(got), want)
	}
}

func TestRebuildInfoChangedKeyAbsentDropsEveryItem(t *testing.T) {
	orig := []infoItem{item("INAM", "a"), item("INAM", "b"), item("IART", "x")}
	edited := tag.NewTagSet()
	edited.Set(tag.Artist, "x")
	got := rebuildInfo(orig, edited, map[tag.Key]bool{tag.Title: true}, false)
	if want := []string{"IART=x"}; !slices.Equal(ids(got), want) {
		t.Errorf("rebuildInfo = %v, want %v", ids(got), want)
	}
}

func TestInfoRepresentableIgnoresUntouchedKeys(t *testing.T) {
	ts := tag.NewTagSet()
	ts.Set(tag.Title, "a", "b") // two INAM items, untouched: already in INFO
	ts.Set(tag.Album, "Z")
	if !infoRepresentable(ts, map[tag.Key]bool{tag.Album: true}) {
		t.Error("an untouched multi-value key must not force an id3 chunk")
	}
	if infoRepresentable(ts, map[tag.Key]bool{tag.Title: true}) {
		t.Error("a changed multi-value key is not INFO-representable")
	}
}
