package waxlabel_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
)

// flacWithCommentBlock builds a FLAC: STREAMINFO, any native PICTURE blocks, a VORBIS_COMMENT
// block carrying the given comments, then a last PADDING block and a frame sync so audio is
// detected. It lets a test plant a base64 METADATA_BLOCK_PICTURE comment alongside (or instead
// of) native covers.
func flacWithCommentBlock(comments []vorbis.Comment, nativePics ...wl.Picture) []byte {
	return flacWithCommentBlockVendor("test", comments, nativePics...)
}

// flacWithCommentBlockVendor is flacWithCommentBlock with a caller-chosen Vorbis vendor string,
// so a test can plant a transcoder stamp (e.g. "Lavf...") and exercise --strip-encoder.
func flacWithCommentBlockVendor(vendor string, comments []vorbis.Comment, nativePics ...wl.Picture) []byte {
	out := []byte("fLaC")
	out = append(out, flacBlock(0, false, validStreamInfo())...) // STREAMINFO (first)
	for _, np := range nativePics {
		out = append(out, flacBlock(6, false, vorbis.RenderPicture(np))...) // PICTURE (block type 6)
	}
	out = append(out, flacBlock(4, false, vorbis.RenderCommentList(vendor, comments))...) // VORBIS_COMMENT
	out = append(out, flacBlock(1, true, make([]byte, 4))...)                             // PADDING (last)
	return append(out, 0xFF, 0xF8)                                                        // frame sync
}

// commentPictureValue renders p as the base64 METADATA_BLOCK_PICTURE comment value (the Ogg
// cover-art form some encoders also use in FLAC).
func commentPictureValue(p wl.Picture) string {
	return base64.StdEncoding.EncodeToString(vorbis.RenderPicture(p))
}

// flacCommentPictureSeed is a compact FLAC carrying a base64 METADATA_BLOCK_PICTURE comment,
// used as a FuzzParse regression seed for the comment-cover decode/materialize path.
func flacCommentPictureSeed() []byte {
	return flacWithCommentBlock([]vorbis.Comment{
		{Name: vorbis.PictureComment, Value: commentPictureValue(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()})},
		{Name: "TITLE", Value: "x"},
	})
}

// TestFLACCommentPictureVisibleAndMaterializes checks that a cover stored only as a base64
// METADATA_BLOCK_PICTURE comment must be visible on read, and a tag edit (pictures untouched)
// must keep it - canonicalized into exactly one native PICTURE block, with the now-stale
// picture comment dropped (not duplicated, not lost).
func TestFLACCommentPictureVisibleAndMaterializes(t *testing.T) {
	pic := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}
	src := flacWithCommentBlock([]vorbis.Comment{
		{Name: vorbis.PictureComment, Value: commentPictureValue(pic)},
		{Name: "TITLE", Value: "Old"},
	})

	pics := mustParseBytes(t, src).Pictures()
	if len(pics) != 1 {
		t.Fatalf("comment-embedded cover not decoded: got %d pictures, want 1", len(pics))
	}
	if !bytes.Equal(pics[0].Data, tinyPNG()) {
		t.Error("decoded comment cover data differs from the original")
	}

	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)

	if bytes.Contains(out, []byte(vorbis.PictureComment)) {
		t.Error("METADATA_BLOCK_PICTURE comment survived a tag edit; it must be materialized into a native block")
	}
	re := mustParseBytes(t, out)
	rp := re.Pictures()
	if len(rp) != 1 {
		t.Fatalf("after tag edit: got %d pictures, want exactly 1 (cover dropped or duplicated)", len(rp))
	}
	if !bytes.Equal(rp[0].Data, tinyPNG()) {
		t.Error("materialized cover data differs from the original")
	}
	if re.Fields().Title != "New" {
		t.Errorf("title after edit = %q, want New", re.Fields().Title)
	}
}

// TestFLACMixedNativeAndCommentPictures covers the mixed case: a file with both a native
// PICTURE block and a comment-embedded cover must read two distinct pictures, and a tag edit
// must yield exactly two (the native block kept verbatim, the comment cover materialized once)
// with no leftover METADATA_BLOCK_PICTURE comment - the easy duplication/loss regression.
func TestFLACMixedNativeAndCommentPictures(t *testing.T) {
	commentPic := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}
	nativePic := wl.Picture{Type: wl.PicBackCover, MIME: "image/jpeg", Data: tinyJPEG()}
	src := flacWithCommentBlock([]vorbis.Comment{
		{Name: vorbis.PictureComment, Value: commentPictureValue(commentPic)},
		{Name: "TITLE", Value: "Old"},
	}, nativePic)

	if pics := mustParseBytes(t, src).Pictures(); len(pics) != 2 {
		t.Fatalf("mixed native+comment: got %d pictures, want 2", len(pics))
	}

	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)

	if bytes.Contains(out, []byte(vorbis.PictureComment)) {
		t.Error("METADATA_BLOCK_PICTURE comment survived in the mixed case; it must be materialized")
	}
	pics := mustParseBytes(t, out).Pictures()
	if len(pics) != 2 {
		t.Fatalf("after tag edit: got %d pictures, want exactly 2 (no duplication, none dropped)", len(pics))
	}
	var haveNative, haveComment bool
	for _, p := range pics {
		haveNative = haveNative || bytes.Equal(p.Data, tinyJPEG())
		haveComment = haveComment || bytes.Equal(p.Data, tinyPNG())
	}
	if !haveNative || !haveComment {
		t.Errorf("mixed pictures not both preserved: native(jpeg)=%v comment(png)=%v", haveNative, haveComment)
	}
}

