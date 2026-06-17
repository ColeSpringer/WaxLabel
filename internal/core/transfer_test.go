package core

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestProjectTransferDispositions exercises all three dispositions in one pass —
// including the Lossy path, which no shipping codec currently produces (every real
// codec writes a field Full or None), using a synthetic capability set.
func TestProjectTransferDispositions(t *testing.T) {
	var ts tag.TagSet
	ts.Set("TITLE", "x")
	ts.Set("ARTIST", "y")
	m := &Media{
		Format:   FormatFLAC,
		Tags:     ts,
		Pictures: []Picture{{}},
		Chapters: []Chapter{{}, {}},
	}
	caps := NewCapabilities(FormatMP4, false,
		Capability{Write: AccessFull},                              // generic field
		Capability{Write: AccessNone, Representation: "no covers"}, // pictures
		Capability{Write: AccessFull},                              // chapters
		map[tag.Key]Capability{
			"ARTIST": {Write: AccessPartial, Fidelity: "ASCII only"},
		},
	)

	items := ProjectTransfer(m, caps)
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4 (2 fields, pictures, chapters)", len(items))
	}
	want := []struct {
		kind   TransferKind
		key    tag.Key
		count  int
		disp   Disposition
		reason string
	}{
		{TransferField, "TITLE", 1, Carried, ""},
		{TransferField, "ARTIST", 1, Lossy, "ASCII only"},
		{TransferPicture, "", 1, Dropped, "unsupported: no covers"},
		{TransferChapter, "", 2, Carried, ""},
	}
	for i, w := range want {
		it := items[i]
		if it.Kind != w.kind || it.Key != w.key || it.Count != w.count ||
			it.Disposition != w.disp || it.Reason != w.reason {
			t.Errorf("item %d = %+v, want %+v", i, it, w)
		}
	}

	carried, lossy, dropped := (TransferReport{Items: items}).Counts()
	if carried != 2 || lossy != 1 || dropped != 1 {
		t.Errorf("counts = (%d,%d,%d), want (2,1,1)", carried, lossy, dropped)
	}
}

// TestProjectTransferMaxItems checks that a set exceeding the destination's hard
// MaxItems cap is reported Dropped — the destination would reject the whole set at
// write time, so reporting it carried would break the report==write invariant.
func TestProjectTransferMaxItems(t *testing.T) {
	caps := NewCapabilities(FormatMP4, false,
		Capability{Write: AccessFull}, Capability{Write: AccessFull},
		Capability{Write: AccessFull, MaxItems: 255}, nil)

	over := &Media{Format: FormatFLAC, Chapters: make([]Chapter, 256)}
	items := ProjectTransfer(over, caps)
	if len(items) != 1 || items[0].Kind != TransferChapter || items[0].Disposition != Dropped {
		t.Fatalf("256 chapters vs limit 255 should drop, got %+v", items)
	}
	if items[0].Reason == "" {
		t.Error("an over-limit drop must carry a reason")
	}

	atLimit := &Media{Format: FormatFLAC, Chapters: make([]Chapter, 255)}
	if got := ProjectTransfer(atLimit, caps); got[0].Disposition != Carried {
		t.Errorf("255 chapters at the limit should carry, got %s", got[0].Disposition)
	}
}

// TestProjectTransferReadOnlyDropsEverything checks that a read-only destination
// drops every item regardless of the per-field write level.
func TestProjectTransferReadOnlyDropsEverything(t *testing.T) {
	var ts tag.TagSet
	ts.Set("TITLE", "x")
	m := &Media{Format: FormatFLAC, Tags: ts, Pictures: []Picture{{}}}
	caps := NewCapabilities(FormatMatroska, true,
		Capability{Write: AccessFull}, Capability{Write: AccessFull}, Capability{}, nil)

	items := ProjectTransfer(m, caps)
	for _, it := range items {
		if it.Disposition != Dropped || it.Reason != "destination is read-only" {
			t.Errorf("item %+v: want dropped/read-only", it)
		}
	}
	if r := (TransferReport{Items: items}); r.Lossless() {
		t.Error("a read-only destination cannot be lossless")
	}
}

// TestProjectTransferEmptyMetadata: a source with no canonical metadata yields no
// items (and so is trivially lossless).
func TestProjectTransferEmptyMetadata(t *testing.T) {
	m := &Media{Format: FormatFLAC}
	items := ProjectTransfer(m, NewCapabilities(FormatMP4, false,
		Capability{Write: AccessFull}, Capability{Write: AccessFull}, Capability{Write: AccessFull}, nil))
	if len(items) != 0 {
		t.Errorf("got %d items, want 0", len(items))
	}
	if !(TransferReport{Items: items}).Lossless() {
		t.Error("empty transfer should be lossless")
	}
}
