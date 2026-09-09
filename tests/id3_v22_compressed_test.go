package waxlabel_test

import (
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestMP3V22CompressedTagIgnoredAndWarned: the tag is invisible to the projection (as it is
// to ffprobe), the parse says so, and a rewrite that replaces the unreadable region says so
// too, so --strict can refuse it.
func TestMP3V22CompressedTagIgnoredAndWarned(t *testing.T) {
	tagBytes := id3v2(2, frame22("TT2", []byte("\x00Compressed")))
	tagBytes[5] = 0x40
	file := slices.Concat(tagBytes, mp3Audio(t))
	doc := mustParseBytes(t, file)
	if _, ok := doc.Get(tag.Title); ok {
		t.Error("a compressed v2.2 tag must not project")
	}
	if !hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("parse should warn malformed-tag-entry: %v", doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Album, "Z").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("rewrite should warn malformed-tag-entry-dropped: %v", plan.Report().Warnings)
	}
	re := mustParseBytes(t, applyToBytes(t, file, plan))
	if re.Fields().Album != "Z" || hasWarning(re, wl.WarnMalformedTagEntry) {
		t.Errorf("rewritten file: album=%q warnings=%v", re.Fields().Album, re.Warnings())
	}
}