// TestFLACCommentPicturePictureEditNoDuplicate covers the picture-only-edit case: adding a
// cover to a file whose original cover is a METADATA_BLOCK_PICTURE comment must drop that stale
// comment (it is re-emitted as a native block), not leave it behind to duplicate the original.
func TestFLACCommentPicturePictureEditNoDuplicate(t *testing.T) {
	commentPic := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}
	src := flacWithCommentBlock([]vorbis.Comment{
		{Name: vorbis.PictureComment, Value: commentPictureValue(commentPic)},
		{Name: "TITLE", Value: "Keep"},
	})
	added := wl.Picture{Type: wl.PicBackCover, MIME: "image/jpeg", Data: tinyJPEG()}

	plan, err := mustParseBytes(t, src).Edit().AddPicture(added).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)

	if bytes.Contains(out, []byte(vorbis.PictureComment)) {
		t.Error("a picture edit left the stale METADATA_BLOCK_PICTURE comment, duplicating the original cover")
	}
	pics := mustParseBytes(t, out).Pictures()
	if len(pics) != 2 {
		t.Fatalf("after a picture add: got %d pictures, want exactly 2 (original cover + added, no duplicate)", len(pics))
	}
	pngCount := 0
	for _, p := range pics {
		if bytes.Equal(p.Data, tinyPNG()) {
			pngCount++
		}
	}
	if pngCount != 1 {
		t.Errorf("original comment cover appears %d times, want exactly 1 (no duplication)", pngCount)
	}
	if mustParseBytes(t, out).Fields().Title != "Keep" {
		t.Error("tag lost on a picture edit")
	}
}

// TestFLACCommentPictureSurvivesVendorStrip covers the vendor-only-edit case: --strip-encoder
// re-renders the comment block (dropping the stripped METADATA_BLOCK_PICTURE comment) even though
// no tag/chapter/picture changed, so the comment-sourced cover must still be materialized into a
// native block rather than silently lost. Regression for the materialization trigger set, which
// must mirror the comment-block re-render condition (commentsChanged || vendorChanged).
func TestFLACCommentPictureSurvivesVendorStrip(t *testing.T) {
	pic := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}
	src := flacWithCommentBlockVendor("Lavf58.76.100", []vorbis.Comment{
		{Name: vorbis.PictureComment, Value: commentPictureValue(pic)},
		{Name: "TITLE", Value: "Song"},
	})

	if pics := mustParseBytes(t, src).Pictures(); len(pics) != 1 {
		t.Fatalf("setup: comment cover not decoded: got %d pictures, want 1", len(pics))
	}

	plan, err := mustParseBytes(t, src).Edit().Prepare(wl.WithStripEncoderStamp())
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("setup: --strip-encoder on a transcoder-stamped FLAC must not be a no-op")
	}
	out := applyToBytes(t, src, plan)

	if bytes.Contains(out, []byte(vorbis.PictureComment)) {
		t.Error("METADATA_BLOCK_PICTURE comment survived a vendor strip; it must be materialized into a native block")
	}
	pics := mustParseBytes(t, out).Pictures()
	if len(pics) != 1 {
		t.Fatalf("comment cover lost on a vendor-only edit: got %d pictures, want exactly 1", len(pics))
	}
	if !bytes.Equal(pics[0].Data, tinyPNG()) {
		t.Error("materialized cover data differs from the original")
	}
}

// TestFLACCommentPictureOutOfRangeTypeMaterializes covers the caveat: a comment cover
// whose on-disk type is out of the single-byte range is clamped to PicOther on read, and
// materializing it re-renders from the parsed struct - so the round-trip asserts the cover's
// presence and image data, not byte-identity of the original comment bytes.
func TestFLACCommentPictureOutOfRangeTypeMaterializes(t *testing.T) {
	body := vorbis.RenderPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()})
	binary.BigEndian.PutUint32(body[0:4], 259) // type past the single-byte ID3/FLAC range
	src := flacWithCommentBlock([]vorbis.Comment{
		{Name: vorbis.PictureComment, Value: base64.StdEncoding.EncodeToString(body)},
		{Name: "TITLE", Value: "Old"},
	})

	pics := mustParseBytes(t, src).Pictures()
	if len(pics) != 1 {
		t.Fatalf("out-of-range comment cover: got %d pictures, want 1", len(pics))
	}
	if pics[0].Type != wl.PicOther {
		t.Errorf("out-of-range type read as %v, want PicOther (clamped)", pics[0].Type)
	}
	if !bytes.Equal(pics[0].Data, tinyPNG()) {
		t.Error("comment cover image data corrupted by the clamp")
	}

	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	rp := mustParseBytes(t, applyToBytes(t, src, plan)).Pictures()
	if len(rp) != 1 || !bytes.Equal(rp[0].Data, tinyPNG()) {
		t.Errorf("materialized out-of-range cover: got %d pictures, data intact = %v (presence/identity, not byte-identity)",
			len(rp), len(rp) == 1 && bytes.Equal(rp[0].Data, tinyPNG()))
	}
}
