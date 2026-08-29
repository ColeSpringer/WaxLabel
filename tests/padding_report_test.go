package waxlabel_test

import (
	"encoding/binary"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
)

// TestOggFLACPaddingBlockNotReported pins a deliberate exclusion: rebuildFLACBlocks drops a
// PADDING block on every rewrite (Ogg re-paginates the header region), so reporting it as
// padding would promise slack that no write grows into and that the next edit deletes. The
// native view still lists the block, which is where it belongs.
func TestOggFLACPaddingBlockNotReported(t *testing.T) {
	padding := append([]byte{1}, make([]byte, 4096)...)
	comment := append([]byte{4}, vorbis.RenderCommentList("test", []vorbis.Comment{{Name: "TITLE", Value: "T"}})...)
	doc := mustParseBytes(t, synthOggFLAC(padding, comment))

	found := false
	for _, e := range doc.Native().Describe() {
		if e.Kind == "PADDING" {
			found = true
		}
	}
	if !found {
		t.Fatal("setup: the PADDING block should be in the native view")
	}
	if got := doc.Padding(); got != 0 {
		t.Errorf("Padding() = %d, want 0: an Ogg FLAC PADDING block is dropped by the next rewrite", got)
	}
}

// mp4LargesizeFree builds a free atom in the 64-bit largesize form: a 32-bit size of 1, the
// name, then the real 8-byte size, giving a 16-byte header.
func mp4LargesizeFree(payload int) []byte {
	total := 16 + payload
	b := []byte{0, 0, 0, 1}
	b = append(b, "free"...)
	b = binary.BigEndian.AppendUint64(b, uint64(total))
	return append(b, make([]byte, payload)...)
}

// TestMP4PaddingHonorsTheWriterHeaderWidth: a rewrite replaces the region with a freshly
// rendered free atom, which always uses the 8-byte header. A source atom in the 64-bit
// largesize form therefore yields 8 more usable bytes than its own payload, and reporting
// the payload would make Padding() jump by 8 across an otherwise in-place save-back.
func TestMP4PaddingHonorsTheWriterHeaderWidth(t *testing.T) {
	const payload = 64
	data := mp4Assemble(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "Original Title")), mp4LargesizeFree(payload))
	doc := mustParseBytes(t, data)
	if got, want := doc.Padding(), int64(payload+8); got != want {
		t.Fatalf("Padding() = %d, want %d (the 16-byte header collapses to 8 on rewrite)", got, want)
	}

	// An equal-length edit reuses the region, so the plan reports the same figure and a
	// re-read of the written file reports it again: no jump across the save-back.
	plan, err := doc.Edit().Set(tag.Title, "Original Titl2").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Report().PaddingAfter; got != doc.Padding() {
		t.Errorf("plan padding = %d, dump padding = %d", got, doc.Padding())
	}
	if got := mustParseBytes(t, applyToBytes(t, data, plan)).Padding(); got != doc.Padding() {
		t.Errorf("padding after the save-back = %d, want the unchanged %d", got, doc.Padding())
	}
}

// TestMP4ChaptersOnlyEditReportsSurvivingPadding: a chapters-only edit does not rewrite the
// ilst region, so the file's free atom survives verbatim. Reporting no padding would tell
// the user a region vanished that the write does not touch.
func TestMP4ChaptersOnlyEditReportsSurvivingPadding(t *testing.T) {
	data := mp4Assemble(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T")), mp4Atom("free", make([]byte, 512)))
	doc := mustParseBytes(t, data)
	if doc.Padding() == 0 {
		t.Fatal("setup: the fixture should carry a free atom")
	}
	plan, err := doc.Edit().SetChapters(wl.Chapter{Start: 0, Title: "One"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Report().PaddingAfter; got != doc.Padding() {
		t.Errorf("chapters-only plan padding = %d, want the surviving %d", got, doc.Padding())
	}
}
