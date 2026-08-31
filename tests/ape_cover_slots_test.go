package waxlabel_test

import (
	"bytes"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// APEv2 item names are unique within a tag, and the Cover Art convention has exactly
// two of them, so a picture set is resolved onto those two slots: exact front and back
// roles keep their own name, any other role takes a free name, an added picture claims
// a slot from a pre-existing same-role one, and only a picture left with no name at all
// is dropped. These tests pin that surface end to end: the editor's resolution and
// warnings, the transfer report's grades, and a written file that lints clean.

// TestWavPackCoverSlotWrite: front + artist + band cannot all fit in two items. The
// front keeps its name, the artist takes the free back name (role lost, image kept),
// the band picture has no name left and is dropped with a warning, and the result
// carries no colliding art for lint to flag.
func TestWavPackCoverSlotWrite(t *testing.T) {
	src := readFixture(t, notagsWV)
	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: tinyJPEG()}).
		AddPicture(wl.Picture{Type: wl.PicBand, Data: tinyGIF()}).
		Prepare(wl.WithAllowUnsupportedDrop())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !planWarns(t, plan, wl.WarnPictureUnsupported) {
		t.Errorf("plan warnings = %+v, want picture-unsupported for the slotless band picture", plan.Report().Warnings)
	}
	if !planWarns(t, plan, wl.WarnPictureMetadataDropped) {
		t.Errorf("plan warnings = %+v, want picture-metadata-dropped for the artist role", plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)

	doc := mustParseBytes(t, out)
	pics := doc.Pictures()
	if len(pics) != 2 || pics[0].Type != wl.PicFrontCover || pics[1].Type != wl.PicBackCover {
		t.Fatalf("pictures = %+v, want the front cover plus the artist stored under the back name", pics)
	}
	if !bytes.Equal(pics[0].Data, tinyPNG()) || !bytes.Equal(pics[1].Data, tinyJPEG()) {
		t.Error("written cover bytes do not match the added images")
	}
	for _, f := range doc.Lint() {
		if f.Code == "multiple-front-covers" || f.Code == "duplicate-picture" {
			t.Errorf("lint finding %q on the written file; the writer emitted colliding cover items", f.Code)
		}
	}
}

// TestWavPackSlotlessPictureRefusedWithoutDropOption: for a library caller the slotless
// picture is refused like an unrepresentable cover format; WithAllowUnsupportedDrop
// (which the CLI always passes) turns the refusal into the warned drop above.
func TestWavPackSlotlessPictureRefusedWithoutDropOption(t *testing.T) {
	src := readFixture(t, notagsWV)
	_, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: tinyJPEG()}).
		AddPicture(wl.Picture{Type: wl.PicBand, Data: tinyGIF()}).
		Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("Prepare err = %v, want ErrUnsupportedTag for a slotless picture without the drop option", err)
	}
}

// TestWavPackAddedFrontReplacesExisting: the edit targets the slot, so an added front
// cover replaces the file's existing front rather than losing to it, and the
// replacement of pre-existing art is warned. No drop option is needed: the added
// picture is stored.
func TestWavPackAddedFrontReplacesExisting(t *testing.T) {
	src := readFixture(t, notagsWV)
	seed, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	fronted := applyToBytes(t, src, seed)

	plan, err := mustParseBytes(t, fronted).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyJPEG()}).Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if plan.IsNoOp() {
		t.Error("adding a front cover over an existing one must write, not collapse to a no-op")
	}
	if !planWarns(t, plan, wl.WarnPictureUnsupported) {
		t.Errorf("plan warnings = %+v, want a warning for the replaced pre-existing front", plan.Report().Warnings)
	}
	pics := mustParseBytes(t, applyToBytes(t, fronted, plan)).Pictures()
	if len(pics) != 1 || pics[0].Type != wl.PicFrontCover || !bytes.Equal(pics[0].Data, tinyJPEG()) {
		t.Fatalf("pictures = %+v, want only the newly added front cover", pics)
	}
}

// TestWavPackCoverSlotNoOpKeepsWarning: with both slots already held, an added picture
// that resolves away entirely changes nothing on disk, but the loss must survive the
// no-op collapse so --strict still catches it.
func TestWavPackCoverSlotNoOpKeepsWarning(t *testing.T) {
	src := readFixture(t, notagsWV)
	seed, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).
		AddPicture(wl.Picture{Type: wl.PicBackCover, Data: tinyJPEG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	full := applyToBytes(t, src, seed)

	plan, err := mustParseBytes(t, full).Edit().
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: tinyGIF()}).Prepare(wl.WithAllowUnsupportedDrop())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Error("an edit whose only picture had no slot should collapse to a no-op")
	}
	if !planWarns(t, plan, wl.WarnPictureUnsupported) {
		t.Errorf("no-op warnings = %+v, want the picture-unsupported drop preserved", plan.Report().Warnings)
	}
}

// TestTransferCoverSlotReport: copying a FLAC carrying front + artist + band pictures
// into WavPack reports the front carried, the artist stored with reduced fidelity (its
// role becomes the back cover), and the band picture dropped for want of a name; the
// written destination holds exactly those two covers.
func TestTransferCoverSlotReport(t *testing.T) {
	flacSrc := readFixture(t, sampleFLAC)
	seed, err := mustParseBytes(t, flacSrc).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: tinyJPEG()}).
		AddPicture(wl.Picture{Type: wl.PicBand, Data: tinyGIF()}).
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	src := mustParseBytes(t, applyToBytes(t, flacSrc, seed))

	report, err := src.PlanTransfer(wl.FormatWavPack)
	if err != nil {
		t.Fatalf("PlanTransfer: %v", err)
	}
	if carried, lossy, dropped := pictureCounts(report); carried != 1 || lossy != 1 || dropped != 1 {
		t.Errorf("picture grades = %d carried %d lossy %d dropped, want 1/1/1\nreport: %+v",
			carried, lossy, dropped, report.Items)
	}

	dstBytes := readFixture(t, notagsWV)
	dst := mustParseBytes(t, dstBytes)
	plan, report2, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if carried, lossy, dropped := pictureCounts(report2); carried != 1 || lossy != 1 || dropped != 1 {
		t.Errorf("prepare picture grades = %d carried %d lossy %d dropped, want 1/1/1", carried, lossy, dropped)
	}
	out := applyToBytes(t, dstBytes, plan)
	pics := mustParseBytes(t, out).Pictures()
	if len(pics) != 2 || pics[0].Type != wl.PicFrontCover || pics[1].Type != wl.PicBackCover {
		t.Fatalf("transferred pictures = %+v, want the front plus the artist under the back name", pics)
	}
	if !bytes.Equal(pics[0].Data, tinyPNG()) || !bytes.Equal(pics[1].Data, tinyJPEG()) {
		t.Error("transferred cover bytes do not match the source images")
	}
}

// pictureCounts tallies the picture items of a transfer report by disposition.
func pictureCounts(r wl.TransferReport) (carried, lossy, dropped int) {
	for _, it := range r.Items {
		if it.Kind != wl.TransferPicture {
			continue
		}
		switch it.Disposition {
		case wl.Carried:
			carried += it.Count
		case wl.Lossy:
			lossy += it.Count
		case wl.Dropped:
			dropped += it.Count
		}
	}
	return carried, lossy, dropped
}
