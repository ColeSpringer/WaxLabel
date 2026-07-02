package waxlabel_test

import (
	"context"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// TestMatroskaNonFrontCoverCopyIdempotent is the M6 regression: a picture whose role Matroska
// reduces to Other must not re-trigger an attachment rewrite on every copy. detectChanges now
// compares base against the reprojected edited set, so re-applying an already-stored non-front
// cover (same bytes) is a true no-op instead of churning a fresh random FileUID each time.
func TestMatroskaNonFrontCoverCopyIdempotent(t *testing.T) {
	png := tinyPNG()
	cover := mkEl(idAttachments, mkEl(idAttached, concat(
		mkStr(idFileName, "small_cover.png"), // stored role: reads back as Other
		mkStr(idFileMime, "image/png"),
		mkEl(idFileData, png),
	)))
	data := buildMatroska("matroska", "M6", cover)
	doc := mustParseBytes(t, data)
	if pics := doc.Pictures(); len(pics) != 1 || pics[0].Type != wl.PicOther {
		t.Fatalf("setup: expected 1 Other-role cover, got %+v", doc.Pictures())
	}
	// Replace it with a back-cover role carrying the same bytes - what a cross-format copy
	// yields (an MP3/FLAC back cover Matroska cannot represent, so it reduces to Other).
	plan, err := doc.Edit().
		RemovePictures(func(wl.Picture) bool { return true }).
		AddPicture(wl.Picture{Type: wl.PicBackCover, Data: slices.Clone(png)}).
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Errorf("re-applying an already-stored non-front cover must be a no-op; operations: %v", plan.Report().Operations)
	}
}

// TestMatroskaForceNonImageReportsNoPicture is the L15 regression: a non-image embedded under
// --force (WithUnrecognizedPictures) is stored as a plain AttachedFile, not a cover, so it reads
// back as 0 pictures. The result view and a fresh reparse must agree on 0 pictures (not the
// "+ pictures: 1" the raw edited set implies), and the plan must warn that it is not a cover.
func TestMatroskaForceNonImageReportsNoPicture(t *testing.T) {
	data := buildMatroska("matroska", "L15", nil)
	nonImage := wl.Picture{Type: wl.PicFrontCover, MIME: "application/octet-stream", Data: []byte("plain file, not an image")}
	plan, err := mustParseBytes(t, data).Edit().AddPicture(nonImage).Prepare(wl.WithUnrecognizedPictures())
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnPictureMetadataDropped) {
		t.Errorf("a forced non-image cover should warn picture-metadata-dropped; warnings: %v", plan.Report().Warnings)
	}
	var w writerTo
	res, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res.Pictures()); n != 0 {
		t.Errorf("result view reports %d pictures, want 0 (a non-image is a plain attachment, not a cover)", n)
	}
	if n := len(mustParseBytes(t, w.b).Pictures()); n != 0 {
		t.Errorf("reparse reports %d pictures, want 0 (the non-image round-trips as an attachment)", n)
	}
}
