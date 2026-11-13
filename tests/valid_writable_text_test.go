package waxlabel_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// public single-source validator behind both the editor's authored-text rejection and the CLI's
// boundary check: clean text passes, a NUL byte (valid UTF-8 U+0000, the case a UTF-8-only check
// would miss) is rejected, and invalid UTF-8 is rejected; both wrapping ErrInvalidData.
func TestValidWritableText(t *testing.T) {
	for _, s := range []string{"", "hello", "Ünîçödé ✓", "multi\nline"} {
		if err := wl.ValidWritableText(s); err != nil {
			t.Errorf("ValidWritableText(%q) = %v, want nil", s, err)
		}
	}
	if err := wl.ValidWritableText("a\x00b"); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("ValidWritableText(NUL) = %v, want ErrInvalidData", err)
	}
	if err := wl.ValidWritableText("a\xffb"); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("ValidWritableText(invalid UTF-8) = %v, want ErrInvalidData", err)
	}
}

// exported bare-phrase helper the CLI boundary reads directly (instead of parsing
// ValidWritableText's error string): clean text yields "", and the NUL and invalid-UTF-8 cases
// yield their exact phrases.
func TestWritableTextReason(t *testing.T) {
	if r := wl.WritableTextReason("clean"); r != "" {
		t.Errorf(`WritableTextReason("clean") = %q, want ""`, r)
	}
	if r := wl.WritableTextReason("a\x00b"); r != "contains a NUL byte" {
		t.Errorf("WritableTextReason(NUL) = %q, want %q", r, "contains a NUL byte")
	}
	if r := wl.WritableTextReason("a\xffb"); r != "contains invalid UTF-8" {
		t.Errorf("WritableTextReason(invalid UTF-8) = %q, want %q", r, "contains invalid UTF-8")
	}
}

// An edit is held to the text it authors. A chapter or lyric set the file already holds with a
// NUL in it does not refuse an edit that adds beside it; one the edit brings does.
func TestEditJudgesOnlyTheChaptersAndLyricsItAuthors(t *testing.T) {
	data := testfile.TaggedFLAC("v",
		"CHAPTER001=00:00:00.000", "CHAPTER001NAME=Intro\x00", "CHAPTER002=00:00:01.000", "CHAPTER002NAME=Verse",
		"SYNCEDLYRICS=[00:01.00]held\x00")
	doc := mustParseBytes(t, data)
	held := doc.Chapters()
	if len(held) != 2 || !strings.Contains(held[0].Title, "\x00") {
		t.Fatalf("setup: chapters = %+v, want the first title holding a NUL", held)
	}
	lyrics := doc.SyncedLyrics()
	if len(lyrics) != 1 || !strings.Contains(lyrics[0].Lines[0].Text, "\x00") {
		t.Fatalf("setup: synced lyrics = %+v, want a line holding a NUL", lyrics)
	}

	added := append(slices.Clone(held), wl.Chapter{Start: 2 * time.Second, Title: "Chorus"})
	if _, err := doc.Edit().SetChapters(added...).Prepare(); err != nil {
		t.Errorf("a chapter added beside the file's own: %v", err)
	}
	bad := append(slices.Clone(held), wl.Chapter{Start: 2 * time.Second, Title: "Cho\x00rus"})
	if _, err := doc.Edit().SetChapters(bad...).Prepare(); !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "chapter title") {
		t.Errorf("an added chapter title with a NUL: err = %v, want it refused by name", err)
	}

	second := wl.SyncedLyrics{Language: "deu", Lines: []wl.SyncedLine{{Time: time.Second, Text: "neu"}}}
	if _, err := doc.Edit().SetSyncedLyrics(lyrics[0], second).Prepare(wl.WithAllowUnsupportedDrop()); err != nil &&
		strings.Contains(err.Error(), "NUL") {
		t.Errorf("a lyric set added beside the file's own: %v", err)
	}
	second.Lines[0].Text = "n\x00eu"
	if _, err := doc.Edit().SetSyncedLyrics(lyrics[0], second).Prepare(); !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "synced-lyrics line") {
		t.Errorf("an added lyric line with a NUL: err = %v, want it refused by name", err)
	}

	// A tag edit beside them is untouched by either, and an edit of the key that holds a NUL
	// still names it.
	nul := mustParseBytes(t, testfile.TaggedFLAC("v", "TITLE=abc\x00", "ARTIST=A"))
	if _, err := nul.Edit().Add(tag.Artist, "second").Prepare(); err != nil {
		t.Errorf("--add ARTIST beside a TITLE holding a NUL: %v", err)
	}
	if _, err := nul.Edit().Add(tag.Title, "second").Prepare(); !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), `"TITLE"`) {
		t.Errorf("--add TITLE onto a TITLE holding a NUL: err = %v, want TITLE named", err)
	}
}
