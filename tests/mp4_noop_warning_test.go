package waxlabel_test

import (
	"context"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// TestMP4PictureMetadataWarningSurvivesNoOp (review): adding a description to an existing
// MP4 cover with no other change is a no-op write - the covr atom stores image data only,
// so the description is dropped and the result round-trips to base - but the
// picture-metadata-dropped warning must still surface rather than vanish behind "no
// changes". The MP4 no-op downgrade now carries every input-rejection warning forward
// (value-dropped and picture-metadata-dropped), not just value-dropped.
func TestMP4PictureMetadataWarningSurvivesNoOp(t *testing.T) {
	ctx := context.Background()

	// Build an m4a carrying a plain front cover (no description).
	base := readFixture(t, "../testdata/notags.m4a")
	withCoverPlan, err := mustParseBytes(t, base).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatalf("setup prepare: %v", err)
	}
	var buf writerTo
	if _, _, err := withCoverPlan.Execute(ctx, wl.WriteTo(&buf, wl.BytesSource(base))); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	withCover := mustParseBytes(t, buf.b)
	if len(withCover.Pictures()) != 1 {
		t.Fatalf("setup: want 1 cover, got %d", len(withCover.Pictures()))
	}

	// Re-add the same cover image WITH a description and no tag change: the covr drops the
	// description, so the result equals base and the plan downgrades to a no-op.
	pic := withCover.Pictures()[0]
	pic.Description = "liner notes"
	noop, err := withCover.Edit().ClearPictures().AddPicture(pic).Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !noop.IsNoOp() {
		t.Fatalf("expected a no-op (the description is dropped, the image is unchanged), got a real write")
	}
	found := false
	for _, w := range noop.Report().Warnings {
		if w.Code == wl.WarnPictureMetadataDropped {
			found = true
		}
	}
	if !found {
		t.Errorf("picture-metadata-dropped warning must survive the no-op downgrade; got %v", noop.Report().Warnings)
	}
}
