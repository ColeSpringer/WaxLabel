package waxlabel_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
)

// The Ogg FLAC mapping is the one Ogg codec whose cover art is a native FLAC PICTURE block rather
// than a METADATA_BLOCK_PICTURE comment, and the one whose identification packet carries a count of
// the header packets that follow.

// declaredHeaderPackets reads the identification packet's count of header packets following it.
func declaredHeaderPackets(t *testing.T, data []byte) int {
	t.Helper()
	if len(data) < 27 {
		t.Fatal("truncated Ogg stream")
	}
	body := 27 + int(data[26])
	if len(data) < body+9 {
		t.Fatal("truncated identification packet")
	}
	return int(binary.BigEndian.Uint16(data[body+7 : body+9]))
}

func TestOggFLACPreservesUnknownBlocks(t *testing.T) {
	marker := []byte("APPLICATION-PRESERVE-ME-0xABCDEF")
	app := testfile.FLACBlock(2, append([]byte("WAXX"), marker...))
	comment := testfile.FLACBlock(4, vorbis.RenderCommentList("test", []vorbis.Comment{{Name: "TITLE", Value: "Keep"}}))
	data := testfile.OggFLAC(app, comment)

	doc := mustParseBytes(t, data)
	if doc.Format() != wl.FormatOggFLAC {
		t.Fatalf("format = %v, want Ogg FLAC", doc.Format())
	}
	if doc.Fields().Title != "Keep" {
		t.Fatalf("setup: Title = %q", doc.Fields().Title)
	}
	plan, err := doc.Edit().Set(tag.Title, "Changed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if !bytes.Contains(out, marker) {
		t.Error("APPLICATION block bytes were not preserved through a tag edit")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Changed" {
		t.Errorf("Title = %q, want Changed", re.Fields().Title)
	}
	if got := declaredHeaderPackets(t, out); got != 2 {
		t.Errorf("declared header packets = %d, want 2", got)
	}
	foundApp := false
	for _, e := range re.Native().Describe() {
		if e.Kind == "APPLICATION" {
			foundApp = true
		}
	}
	if !foundApp {
		t.Error("APPLICATION block missing from native view after edit")
	}
}

// mapping's one divergence from Vorbis and Opus: a cover is written as a FLAC PICTURE block, not a
// base64 METADATA_BLOCK_PICTURE comment, and the identification packet's header-packet count
// follows the block it added.
func TestOggFLACPictureIsNativeBlock(t *testing.T) {
	src := readFixture(t, notagsOggFLAC)
	before := declaredHeaderPackets(t, src)

	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)

	if bytes.Contains(out, []byte("METADATA_BLOCK_PICTURE")) {
		t.Error("cover was stored as a comment; Ogg FLAC must use a native PICTURE block")
	}
	if got := declaredHeaderPackets(t, out); got != before+1 {
		t.Errorf("declared header packets = %d, want %d", got, before+1)
	}
	re := mustParseBytes(t, out)
	if pics := re.Pictures(); len(pics) != 1 || pics[0].Type != wl.PicFrontCover || !bytes.Equal(pics[0].Data, testfile.PNG()) {
		t.Fatalf("expected one front cover with the stored bytes, got %+v", pics)
	}
	seenPicture := false
	for _, e := range re.Native().Describe() {
		if e.Kind == "PICTURE" {
			seenPicture = true
		}
	}
	if !seenPicture {
		t.Error("no PICTURE block in the native view after adding a cover")
	}

	// And back out again: the count must shrink with the block.
	plan2, err := re.Edit().ClearPictures().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, out, plan2)
	if got := declaredHeaderPackets(t, out2); got != before {
		t.Errorf("after clearing pictures, declared header packets = %d, want %d", got, before)
	}
	if pics := mustParseBytes(t, out2).Pictures(); len(pics) != 0 {
		t.Errorf("ClearPictures left %d pictures", len(pics))
	}
}

// cross-form file: some encoders put a METADATA_BLOCK_PICTURE comment in an Ogg FLAC stream. It
// must read as a cover, and a tag-only edit (which re-renders the comment block and so strips the
// entry) must materialize it as a native PICTURE block.
func TestOggFLACCommentPictureMaterialized(t *testing.T) {
	pic := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()}
	comment := testfile.FLACBlock(4, vorbis.RenderCommentList("test", []vorbis.Comment{
		{Name: "TITLE", Value: "Keep"},
		{Name: vorbis.PictureComment, Value: commentPictureValue(pic)},
	}))
	data := testfile.OggFLAC(comment)

	doc := mustParseBytes(t, data)
	if len(doc.Pictures()) != 1 {
		t.Fatalf("setup: expected the comment cover to be read, got %+v", doc.Pictures())
	}
	plan, err := doc.Edit().Set(tag.Title, "Changed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	re := mustParseBytes(t, out)
	if len(re.Pictures()) != 1 {
		t.Fatalf("cover lost across a tag-only edit: %+v", re.Pictures())
	}
	if bytes.Contains(out, []byte("METADATA_BLOCK_PICTURE")) {
		t.Error("the stale picture comment survived; it must be replaced by a native block")
	}
	if got := declaredHeaderPackets(t, out); got != 2 {
		t.Errorf("declared header packets = %d, want 2 (comment + materialized picture)", got)
	}
}

// create-a-comment-block path: the mapping requires one, but a stream missing it is read rather
// than refused.
func TestOggFLACWithoutCommentBlock(t *testing.T) {
	data := testfile.OggFLAC(testfile.FLACBlock(1, make([]byte, 8)))

	doc := mustParseBytes(t, data)
	if doc.Tags().Len() != 0 {
		t.Fatalf("setup: expected no tags, got %d", doc.Tags().Len())
	}
	plan, err := doc.Edit().Set(tag.Title, "Created").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	re := mustParseBytes(t, out)
	if re.Fields().Title != "Created" {
		t.Errorf("Title = %q, want Created", re.Fields().Title)
	}
	// The PADDING block is dropped (Ogg re-paginates the header region on every
	// rewrite, so padding buys nothing), leaving just the new comment block.
	if got := declaredHeaderPackets(t, out); got != 1 {
		t.Errorf("declared header packets = %d, want 1", got)
	}
}
