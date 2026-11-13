package ape

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// Shared APEv2 writer/Capabilities cover WavPack, MAC, Musepack.

// TestReservedItemNameFolds: case-folded ("OGGS" still reserved).
func TestReservedItemNameFolds(t *testing.T) {
	for _, name := range []string{"ID3", "id3", "TAG", "tag", "OggS", "OGGS", "MP+", "mp+"} {
		if !ReservedItemName(name) {
			t.Errorf("ReservedItemName(%q) = false, want true", name)
		}
	}
	// The length rule is InvalidItemName's.
	for _, name := range []string{"X", "Title", "ID3v2", "TAGS", "MP", "Ogg"} {
		if ReservedItemName(name) {
			t.Errorf("ReservedItemName(%q) = true, want false", name)
		}
	}
}

// TestRebuildDropsReservedKeys: reserved not authored; drop recorded.
func TestRebuildDropsReservedKeys(t *testing.T) {
	base := tag.NewTagSet()
	edited := tag.NewTagSet()
	edited.Add(tag.Title, "Song")
	for _, k := range []tag.Key{"ID3", "TAG", "OGGS", "MP+"} {
		edited.Add(k, "hazard")
	}
	items, info := Rebuild(nil, base, edited, nil, false)
	for _, it := range items {
		if ReservedItemName(it.Key) {
			t.Errorf("wrote a reserved item name %q", it.Key)
		}
	}
	if len(items) != 1 || items[0].Key != "Title" {
		t.Errorf("items = %+v, want only the Title item", items)
	}
	want := []tag.Key{"ID3", "TAG", "OGGS", "MP+"}
	if !slices.Equal(info.ReservedKeys, want) {
		t.Errorf("ReservedKeys = %v, want %v", info.ReservedKeys, want)
	}
	ws := RebuildWarnings(nil, info)
	if len(ws) != len(want) {
		t.Fatalf("warnings = %d, want %d", len(ws), len(want))
	}
	for i, w := range ws {
		if w.Code != core.WarnValueDropped {
			t.Errorf("warning %d code = %v, want value-dropped", i, w.Code)
		}
		if !slices.Contains(w.Keys, want[i]) {
			t.Errorf("warning %d keys = %v, want %s", i, w.Keys, want[i])
		}
	}
}

// TestRebuildPreservesUntouchedReservedItem: leave prior reserved item alone.
func TestRebuildPreservesUntouchedReservedItem(t *testing.T) {
	orig := []Item{{Key: "ID3", Value: "was already here"}, {Key: "Title", Value: "Old"}}
	base := tag.NewTagSet()
	base.Add("ID3", "was already here")
	base.Add(tag.Title, "Old")
	edited := base.Clone()
	edited.Set(tag.Title, "New")

	items, info := Rebuild(orig, base, edited, nil, false)
	if len(info.ReservedKeys) != 0 {
		t.Errorf("an untouched reserved item was reported dropped: %v", info.ReservedKeys)
	}
	if !slices.ContainsFunc(items, func(it Item) bool { return it.Key == "ID3" && it.Value == "was already here" }) {
		t.Errorf("the pre-existing reserved item was destroyed: %+v", items)
	}
}

// TestRebuildKeepsReservedItemOnRefusedSet: refused set keeps prior reserved item.
func TestRebuildKeepsReservedItemOnRefusedSet(t *testing.T) {
	orig := []Item{{Key: "ID3", Value: "oldvalue"}}
	base := tag.NewTagSet()
	base.Add("ID3", "oldvalue")
	edited := base.Clone()
	edited.Set("ID3", "newvalue")

	items, info := Rebuild(orig, base, edited, nil, false)
	if !slices.Equal(info.ReservedKeys, []tag.Key{"ID3"}) {
		t.Errorf("ReservedKeys = %v, want [ID3]", info.ReservedKeys)
	}
	if len(items) != 1 || items[0].Key != "ID3" || items[0].Value != "oldvalue" {
		t.Errorf("items = %+v, want the original ID3 item preserved", items)
	}
}

// TestTransferClassifierGradesReservedKeys: reserved => Dropped (report==write).
func TestTransferClassifierGradesReservedKeys(t *testing.T) {
	for _, k := range []tag.Key{"ID3", "TAG", "OGGS", "MP+"} {
		d, reason, override := NewTransferClassifier(nil)(k, []string{"v"}, nil)
		if !override || d != core.Dropped {
			t.Errorf("classifier(%s) = %v,%v, want Dropped,true", k, d, override)
		}
		if reason == "" {
			t.Errorf("classifier(%s) gave no reason", k)
		}
	}
	if _, _, override := NewTransferClassifier(nil)(tag.Title, []string{"v"}, nil); override {
		t.Error("classifier overrode an ordinary key's format-level grade")
	}
}

