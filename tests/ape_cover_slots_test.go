package waxlabel_test

import (
	"bytes"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/waxerr"
)

// APEv2 item names are unique within a tag, and the Cover Art convention names one item per
// picture role, so a picture set is resolved onto those names: each role keeps its own, an added
// picture takes the name from a picture the file held under it, and only a second picture of a
// role in one edit is left with no name.

// A front cover, an artist and a band picture are three items, each read back under its role.
func TestWavPackCoverRolesWrite(t *testing.T) {
	src := readFixture(t, notagsWV)
	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: testfile.JPEG()}).
		AddPicture(wl.Picture{Type: wl.PicBand, Data: testfile.GIF()}).
		Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for _, code := range []wl.WarningCode{wl.WarnPictureUnsupported, wl.WarnPictureMetadataDropped} {
		if planWarns(t, plan, code) {
			t.Errorf("plan warnings = %+v, want no %s: every role has a name", plan.Report().Warnings, code)
		}
	}
	out := applyToBytes(t, src, plan)
	for _, name := range []string{"Cover Art (Front)", "Cover Art (Artist)", "Cover Art (Band)"} {
		if !bytes.Contains(out, []byte(name+"\x00")) {
			t.Errorf("no %q item written", name)
		}
	}

	doc := mustParseBytes(t, out)
	pics := doc.Pictures()
	if len(pics) != 3 || pics[0].Type != wl.PicFrontCover || pics[1].Type != wl.PicArtist || pics[2].Type != wl.PicBand {
		t.Fatalf("pictures = %+v, want the front, artist and band pictures", pics)
	}
	if !bytes.Equal(pics[0].Data, testfile.PNG()) || !bytes.Equal(pics[1].Data, testfile.JPEG()) || !bytes.Equal(pics[2].Data, testfile.GIF()) {
		t.Error("written picture bytes do not match the added images")
	}
	for _, f := range doc.Lint() {
		if f.Code == "multiple-front-covers" || f.Code == "duplicate-picture" {
			t.Errorf("lint finding %q on the written file; the writer emitted colliding cover items", f.Code)
		}
	}
}

// A second picture of a role added in one edit has no name: dropped with a warning under
// WithAllowUnsupportedDrop (which the CLI always passes), refused without it.
func TestWavPackSecondPictureOfARole(t *testing.T) {
	src := readFixture(t, notagsWV)
	edit := func() *wl.Editor {
		return mustParseBytes(t, src).Edit().
			AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).
			AddPicture(wl.Picture{Type: wl.PicBand, Data: testfile.JPEG()}).
			AddPicture(wl.Picture{Type: wl.PicBand, Data: testfile.GIF()})
	}
	if _, err := edit().Prepare(); !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("Prepare err = %v, want ErrUnsupportedTag for a picture with no name left", err)
	}
	// One picture added twice in one edit is two pictures: the second has no name either.
	twice := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicBand, Data: testfile.JPEG()}).
		AddPicture(wl.Picture{Type: wl.PicBand, Data: testfile.JPEG()})
	if _, err := twice.Prepare(); !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("the same picture added twice: Prepare err = %v, want ErrUnsupportedTag", err)
	}
	plan, err := edit().Prepare(wl.WithAllowUnsupportedDrop())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !planWarns(t, plan, wl.WarnPictureUnsupported) {
		t.Errorf("plan warnings = %+v, want picture-unsupported for the second band picture", plan.Report().Warnings)
	}
	pics := mustParseBytes(t, applyToBytes(t, src, plan)).Pictures()
	if len(pics) != 2 || pics[1].Type != wl.PicBand || !bytes.Equal(pics[1].Data, testfile.JPEG()) {
		t.Fatalf("pictures = %+v, want the front and the first band picture", pics)
	}
}

