package waxlabel_test

import (
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// (Fix 6): MP4's covr atom stores image bytes only, so adding a front cover that carries a
// description drops the description on write.
func TestMP4DescribedCoverWarnsOnWrite(t *testing.T) {
	base := readFixture(t, "../testdata/notags.m4a")
	plan, err := mustParseBytes(t, base).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG(), Description: "liner notes"}).
		Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if plan.IsNoOp() {
		t.Fatal("adding a new cover image is a real write, not a no-op")
	}
	if !reportHasWarning(plan.Report().Warnings, wl.WarnPictureMetadataDropped) {
		t.Errorf("a described front cover must warn picture-metadata-dropped on an MP4 write; got %v",
			plan.Report().Warnings)
	}
}

// (Fix 6): the realistic scenario the original QA report flagged; copying a described cover from
// another file ONTO an MP4.
func TestMP4DescribedCoverWarnsOnTransfer(t *testing.T) {
	srcBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG(), Description: "liner notes"})
	})
	src := mustParseBytes(t, srcBytes)
	dstBytes := readFixture(t, "../testdata/notags.m4a")
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	// The write plan warns about the description the covr cannot store.
	if !reportHasWarning(plan.Report().Warnings, wl.WarnPictureMetadataDropped) {
		t.Errorf("a transferred described cover must warn picture-metadata-dropped; got %v",
			plan.Report().Warnings)
	}
	// The transfer report grades the picture Lossy (its description does not survive MP4).
	var pic wl.TransferItem
	for _, it := range report.Items {
		if it.Kind == wl.TransferPicture {
			pic = it
		}
	}
	if pic.Disposition != wl.Lossy {
		t.Errorf("described cover transferred to MP4 = %v, want Lossy (description dropped)", pic.Disposition)
	}
}
