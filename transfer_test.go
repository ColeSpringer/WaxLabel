package waxlabel_test

import (
	"errors"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestPlanTransferReportsLosses simulates copying an M4B (tags + chapters) into a
// FLAC: the tags carry but the chapters drop, and no destination bytes are needed.
func TestPlanTransferReportsLosses(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	report, err := src.PlanTransfer(wl.FormatFLAC)
	if err != nil {
		t.Fatalf("PlanTransfer: %v", err)
	}
	if report.Source != wl.FormatMP4 || report.Dest != wl.FormatFLAC {
		t.Errorf("report formats = %s -> %s, want MP4 -> FLAC", report.Source, report.Dest)
	}

	var sawChapterDrop bool
	for _, it := range report.Items {
		switch it.Kind {
		case wl.TransferField:
			if it.Disposition != wl.Carried {
				t.Errorf("field %s = %s, want carried (FLAC writes all fields)", it.Key, it.Disposition)
			}
		case wl.TransferChapter:
			sawChapterDrop = true
			if it.Disposition != wl.Dropped {
				t.Errorf("chapters = %s, want dropped (FLAC has no chapter write)", it.Disposition)
			}
			if it.Reason == "" {
				t.Error("a dropped item must carry a reason")
			}
		}
	}
	if !sawChapterDrop {
		t.Error("expected a dropped chapter item (the M4B has chapters)")
	}
	if report.Lossless() {
		t.Error("dropping chapters is not lossless")
	}
}

// TestPlanTransferUnsupportedDest: simulating a transfer to a format with no codec
// is an error, while a read-only destination is a valid all-dropped report.
func TestPlanTransferUnsupportedDest(t *testing.T) {
	src := mustParseFile(t, sampleFLAC)

	if _, err := src.PlanTransfer(wl.FormatUnknown); !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("PlanTransfer(Unknown) err = %v, want ErrUnsupportedFormat", err)
	}

	report, err := src.PlanTransfer(wl.FormatMatroska)
	if err != nil {
		t.Fatalf("PlanTransfer(Matroska): %v", err)
	}
	if _, _, dropped := report.Counts(); dropped != len(report.Items) || len(report.Items) == 0 {
		t.Errorf("read-only Matroska should drop everything, got %+v", report)
	}
}

// TestPrepareTransferReportMatchesResult is the load-bearing invariant: the loss
// report PrepareTransfer returns is exactly what executing its plan produces —
// every carried field lands with the source's values, and the dropped chapters do
// not appear. M4B -> FLAC exercises both a carried set and a dropped set at once.
func TestPrepareTransferReportMatchesResult(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	dstBytes := readFixture(t, "testdata/notags.flac") // a blank canvas
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))

	carriedKeys := map[tag.Key]bool{}
	for _, it := range report.Items {
		switch it.Kind {
		case wl.TransferField:
			srcVals, _ := src.Get(it.Key)
			gotVals, present := result.Get(it.Key)
			switch it.Disposition {
			case wl.Carried:
				carriedKeys[it.Key] = true
				if !present || !slices.Equal(gotVals, srcVals) {
					t.Errorf("carried %s = %v (present=%v), want source values %v", it.Key, gotVals, present, srcVals)
				}
			case wl.Dropped:
				if present {
					t.Errorf("dropped %s should not have been written, got %v", it.Key, gotVals)
				}
			}
		case wl.TransferChapter:
			if it.Disposition == wl.Dropped && len(result.Chapters()) != 0 {
				t.Errorf("chapters reported dropped but result has %d", len(result.Chapters()))
			}
		}
	}
	// The blank destination had no tags, so the result's keys are exactly the
	// carried set — the report and the write cannot disagree on membership.
	for _, k := range result.Tags().Keys() {
		if !carriedKeys[k] {
			t.Errorf("result has key %s the report did not mark carried", k)
		}
	}
}