// TestTransferClassifierGradesCoverNameKeys: Cover Art text => Dropped.
func TestTransferClassifierGradesCoverNameKeys(t *testing.T) {
	for _, k := range []tag.Key{"COVER ART (FRONT)", "COVER ART (BACK)"} {
		d, reason, override := NewTransferClassifier(nil)(k, []string{"v"}, nil)
		if !override || d != core.Dropped {
			t.Errorf("classifier(%s) = %v,%v, want Dropped,true", k, d, override)
		}
		if reason == "" {
			t.Errorf("classifier(%s) gave no reason", k)
		}
	}
}

// TestCapabilitiesCarryTransferClassifier: shared Capabilities install classifier.
func TestCapabilitiesCarryTransferClassifier(t *testing.T) {
	src := &core.Media{Tags: tag.NewTagSet()}
	src.Tags.Add("ID3", "hazard")
	src.Tags.Add(tag.Title, "Song")
	for _, f := range []core.Format{core.FormatWavPack, core.FormatMonkeysAudio, core.FormatMusepack} {
		items := core.ProjectTransferOnto(src, nil, Capabilities(f, nil)).Items
		for _, it := range items {
			if it.Key == "ID3" && it.Disposition != core.Dropped {
				t.Errorf("%s: ID3 graded %v, want Dropped", f, it.Disposition)
			}
			if it.Key == tag.Title && it.Disposition != core.Carried {
				t.Errorf("%s: TITLE graded %v, want Carried", f, it.Disposition)
			}
		}
	}
}

// TestInvalidKeyWarningsFlagUnprojectableItems: warn names past canonical 0x7D.
func TestInvalidKeyWarningsFlagUnprojectableItems(t *testing.T) {
	tg := &Tag{Items: []Item{{Key: "MOOD~X", Value: "calm"}, {Key: "Title", Value: "Song"}}}
	ws := InvalidKeyWarnings(tg)
	if len(ws) != 1 || ws[0].Code != core.WarnInvalidTagKey {
		t.Fatalf("warnings = %+v, want one invalid-tag-key", ws)
	}
	if !strings.Contains(ws[0].Message, "MOOD~X") {
		t.Errorf("message = %q, want it to name the item", ws[0].Message)
	}
	// Flagged set == Project omit set.
	if _, ok := Project(tg).Tags.Get("MOOD~X"); ok {
		t.Error("the item was projected, so it should not be flagged")
	}
	if _, ok := Project(tg).Tags.Get(tag.Title); !ok {
		t.Error("an ordinary item was omitted from the projection")
	}
	// Reserved is write-only; prior reserved still reads.
	if ws := InvalidKeyWarnings(&Tag{Items: []Item{{Key: "ID3", Value: "x"}}}); len(ws) != 0 {
		t.Errorf("a reserved item name was flagged as unreadable: %+v", ws)
	}
}

// sameItem reports whether a and b are one item: name, flags, value and bytes.
func sameItem(a, b Item) bool {
	return a.Key == b.Key && a.Flags == b.Flags && a.Value == b.Value && bytes.Equal(a.Data, b.Data)
}

// longKey is a canonical key one character past the 255 an APEv2 item name may hold.
var longKey = tag.Key(strings.Repeat("K", 256))

// TestInvalidItemName: 2 to 255 bytes of printable ASCII.
func TestInvalidItemName(t *testing.T) {
	for _, name := range []string{"", "X", strings.Repeat("K", 256), "Ti\x7ftle", "T\x1fX", "Caf\xc3\xa9"} {
		if reason, bad := InvalidItemName(name); !bad || reason == "" {
			t.Errorf("InvalidItemName(%q) = %q,%v, want a reason and true", name, reason, bad)
		}
	}
	for _, name := range []string{"XY", strings.Repeat("K", 255), "Album Artist", "MOOD~X", "Cover Art (Front)"} {
		if reason, bad := InvalidItemName(name); bad {
			t.Errorf("InvalidItemName(%q) = %q,true, want it accepted", name, reason)
		}
	}
}

