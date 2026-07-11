package waxlabel_test

import (
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Some files contain duplicate file-icon pictures (two type-1 or two type-2
// blocks), which FLAC forbids but existing files may carry. WaxLabel preserves
// that input on unrelated edits: validatePictures runs only when the edit wrote
// the picture set. A direct picture edit that creates a duplicate icon is still
// rejected.

// pngIcon is a type-1 (32x32 file-icon) picture backed by a real PNG.
func pngIcon() wl.Picture {
	return wl.Picture{Type: wl.PicFileIcon, MIME: "image/png", Data: tinyPNG()}
}

// flacTwoType1Icons builds a FLAC carrying two type-1 PICTURE blocks by
// injecting raw bytes; the editor itself refuses to author a second icon.
func flacTwoType1Icons(comments ...vorbis.Comment) []byte {
	return flacWithCommentBlock(comments, pngIcon(), pngIcon())
}

func countType1(doc *wl.Document) int {
	n := 0
	for _, p := range doc.Pictures() {
		if p.Type == wl.PicFileIcon {
			n++
		}
	}
	return n
}

func TestDuplicateIconTagEditSucceeds(t *testing.T) {
	// A tags-only edit uses the file's existing pictures (picsTouched=false), so
	// duplicate icons pass through and the edit succeeds.
	data := flacTwoType1Icons()
	if got := countType1(mustParseBytes(t, data)); got != 2 {
		t.Fatalf("setup: want 2 type-1 icons parsed, got %d", got)
	}
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New Title").Prepare()
	if err != nil {
		t.Fatalf("tags-only edit on a duplicate-icon file was refused: %v", err)
	}
	out := applyToBytes(t, data, plan)
	re := mustParseBytes(t, out)
	if re.Fields().Title != "New Title" {
		t.Errorf("title after edit = %q", re.Fields().Title)
	}
	if got := countType1(re); got != 2 {
		t.Errorf("duplicate icons not preserved verbatim: %d type-1 icons after edit", got)
	}
}

func TestDuplicateIconTransferSucceedsWhenPicturesUntouched(t *testing.T) {
	// Transfer tags from a clean, pictureless source into a two-icon destination.
	// The source carries no picture, so the destination's picture set is untouched
	// (picsTouched=false) and the transfer succeeds.
	dstData := flacTwoType1Icons()
	dst := mustParseBytes(t, dstData)
	src := mustParseBytes(t, flacWithVendor("test", "TITLE=Copied"))
	plan, _, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("transfer onto a duplicate-icon destination was refused: %v", err)
	}
	out := applyToBytes(t, dstData, plan)
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Copied" {
		t.Errorf("transferred title = %q", re.Fields().Title)
	}
	if got := countType1(re); got != 2 {
		t.Errorf("destination icons not preserved through transfer: %d type-1 icons", got)
	}
}

func TestDuplicateIconRemediationSucceeds(t *testing.T) {
	// Dropping one duplicate icon is a picture edit (picsTouched=true), so
	// validatePictures runs on the reduced, now-valid set.
	data := flacTwoType1Icons()
	dropped := false
	plan, err := mustParseBytes(t, data).Edit().RemovePictures(func(p wl.Picture) bool {
		if p.Type == wl.PicFileIcon && !dropped {
			dropped = true
			return true // drop exactly the first icon
		}
		return false
	}).Prepare()
	if err != nil {
		t.Fatalf("dropping one duplicate icon was refused: %v", err)
	}
	if got := countType1(mustParseBytes(t, applyToBytes(t, data, plan))); got != 1 {
		t.Errorf("want 1 type-1 icon after remediation, got %d", got)
	}
}

func TestDuplicateIconLintFixRemediatesEncoderStamp(t *testing.T) {
	// A transcoder vendor stamp creates a fixable inherited-encoder finding
	// alongside duplicate icons, which lint cannot auto-fix. Prepare should still
	// succeed: the encoder stamp is stripped while the icons pass through untouched.
	data := flacWithCommentBlockVendor("Lavf59.27.100",
		[]vorbis.Comment{{Name: "TITLE", Value: "x"}}, pngIcon(), pngIcon())
	doc := mustParseBytes(t, data)
	if !hasInheritedEncoder(doc) {
		t.Fatal("setup: expected an inherited-encoder finding to remediate")
	}
	fix := doc.PlanLintFix()
	plan, err := doc.Edit().Apply(fix.Patch).Prepare(fix.Options...)
	if err != nil {
		t.Fatalf("lint --fix Prepare failed on a duplicate-icon file: %v", err)
	}
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if hasInheritedEncoder(re) {
		t.Error("the fixable inherited-encoder stamp was not remediated")
	}
	if got := countType1(re); got != 2 {
		t.Errorf("duplicate icons should pass through lint --fix untouched, got %d type-1", got)
	}
}

func TestDuplicateIconDirectPictureEditStillRefused(t *testing.T) {
	// Control: authoring a second icon is still caught (picsTouched=true -> validatePictures runs).
	data := flacWithCommentBlock(nil, pngIcon()) // one icon to start
	_, err := mustParseBytes(t, data).Edit().AddPicture(pngIcon()).Prepare()
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Fatalf("adding a second type-1 icon should be refused, got %v", err)
	}
}

func TestDuplicateIconTransferCarryingSecondIconSucceeds(t *testing.T) {
	// A transfer faithfully carries the source picture set, so the source's own duplicate icons
	// must not abort the copy as if the user had authored a second icon; the carried flag
	// suppresses the icon-count rule (like the other faithful-carry checks). lint still flags the
	// carried result, so the duplicate stays discoverable.
	dstData := flacWithVendor("test", "TITLE=Dst")
	src := mustParseBytes(t, flacTwoType1Icons(vorbis.Comment{Name: "TITLE", Value: "Src"}))
	plan, _, err := src.PrepareTransfer(mustParseBytes(t, dstData))
	if err != nil {
		t.Fatalf("a transfer carrying two type-1 icons should succeed, got %v", err)
	}
	re := mustParseBytes(t, applyToBytes(t, dstData, plan))
	if got := countType1(re); got != 2 {
		t.Errorf("expected 2 type-1 icons carried, got %d", got)
	}
	found := false
	for _, f := range re.Lint() {
		if f.Code == "duplicate-icon" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected the duplicate-icon finding on the carried result")
	}
}

func TestDuplicateFrontCoverTagEditUnaffected(t *testing.T) {
	// Control: type-3 (front cover) duplicates were never covered by the icon-count rule; a
	// tags-only edit stays fine regardless of the gate.
	cover := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()}
	data := flacWithCommentBlock(nil, cover, cover)
	if _, err := mustParseBytes(t, data).Edit().Set(tag.Title, "X").Prepare(); err != nil {
		t.Fatalf("a tags-only edit on duplicate front covers was refused: %v", err)
	}
}
