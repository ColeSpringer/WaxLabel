package waxlabel_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestMatroskaNonFrontCoverCopyIdempotent is a regression guard: a picture whose role Matroska
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
	data := buildMatroska("matroska", "cover", cover)
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

// TestMatroskaForceNonImageReprojectsAsCover is the read/write-symmetry regression: a non-image
// cover embedded under --force is written under the cover-art
// file name (cover.<ext>), so it now reads back as one Unrecognized() picture rather than vanishing
// as a plain attachment. The result view and a fresh reparse must AGREE on that 1 picture (the
// write-fidelity invariant), and its bytes and front-cover role round-trip while the MIME stays
// application/octet-stream (unrecognized, not sniffed to a real image).
func TestMatroskaForceNonImageReprojectsAsCover(t *testing.T) {
	data := buildMatroska("matroska", "force-cover", nil)
	nonImage := wl.Picture{Type: wl.PicFrontCover, MIME: "application/octet-stream", Data: []byte("plain file, not an image")}
	plan, err := mustParseBytes(t, data).Edit().AddPicture(nonImage).Prepare(wl.WithUnrecognizedPictures())
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	res, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	resPics, rePics := res.Pictures(), mustParseBytes(t, w.b).Pictures()
	if len(resPics) != 1 || len(rePics) != 1 {
		t.Fatalf("result=%d reparse=%d pictures, want 1 each (a --force cover round-trips as an Unrecognized picture)", len(resPics), len(rePics))
	}
	got := rePics[0]
	if got.Type != wl.PicFrontCover {
		t.Errorf("reprojected cover role = %v, want front cover (stored as cover.<ext>)", got.Type)
	}
	if got.MIME != "application/octet-stream" {
		t.Errorf("reprojected cover MIME = %q, want application/octet-stream (unrecognized)", got.MIME)
	}
	if string(got.Data) != "plain file, not an image" {
		t.Errorf("reprojected cover bytes changed: %q", got.Data)
	}
}

// TestMatroskaForceCoverNoAccumulation is the report repro: repeating
// `--remove-pictures --add-cover garbage.bin --force` must not stack cover_1/cover_2/... covers
// (the forced cover is now a removable picture, so --remove-pictures clears the prior one before
// the re-add), and a final --remove-pictures leaves zero covers. A foreign non-cover attachment
// named cover_letter.txt is preserved verbatim throughout (isCoverName rejects the non-numeric
// suffix, so it is never reprojected as a cover and rebuilt).
func TestMatroskaForceCoverNoAccumulation(t *testing.T) {
	letter := mkEl(idAttachments, mkEl(idAttached, concat(
		mkStr(idFileName, "cover_letter.txt"),
		mkStr(idFileMime, "text/plain"),
		mkEl(idFileData, []byte("Dear hiring manager,")),
	)))
	data := buildMatroska("matroska", "force-cover", letter)

	forceCover := func(src []byte) []byte {
		t.Helper()
		garbage := wl.Picture{Type: wl.PicFrontCover, MIME: "application/octet-stream", Data: []byte("garbage")}
		plan, err := mustParseBytes(t, src).Edit().
			RemovePictures(func(wl.Picture) bool { return true }).
			AddPicture(garbage).
			Prepare(wl.WithUnrecognizedPictures())
		if err != nil {
			t.Fatal(err)
		}
		var w writerTo
		if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(src))); err != nil {
			t.Fatal(err)
		}
		return w.b
	}

	out := forceCover(forceCover(data)) // two rounds must not accumulate
	if pics := mustParseBytes(t, out).Pictures(); len(pics) != 1 {
		t.Errorf("after two --force rounds: %d covers, want 1 (no cover_<n> accumulation)", len(pics))
	}

	// --remove-pictures now clears the forced cover (a picture, not an unremovable attachment).
	clr, err := mustParseBytes(t, out).Edit().RemovePictures(func(wl.Picture) bool { return true }).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var cw writerTo
	if _, _, err := clr.Execute(context.Background(), wl.WriteTo(&cw, wl.BytesSource(out))); err != nil {
		t.Fatal(err)
	}
	if pics := mustParseBytes(t, cw.b).Pictures(); len(pics) != 0 {
		t.Errorf("after --remove-pictures: %d covers, want 0", len(pics))
	}
	// The foreign cover_letter.txt attachment survives verbatim (never reprojected as a cover).
	if !bytes.Contains(cw.b, []byte("cover_letter.txt")) || !bytes.Contains(cw.b, []byte("Dear hiring manager,")) {
		t.Errorf("foreign cover_letter.txt attachment must be preserved verbatim")
	}
}