// TestRebuildDropsOutOfSpecNames: not authored; drop recorded and warned with the key.
func TestRebuildDropsOutOfSpecNames(t *testing.T) {
	base := tag.NewTagSet()
	edited := tag.NewTagSet()
	edited.Add(tag.Title, "Song")
	edited.Add("X", "one")
	edited.Add(longKey, "long")

	items, info := Rebuild(nil, base, edited, nil, false)
	if len(items) != 1 || items[0].Key != "Title" {
		t.Fatalf("items = %+v, want only the Title item", items)
	}
	ws := RebuildWarnings(nil, info)
	want := []tag.Key{"X", longKey}
	if len(ws) != len(want) {
		t.Fatalf("warnings = %+v, want one per refused name", ws)
	}
	for i, w := range ws {
		if w.Code != core.WarnValueDropped || !slices.Equal(w.Keys, []tag.Key{want[i]}) {
			t.Errorf("warning %d = %v keyed %v, want value-dropped keyed %.8s", i, w.Code, w.Keys, want[i])
		}
	}
	if !strings.Contains(ws[0].Message, "2 to 255") {
		t.Errorf("message = %q, want it to state the length the specification allows", ws[0].Message)
	}
}

// TestRebuildKeepsOutOfSpecItemOnRefusedSet: a refused set keeps the item the file holds.
func TestRebuildKeepsOutOfSpecItemOnRefusedSet(t *testing.T) {
	for _, k := range []tag.Key{"X", longKey} {
		orig := []Item{{Key: string(k), Value: "old", Flags: flagReadOnly}, {Key: "Title", Value: "T"}}
		base := Project(&Tag{Items: orig}).Tags
		edited := base.Clone()
		edited.Set(k, "new")

		items, info := Rebuild(orig, base, edited, nil, false)
		if len(items) != 2 || !sameItem(items[0], orig[0]) {
			t.Errorf("%.8s: items = %+v, want the item the file holds kept as it is", k, items)
		}
		if ws := RebuildWarnings(nil, info); len(ws) != 1 || ws[0].Code != core.WarnValueDropped {
			t.Errorf("%.8s: warnings = %+v, want one value-dropped", k, ws)
		}
	}
}

// TestRebuildKeepsUntouchedOutOfSpecItem: an unrelated edit leaves it alone, unwarned.
func TestRebuildKeepsUntouchedOutOfSpecItem(t *testing.T) {
	orig := []Item{{Key: "X", Value: "old"}, {Key: "Title", Value: "T"}}
	base := Project(&Tag{Items: orig}).Tags
	edited := base.Clone()
	edited.Set(tag.Title, "New")

	items, info := Rebuild(orig, base, edited, nil, false)
	if len(items) != 2 || !sameItem(items[0], orig[0]) || items[1].Value != "New" {
		t.Errorf("items = %+v, want X kept and Title rewritten", items)
	}
	if ws := RebuildWarnings(nil, info); len(ws) != 0 {
		t.Errorf("warnings = %+v, want none", ws)
	}
}

// TestTransferClassifierGradesOutOfSpecNames: Dropped, as the writer refuses them.
func TestTransferClassifierGradesOutOfSpecNames(t *testing.T) {
	for _, k := range []tag.Key{"X", longKey} {
		d, reason, override := NewTransferClassifier(nil)(k, []string{"v"}, nil)
		if !override || d != core.Dropped || !strings.Contains(reason, "2 to 255") {
			t.Errorf("classifier(%.8s) = %v,%q,%v, want Dropped with the length rule", k, d, reason, override)
		}
	}
}

// TestItemNameWarnings: one malformed-tag-entry for the names a file holds outside the
// specification, keyed where a tag reads the item; a name invalid-tag-key reports is left to it.
func TestItemNameWarnings(t *testing.T) {
	bin := uint32(itemTypeBinary << itemTypeShift)
	tg := &Tag{Items: []Item{
		{Key: "X", Value: "one"},
		{Key: "Title", Value: "T"},
		{Key: "B", Data: []byte{1}, Flags: bin},
		{Key: "Caf\xe9", Value: "v"},
	}}
	ws := ItemNameWarnings(tg)
	if len(ws) != 1 || ws[0].Code != core.WarnMalformedTagEntry {
		t.Fatalf("warnings = %+v, want one malformed-tag-entry", ws)
	}
	if !slices.Equal(ws[0].Keys, []tag.Key{"X"}) {
		t.Errorf("keys = %v, want [X]", ws[0].Keys)
	}
	if m := ws[0].Message; !strings.Contains(m, `"X"`) || !strings.Contains(m, "1 more") {
		t.Errorf("message = %q, want the first name and the count of the rest", m)
	}
	if ws := ItemNameWarnings(&Tag{Items: []Item{{Key: "Title", Value: "T"}}}); len(ws) != 0 {
		t.Errorf("warnings = %+v, want none for names inside the specification", ws)
	}
	if ws := ItemNameWarnings(nil); len(ws) != 0 {
		t.Errorf("nil tag: warnings = %+v, want none", ws)
	}
}

