package waxlabel_test

import (
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/vorbis"
)

// TestOggFLACChainedPictureEditKeepsMalformedBlock pins the result-equals-a-fresh-parse
// promise across two picture edits: the first re-emits the cover set (carrying the
// undecodable block along), and the second must still see that block rather than
// dropping bytes the read path promised to preserve.
func TestOggFLACChainedPictureEditKeepsMalformedBlock(t *testing.T) {
	junk := append([]byte{6, 0, 0, 4}, []byte{0xFF, 0xFF, 0xFF, 0xFF}...) // PICTURE block, undecodable body
	comment := append([]byte{4}, vorbis.RenderCommentList("test", nil)...)
	data := synthOggFLAC(junk, comment)

	doc := mustParseBytes(t, data)
	if len(doc.Pictures()) != 0 {
		t.Fatalf("setup: the malformed block must not decode, got %+v", doc.Pictures())
	}
	plan, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	after, _, err := plan.Execute(t.Context(), wl.WriteTo(discardWriter{}, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}

	// A second picture edit, once off the returned document and once off a fresh parse.
	second := func(d *wl.Document, src []byte) int {
		p2, err := d.Edit().AddPicture(wl.Picture{Type: wl.PicBackCover, Data: tinyPNG()}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		return len(applyToBytes(t, src, p2))
	}
	fromResult := second(after, out)
	fromParse := second(mustParseBytes(t, out), out)
	if fromResult != fromParse {
		t.Errorf("chained edit off the returned document = %d bytes, off a fresh parse = %d", fromResult, fromParse)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