// TestMatroskaCoverNamedNonImageStaysAttachment guards the cover-name gate's scope: an
// attachment named exactly cover.txt but carrying a non-image, non-octet-stream MIME
// (text/plain) is NOT promoted to a picture - only an image MIME or WaxLabel's --force
// octet-stream cover is. So it reads back as 0 pictures and survives an unrelated tag edit
// verbatim, rather than being reprojected and rebuilt as cover.jpg.
func TestMatroskaCoverNamedNonImageStaysAttachment(t *testing.T) {
	att := mkEl(idAttachments, mkEl(idAttached, concat(
		mkStr(idFileName, "cover.txt"), // a valid cover *name* but a text MIME
		mkStr(idFileMime, "text/plain"),
		mkEl(idFileData, []byte("liner notes, not a cover")),
	)))
	data := buildMatroska("matroska", "cover-txt", att)

	if pics := mustParseBytes(t, data).Pictures(); len(pics) != 0 {
		t.Fatalf("cover.txt (text/plain) projected %d pictures, want 0 (not an image or octet-stream cover)", len(pics))
	}
	// An unrelated tag edit must preserve the text attachment verbatim (name and bytes).
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(w.b, []byte("cover.txt")) || !bytes.Contains(w.b, []byte("liner notes, not a cover")) {
		t.Errorf("cover.txt text attachment must be preserved verbatim (not rebuilt as cover.jpg)")
	}
	if bytes.Contains(w.b, []byte("cover.jpg")) {
		t.Errorf("cover.txt must not be rebuilt under an image cover name")
	}
	if pics := mustParseBytes(t, w.b).Pictures(); len(pics) != 0 {
		t.Errorf("after edit: cover.txt projected %d pictures, want 0", len(pics))
	}
}

// TestMatroskaForeignOctetCoverReprojects locks in the deliberate consequence of the octet-stream
// cover gate: a FOREIGN application/octet-stream attachment named cover.bin (not authored by
// WaxLabel) reprojects as a removable Unrecognized() picture, and an unrelated edit rebuilds it
// under the cover-art convention rather than preserving it verbatim. This is spec-aligned for
// Matroska's cover convention; the bytes and MIME are preserved. A non-octet foreign attachment
// named cover.* stays a plain attachment (TestMatroskaCoverNamedNonImageStaysAttachment).
func TestMatroskaForeignOctetCoverReprojects(t *testing.T) {
	foreign := mkEl(idAttachments, mkEl(idAttached, concat(
		mkStr(idFileName, "cover.bin"),
		mkStr(idFileMime, "application/octet-stream"),
		mkEl(idFileData, []byte("foreign cover bytes")),
	)))
	data := buildMatroska("matroska", "foreign-octet", foreign)

	pics := mustParseBytes(t, data).Pictures()
	if len(pics) != 1 || pics[0].MIME != "application/octet-stream" {
		t.Fatalf("foreign cover.bin projected %+v, want 1 octet-stream picture", pics)
	}
	// It is removable via the picture API.
	plan, err := mustParseBytes(t, data).Edit().RemovePictures(func(wl.Picture) bool { return true }).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data))); err != nil {
		t.Fatal(err)
	}
	if pics := mustParseBytes(t, w.b).Pictures(); len(pics) != 0 {
		t.Errorf("after --remove-pictures: %d covers, want 0 (a foreign octet cover is removable)", len(pics))
	}
}

// TestMatroskaNonCoverPictureRejected guards against silent data loss: a directly-authored
// picture whose MIME is neither an image nor an unsniffable octet-stream cover (a caller doing
// AddPicture{MIME:"text/plain"|"application/pdf"} + WithUnrecognizedPictures) cannot be stored as
// Matroska cover art. The reprojection would drop it, collapsing the edit to a silent no-op, so
// Prepare must refuse it with a clear error instead. The CLI and transfer paths never produce
// such a MIME, so this only guards the direct library API.
func TestMatroskaNonCoverPictureRejected(t *testing.T) {
	data := buildMatroska("matroska", "reject", nil)
	for _, mime := range []string{"text/plain", "application/pdf"} {
		pic := wl.Picture{Type: wl.PicFrontCover, MIME: mime, Data: []byte("not cover art")}
		_, err := mustParseBytes(t, data).Edit().AddPicture(pic).Prepare(wl.WithUnrecognizedPictures())
		if err == nil {
			t.Errorf("MIME %q: Prepare succeeded, want a refusal (a non-image/non-octet picture is not cover art)", mime)
			continue
		}
		if !strings.Contains(err.Error(), "cover art") {
			t.Errorf("MIME %q: error = %v, want one mentioning cover art", mime, err)
		}
	}
	// An octet-stream --force cover of the same shape is still accepted (it round-trips as an
	// Unrecognized picture), so the refusal is scoped to genuinely non-cover MIMEs.
	octet := wl.Picture{Type: wl.PicFrontCover, MIME: "application/octet-stream", Data: []byte("forced")}
	if _, err := mustParseBytes(t, data).Edit().AddPicture(octet).Prepare(wl.WithUnrecognizedPictures()); err != nil {
		t.Errorf("octet-stream --force cover must still be accepted, got %v", err)
	}
}
