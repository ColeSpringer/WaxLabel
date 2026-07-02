package vorbis

import (
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestSyncedLyricsOwnership checks a SYNCEDLYRICS comment is owned by the synced-lyrics
// model: it does not appear in the generic tag projection, but it does project as synced
// lyrics.
func TestSyncedLyricsOwnership(t *testing.T) {
	comments := []Comment{
		{Name: "TITLE", Value: "Song"},
		{Name: "SYNCEDLYRICS", Value: "[00:01.000]Hello\n[00:02.000]World"},
	}
	ts, _ := Project(comments)
	if ts.Has(tag.Key("SYNCEDLYRICS")) {
		t.Error("SYNCEDLYRICS leaked into the generic tag view")
	}
	if v, _ := ts.First(tag.Title); v != "Song" {
		t.Errorf("TITLE = %q, want Song", v)
	}
	got := ProjectSyncedLyrics(comments)
	if len(got) != 1 || len(got[0].Lines) != 2 {
		t.Fatalf("ProjectSyncedLyrics = %+v, want one set with two lines", got)
	}
	// LRC drops the language and descriptor.
	if got[0].Language != "" || got[0].Description != "" {
		t.Errorf("expected lang/desc dropped, got %q/%q", got[0].Language, got[0].Description)
	}
	if got[0].Lines[1].Time != 2*time.Second || got[0].Lines[1].Text != "World" {
		t.Errorf("line1 = %+v", got[0].Lines[1])
	}
}

// TestSyncedLyricsProjectSkipsEmpty checks the projection scans past a malformed/empty
// SYNCEDLYRICS comment to a later valid one rather than bailing on the first.
func TestSyncedLyricsProjectSkipsEmpty(t *testing.T) {
	comments := []Comment{
		{Name: "SYNCEDLYRICS", Value: "[ti:metadata only]"},    // no timed line
		{Name: "SYNCEDLYRICS", Value: "[00:03.000]Real lyric"}, // the valid one
	}
	got := ProjectSyncedLyrics(comments)
	if len(got) != 1 || len(got[0].Lines) != 1 || got[0].Lines[0].Text != "Real lyric" {
		t.Fatalf("ProjectSyncedLyrics = %+v, want the second comment's line", got)
	}
}

// TestSyncedLyricsRebuildPreserves checks an unrelated tag edit preserves the SYNCEDLYRICS
// comment verbatim (it is not touched by the generic key diff).
func TestSyncedLyricsRebuildPreserves(t *testing.T) {
	orig := []Comment{
		{Name: "TITLE", Value: "Old"},
		{Name: "SYNCEDLYRICS", Value: "[00:01.000]Keep me"},
	}
	base := tag.NewTagSet()
	base.Add(tag.Title, "Old")
	edited := tag.NewTagSet()
	edited.Add(tag.Title, "New")
	// A title-only edit: chapters and synced lyrics unchanged.
	out, _ := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
	found := false
	for _, cm := range out {
		if cm.Name == "SYNCEDLYRICS" {
			found = true
			if cm.Value != "[00:01.000]Keep me" {
				t.Errorf("SYNCEDLYRICS value changed: %q", cm.Value)
			}
		}
	}
	if !found {
		t.Error("SYNCEDLYRICS dropped by an unrelated edit")
	}
}

// TestSyncedLyricsRebuildReplaces checks a synced-lyrics edit drops the source SYNCEDLYRICS
// and emits the edited set, while leaving unrelated tags untouched.
func TestSyncedLyricsRebuildReplaces(t *testing.T) {
	orig := []Comment{
		{Name: "TITLE", Value: "Song"},
		{Name: "SYNCEDLYRICS", Value: "[00:01.000]Old line"},
	}
	ts := tag.NewTagSet()
	ts.Add(tag.Title, "Song")
	newSet := []core.SyncedLyrics{{Lines: []core.SyncedLine{{Time: 5 * time.Second, Text: "New line"}}}}
	out, _ := Rebuild(orig, ts, map[tag.Key]bool{}, nil, false, newSet, true)

	var lyrics, titles int
	var lrcValue string
	for _, cm := range out {
		switch {
		case cm.Name == "SYNCEDLYRICS":
			lyrics++
			lrcValue = cm.Value
		case cm.Name == "TITLE":
			titles++
		}
	}
	if lyrics != 1 {
		t.Fatalf("got %d SYNCEDLYRICS comments, want exactly 1 (old dropped, new emitted)", lyrics)
	}
	if titles != 1 {
		t.Errorf("TITLE count = %d, want 1 (unrelated tag preserved)", titles)
	}
	if got := ProjectSyncedLyrics([]Comment{{Name: "SYNCEDLYRICS", Value: lrcValue}}); len(got) != 1 ||
		got[0].Lines[0].Text != "New line" || got[0].Lines[0].Time != 5*time.Second {
		t.Errorf("re-projected edited set = %+v", got)
	}
}

// TestSyncedLyricsClear checks a synced-lyrics clear (changed flag set, empty set) drops the
// SYNCEDLYRICS comment entirely.
func TestSyncedLyricsClear(t *testing.T) {
	orig := []Comment{{Name: "SYNCEDLYRICS", Value: "[00:01.000]x"}}
	out, _ := Rebuild(orig, tag.NewTagSet(), map[tag.Key]bool{}, nil, false, nil, true)
	for _, cm := range out {
		if isSyncedLyricsComment(cm.Name) {
			t.Errorf("SYNCEDLYRICS survived a clear: %+v", out)
		}
	}
}

// FuzzVorbisProjectSyncedLyrics asserts the SYNCEDLYRICS projection never panics on
// arbitrary comment values.
func FuzzVorbisProjectSyncedLyrics(f *testing.F) {
	f.Add("[00:01.000]line")
	f.Add("")
	f.Add("not lrc at all")
	f.Add("[offset:5][[[::..")
	f.Fuzz(func(t *testing.T, value string) {
		got := ProjectSyncedLyrics([]Comment{{Name: "SYNCEDLYRICS", Value: value}})
		for _, sl := range got {
			for _, ln := range sl.Lines {
				if ln.Time < 0 {
					t.Errorf("negative time %v from %q", ln.Time, value)
				}
			}
		}
	})
}
