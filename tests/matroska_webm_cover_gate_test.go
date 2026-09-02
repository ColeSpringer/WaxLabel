package waxlabel_test

import (
	"errors"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// webmWithCover is a WebM file carrying a cover attachment, which WebM's subset cannot
// write: the same read-but-not-written shape as Musepack's chapters.
func webmWithCover() []byte {
	cover := mkEl(idAttachments, mkEl(idAttached, concat(
		mkStr(idFileName, "cover.png"),
		mkStr(idFileMime, "image/png"),
		mkEl(idFileData, tinyPNG()),
	)))
	return buildMatroska("webm", "WebM Dst", cover)
}

// TestWebMCoverClearGate: clearing the cover a WebM file carries is refused, or under
// the drop option dropped with a warning that says the file keeps its cover, rather
// than reaching the writer; re-setting the file's own cover plans no change.
func TestWebMCoverClearGate(t *testing.T) {
	src := webmWithCover()
	doc := mustParseBytes(t, src)
	if len(doc.Pictures()) != 1 {
		t.Fatalf("setup: %d pictures, want 1", len(doc.Pictures()))
	}
	if _, err := doc.Edit().ClearPictures().Prepare(); !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("clear without the drop option: err = %v, want ErrUnsupportedTag", err)
	}
	plan, err := doc.Edit().ClearPictures().Prepare(wl.WithAllowUnsupportedDrop())
	if err != nil {
		t.Fatalf("clear under the drop option: %v", err)
	}
	var found bool
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnPictureUnsupported {
			found = true
			if !strings.Contains(w.Message, "read-only") || !strings.Contains(w.Message, "keeps its cover art") {
				t.Errorf("warning %q should say the cover art is read-only and kept", w.Message)
			}
		}
	}
	if !found {
		t.Error("expected a picture-unsupported warning for the dropped clear")
	}
	if n := len(mustParseBytes(t, applyToBytes(t, src, plan)).Pictures()); n != 1 {
		t.Errorf("after the dropped clear the file has %d pictures, want its 1", n)
	}
	plan, err = doc.Edit().ClearPictures().AddPicture(doc.Pictures()[0]).Prepare()
	if err != nil {
		t.Fatalf("re-setting the file's own cover: %v", err)
	}
	if !plan.IsNoOp() {
		t.Error("re-setting the file's own cover should plan no change")
	}
}
