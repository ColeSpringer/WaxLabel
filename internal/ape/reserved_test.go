package ape

import (
	"slices"
	"strings"
	"testing"

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
	// Length rule not enforced; see ReservedItemName.
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
		d, reason, override := TransferClassifier(k, []string{"v"}, tag.NewTagSet())
		if !override || d != core.Dropped {
			t.Errorf("TransferClassifier(%s) = %v,%v, want Dropped,true", k, d, override)
		}
		if reason == "" {
			t.Errorf("TransferClassifier(%s) gave no reason", k)
		}
	}
	if _, _, override := TransferClassifier(tag.Title, []string{"v"}, tag.NewTagSet()); override {
		t.Error("TransferClassifier overrode an ordinary key's format-level grade")
	}
}

// TestTransferClassifierGradesCoverNameKeys: Cover Art text => Dropped.
func TestTransferClassifierGradesCoverNameKeys(t *testing.T) {
	for _, k := range []tag.Key{"COVER ART (FRONT)", "COVER ART (BACK)"} {
		d, reason, override := TransferClassifier(k, []string{"v"}, tag.NewTagSet())
		if !override || d != core.Dropped {
			t.Errorf("TransferClassifier(%s) = %v,%v, want Dropped,true", k, d, override)
		}
		if reason == "" {
			t.Errorf("TransferClassifier(%s) gave no reason", k)
		}
	}
}

// TestCapabilitiesCarryTransferClassifier: shared Capabilities install classifier.
func TestCapabilitiesCarryTransferClassifier(t *testing.T) {
	src := &core.Media{Tags: tag.NewTagSet()}
	src.Tags.Add("ID3", "hazard")
	src.Tags.Add(tag.Title, "Song")
	for _, f := range []core.Format{core.FormatWavPack, core.FormatMonkeysAudio, core.FormatMusepack} {
		items := core.ProjectTransfer(src, Capabilities(f, false))
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