// An edit targets the name, so an added front cover replaces the file's existing front, and the
// replacement is warned. No drop option is needed: the added picture is stored.
func TestWavPackAddedFrontReplacesExisting(t *testing.T) {
	src := readFixture(t, notagsWV)
	seed, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: testfile.GIF()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	fronted := applyToBytes(t, src, seed)

	plan, err := mustParseBytes(t, fronted).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.JPEG()}).Prepare()
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
	if len(pics) != 2 || pics[0].Type != wl.PicArtist || pics[1].Type != wl.PicFrontCover || !bytes.Equal(pics[1].Data, testfile.JPEG()) {
		t.Fatalf("pictures = %+v, want the artist picture and the newly added front cover", pics)
	}
}

// Adding the front cover the file already holds beside one that loses the name to it changes
// nothing on disk, but the loss must survive the no-op collapse so --strict still catches it.
func TestWavPackCoverNameNoOpKeepsWarning(t *testing.T) {
	src := readFixture(t, notagsWV)
	seed, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	full := applyToBytes(t, src, seed)

	plan, err := mustParseBytes(t, full).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.GIF()}).Prepare(wl.WithAllowUnsupportedDrop())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Error("an edit that leaves the file's own front cover in place should collapse to a no-op")
	}
	if !planWarns(t, plan, wl.WarnPictureUnsupported) {
		t.Errorf("no-op warnings = %+v, want the picture-unsupported drop preserved", plan.Report().Warnings)
	}
}

// Copying a FLAC carrying a front, an artist and two band pictures into WavPack carries the first
// three under their roles and drops the second band picture for want of a name; the written
// destination holds exactly those three.
func TestTransferCoverRoleReport(t *testing.T) {
	flacSrc := readFixture(t, sampleFLAC)
	seed, err := mustParseBytes(t, flacSrc).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: testfile.JPEG()}).
		AddPicture(wl.Picture{Type: wl.PicBand, Data: testfile.GIF()}).
		AddPicture(wl.Picture{Type: wl.PicBand, Data: append(testfile.PNG(), 0)}).
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	src := mustParseBytes(t, applyToBytes(t, flacSrc, seed))

	report, err := src.PlanTransfer(wl.FormatWavPack)
	if err != nil {
		t.Fatalf("PlanTransfer: %v", err)
	}
	if carried, lossy, dropped := pictureCounts(report); carried != 3 || lossy != 0 || dropped != 1 {
		t.Errorf("picture grades = %d carried %d lossy %d dropped, want 3/0/1\nreport: %+v",
			carried, lossy, dropped, report.Items)
	}

	dstBytes := readFixture(t, notagsWV)
	dst := mustParseBytes(t, dstBytes)
	plan, report2, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if carried, lossy, dropped := pictureCounts(report2); carried != 3 || lossy != 0 || dropped != 1 {
		t.Errorf("prepare picture grades = %d carried %d lossy %d dropped, want 3/0/1", carried, lossy, dropped)
	}
	out := applyToBytes(t, dstBytes, plan)
	pics := mustParseBytes(t, out).Pictures()
	if len(pics) != 3 || pics[0].Type != wl.PicFrontCover || pics[1].Type != wl.PicArtist || pics[2].Type != wl.PicBand {
		t.Fatalf("transferred pictures = %+v, want the front, the artist and the first band picture", pics)
	}
	if !bytes.Equal(pics[0].Data, testfile.PNG()) || !bytes.Equal(pics[1].Data, testfile.JPEG()) || !bytes.Equal(pics[2].Data, testfile.GIF()) {
		t.Error("transferred picture bytes do not match the source images")
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

// Adding again the picture the file holds under its role's name replaces nothing: the edit is a
// no-op with no warning, so a tagging run repeated under --strict passes.
func TestWavPackReAddedPictureIsNoChange(t *testing.T) {
	src := readFixture(t, notagsWV)
	seed, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: testfile.GIF()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	full := applyToBytes(t, src, seed)

	plan, err := mustParseBytes(t, full).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() || len(plan.Report().Warnings) != 0 {
		t.Errorf("noOp=%v warnings=%+v, want a no-op that warns of nothing", plan.IsNoOp(), plan.Report().Warnings)
	}
}