// TestPrepareTransferOverlayKeepsDestKeys: a key present only in the destination
// survives the overlay (copy adds/overwrites the source's keys, it does not wipe
// the destination).
func TestPrepareTransferOverlayKeepsDestKeys(t *testing.T) {
	src := mustParseBytes(t, readFixture(t, sampleFLAC)) // TITLE/ARTIST/ALBUM/ENCODER
	// A destination FLAC carrying a key the source lacks.
	dstBytes := writeBack(t, "testdata/notags.flac", func(e *wl.Editor) {
		e.Set("CATALOGNUMBER", "KEPT-001")
	})
	dst := mustParseBytes(t, dstBytes)

	plan, _, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))

	if got, _ := result.Get("CATALOGNUMBER"); len(got) != 1 || got[0] != "KEPT-001" {
		t.Errorf("CATALOGNUMBER = %v, want it preserved from the destination", got)
	}
	if got, _ := result.Get("TITLE"); len(got) != 1 || got[0] != "Original Title" {
		t.Errorf("TITLE = %v, want overlaid from the source", got)
	}
}

// TestPrepareTransferCarriesPictures: a source picture is carried into a
// picture-capable destination and matches byte-for-byte.
func TestPrepareTransferCarriesPictures(t *testing.T) {
	png := tinyPNG()
	srcBytes := writeBack(t, "testdata/notags.flac", func(e *wl.Editor) {
		e.Set("TITLE", "Cover Test")
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: png})
	})
	src := mustParseBytes(t, srcBytes)

	dstBytes := readFixture(t, "testdata/notags.m4a")
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	var pic wl.TransferItem
	for _, it := range report.Items {
		if it.Kind == wl.TransferPicture {
			pic = it
		}
	}
	if pic.Disposition != wl.Carried || pic.Count != 1 {
		t.Fatalf("picture item = %+v, want 1 carried", pic)
	}

	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	pics := result.Pictures()
	if len(pics) != 1 || !slices.Equal(pics[0].Data, png) {
		t.Errorf("destination picture not carried byte-for-byte (got %d pictures)", len(pics))
	}
}

// TestPlanTransferMatchesPrepareTransfer: the format-only simulation and the
// project-onto-a-document path agree, because both consult the same decision
// function with the same destination capabilities.
func TestPlanTransferMatchesPrepareTransfer(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	dst := mustParseBytes(t, readFixture(t, "testdata/notags.flac"))

	sim, err := src.PlanTransfer(dst.Format())
	if err != nil {
		t.Fatalf("PlanTransfer: %v", err)
	}
	_, applied, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}

	if len(sim.Items) != len(applied.Items) {
		t.Fatalf("item counts differ: sim %d, applied %d", len(sim.Items), len(applied.Items))
	}
	for i := range sim.Items {
		if sim.Items[i] != applied.Items[i] {
			t.Errorf("item %d: sim %+v != applied %+v", i, sim.Items[i], applied.Items[i])
		}
	}
}

// TestMP4RejectsUnstorableCover: an MP4 covr atom can only label JPEG/PNG/BMP, so
// a cover in another format must fail loudly at Prepare rather than be silently
// stored mislabeled as JPEG (a corrupt cover a cross-format copy would otherwise
// claim "carried losslessly"). A supported format still writes.
func TestMP4RejectsUnstorableCover(t *testing.T) {
	doc := mustParseBytes(t, readFixture(t, "testdata/notags.m4a"))
	_, err := doc.Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/webp", Data: []byte("RIFF....WEBP")}).
		Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("err = %v, want ErrUnsupportedTag for a WebP cover on MP4", err)
	}
	if _, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare(); err != nil {
		t.Errorf("a PNG cover should be accepted: %v", err)
	}
}

// TestPrepareTransferReadOnlyDestErrors projects onto a read-only format: it both
// reports everything dropped and refuses to prepare (the format cannot be written
// in this version).
func TestPrepareTransferReadOnlyDestErrors(t *testing.T) {
	src := mustParseFile(t, sampleFLAC)
	dst := mustParseBytes(t, readFixture(t, "testdata/notags.mka"))

	_, report, err := src.PrepareTransfer(dst)
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("err = %v, want ErrUnsupportedFormat", err)
	}
	if report.Lossless() {
		t.Error("report should still record the drops even though Prepare failed")
	}
}