// TestRebuildWritesTheKeysOwnNameOverANameItCannotWrite: an item under a name outside the
// specification whose key has a name inside it is written under that name when its key is edited,
// its flags kept, with no warning; an unrelated edit leaves it as it is.
func TestRebuildWritesTheKeysOwnNameOverANameItCannotWrite(t *testing.T) {
	names := func(items []Item) []string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.Key + "=" + it.Value
		}
		return out
	}
	for _, c := range []struct {
		name string
		orig []Item
		mut  func(*tag.TagSet)
		want []string
	}{
		{"artist", []Item{{Key: "Artist\t", Value: "Old", Flags: flagReadOnly}, {Key: "Title", Value: "T"}},
			func(ts *tag.TagSet) { ts.Set(tag.Artist, "New") }, []string{"Artist=New", "Title=T"}},
		{"a second item under the key's own name", []Item{{Key: "Artist\t", Value: "A2"}, {Key: "Artist", Value: "A1"}},
			func(ts *tag.TagSet) { ts.Set(tag.Artist, "New") }, []string{"Artist=New"}},
		{"slash number", []Item{{Key: "Track\t", Value: "3/12"}, {Key: "Title", Value: "T"}},
			func(ts *tag.TagSet) { ts.Set(tag.TrackNumber, "5") }, []string{"Track=5", "TRACKTOTAL=12", "Title=T"}},
		{"slash total", []Item{{Key: "Track\t", Value: "3/12"}, {Key: "Title", Value: "T"}},
			func(ts *tag.TagSet) { ts.Set(tag.TrackTotal, "20") }, []string{"Track=3", "TRACKTOTAL=20", "Title=T"}},
		{"unrelated edit", []Item{{Key: "Track\t", Value: "3/12"}, {Key: "Artist\t", Value: "Old"}, {Key: "Title", Value: "T"}},
			func(ts *tag.TagSet) { ts.Set(tag.Title, "New") }, []string{"Track\t=3/12", "Artist\t=Old", "Title=New"}},
		// The key has no name of its own inside the specification, but the file spells one that is.
		{"a spelling of the file's inside the specification", []Item{{Key: " X ", Value: "one"}},
			func(ts *tag.TagSet) { ts.Set("X", "two") }, []string{" X =two"}},
		{"that spelling behind an item that has none", []Item{{Key: "X", Value: "one"}, {Key: "Title", Value: "T"}, {Key: " X ", Value: "uno"}},
			func(ts *tag.TagSet) { ts.Set("X", "two") }, []string{" X =two", "Title=T"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := Project(&Tag{Items: c.orig}).Tags
			edited := base.Clone()
			c.mut(&edited)
			items, info := Rebuild(c.orig, base, edited, nil, false)
			if got := names(items); !slices.Equal(got, c.want) {
				t.Errorf("items = %q, want %q", got, c.want)
			}
			if ws := RebuildWarnings(nil, info); len(ws) != 0 {
				t.Errorf("warnings = %+v, want none", ws)
			}
			if c.name == "artist" && items[0].Flags != flagReadOnly {
				t.Errorf("flags = %#x, want the item's own kept", items[0].Flags)
			}
		})
	}
}

// TestRebuildRefusedSetKeepsEveryItemOfTheKey: a key held in several items, none of which can be
// written again: the refused set leaves them all.
func TestRebuildRefusedSetKeepsEveryItemOfTheKey(t *testing.T) {
	for _, name := range []string{"X", "ID3"} {
		orig := []Item{{Key: name, Value: "one"}, {Key: "Title", Value: "T"}, {Key: name, Value: "uno"}}
		base := Project(&Tag{Items: orig}).Tags
		if v, _ := base.Get(tag.Key(name)); !slices.Equal(v, []string{"one", "uno"}) {
			t.Fatalf("%s = %q, want both items read", name, v)
		}
		edited := base.Clone()
		edited.Set(tag.Key(name), "New")
		items, info := Rebuild(orig, base, edited, nil, false)
		if len(items) != 3 || !sameItem(items[0], orig[0]) || !sameItem(items[1], orig[1]) || !sameItem(items[2], orig[2]) {
			t.Errorf("%s: items = %+v, want the file's three items as they are", name, items)
		}
		if ws := RebuildWarnings(nil, info); len(ws) != 1 || ws[0].Code != core.WarnValueDropped {
			t.Errorf("%s: warnings = %+v, want one value-dropped", name, ws)
		}
	}
}

// TestTransferClassifierKnowsTheDestinationsItems: a key the destination holds under a name that
// can be written is carried, and one it holds with the very values is carried whatever its name.
func TestTransferClassifierKnowsTheDestinationsItems(t *testing.T) {
	items := []Item{{Key: "X", Value: "one"}, {Key: "X", Value: "uno"}, {Key: " Y ", Value: "why"},
		{Key: "Artist\t", Value: "Old"}, {Key: "ID3", Value: "v"}}
	classify := NewTransferClassifier(items)
	for _, c := range []struct {
		key  tag.Key
		vals []string
		want core.Disposition
	}{
		{"X", []string{"one", "uno"}, core.Carried},
		{"X", []string{"one"}, core.Dropped},
		{"X", []string{"two"}, core.Dropped},
		{"Y", []string{"other"}, core.Carried},
		{tag.Artist, []string{"New"}, core.Carried},
		{"ID3", []string{"v"}, core.Carried},
		{"ID3", []string{"w"}, core.Dropped},
		{"Z", []string{"new"}, core.Dropped},
		{tag.Title, []string{"T"}, core.Carried},
	} {
		d, reason, override := classify(c.key, c.vals, nil)
		if !override {
			d = core.Carried
		}
		if d != c.want || (d == core.Dropped && reason == "") {
			t.Errorf("%s=%q: graded %v (%q), want %v", c.key, c.vals, d, reason, c.want)
		}
	}
	// A spelling the format allows, behind an item that has none, is the one a write uses.
	spelled := NewTransferClassifier([]Item{{Key: "X", Value: "one"}, {Key: " X ", Value: "uno"}})
	if d, reason, override := spelled("X", []string{"new"}, nil); override {
		t.Errorf("a writable spelling behind an unwritable one: X graded %v (%q), want carried", d, reason)
	}
	// A binary item under the key's name holds no text of it.
	binary := NewTransferClassifier([]Item{{Key: "X", Data: []byte{1}, Flags: itemTypeBinary << itemTypeShift}})
	if d, _, override := binary("X", []string{""}, nil); !override || d != core.Dropped {
		t.Errorf("beside a binary item: X graded %v,%v, want Dropped", d, override)
	}
	// With no file, a key is graded by its own name.
	if d, _, override := NewTransferClassifier(nil)("Y", []string{"other"}, nil); !override || d != core.Dropped {
		t.Errorf("no file: Y graded %v,%v, want Dropped", d, override)
	}
}

// onePass bounds the tests that pin a pass over n things against a search of what was gathered for
// each of them. It is far above what one pass takes on a slow runner under the race detector, and
// the search takes minutes at the sizes these tests use.
const onePass = 30 * time.Second

// TestItemNameWarningsInOnePass: the keys of the items named outside the specification are
// gathered in one pass, whatever their number.
func TestItemNameWarningsInOnePass(t *testing.T) {
	const n = 500000
	items := make([]Item, n)
	for i := range items {
		items[i] = Item{Key: "\tK" + strconv.Itoa(i), Value: "v"} // a tab ahead of a name that reads as a key
	}
	start := time.Now()
	ws := ItemNameWarnings(&Tag{Items: items})
	if d := time.Since(start); d > onePass {
		t.Errorf("the warning took %v", d)
	}
	if len(ws) != 1 || len(ws[0].Keys) != n {
		t.Fatalf("warnings = %d, want one keyed by all %d names", len(ws), n)
	}
	// A key two items read is listed once.
	ws = ItemNameWarnings(&Tag{Items: []Item{{Key: "X", Value: "a"}, {Key: "x", Value: "b"}}})
	if len(ws) != 1 || !slices.Equal(ws[0].Keys, []tag.Key{"X"}) {
		t.Errorf("warnings = %+v, want X listed once", ws)
	}
}
