package waxlabel_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// bigPictureFLAC returns FLAC bytes with an embedded picture of the given size.
func bigPictureFLAC(t *testing.T, payload int) []byte {
	t.Helper()
	data := make([]byte, payload)
	for i := range data {
		data[i] = byte(i)
	}
	// A JPEG SOI so it sniffs as a real type; the bulk is arbitrary.
	data[0], data[1], data[2] = 0xFF, 0xD8, 0xFF
	return writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/jpeg", Data: data})
	})
}

// Inspect must skip picture payloads, so its allocation count cannot depend on
// how big the embedded artwork is.
func TestInspectAllocsIndependentOfPictureSize(t *testing.T) {
	small := mustParseBytes(t, bigPictureFLAC(t, 1024))
	big := mustParseBytes(t, bigPictureFLAC(t, 8<<20)) // 8 MiB

	allocSmall := testing.AllocsPerRun(50, func() { _ = small.Inspect() })
	allocBig := testing.AllocsPerRun(50, func() { _ = big.Inspect() })

	if allocSmall != allocBig {
		t.Errorf("Inspect allocs differ by picture size (%v vs %v); it must not touch picture bytes", allocSmall, allocBig)
	}
}

// Pictures() returns a fully detached deep copy on every call: each call's Data is
// independent, so mutating one does not corrupt a later call (the #16 fix). Bulk
// scans that must not pay the per-call copy use Inspect(), which skips payloads.
func TestPicturesDetachedAcrossCalls(t *testing.T) {
	doc := mustParseBytes(t, bigPictureFLAC(t, 4<<20))
	a := doc.Pictures()
	b := doc.Pictures()
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("want 1 picture, got %d/%d", len(a), len(b))
	}
	if len(a[0].Data) == 0 || &a[0].Data[0] == &b[0].Data[0] {
		t.Fatal("Pictures() must return a distinct Data backing per call")
	}
	// Mutating one call's bytes must not bleed into another call's.
	orig := b[0].Data[0]
	a[0].Data[0] = ^orig
	if doc.Pictures()[0].Data[0] != orig {
		t.Error("mutating returned Data corrupted a later Pictures() call")
	}
}

func TestRemovePicturesByType(t *testing.T) {
	// Start from a file with a front and a back cover (distinct bytes).
	back := append(tinyPNG(), 0x00)
	withPics := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()})
		e.AddPicture(wl.Picture{Type: wl.PicBackCover, Data: back})
	})
	doc := mustParseBytes(t, withPics)
	if len(doc.Pictures()) != 2 {
		t.Fatalf("setup: got %d pictures, want 2", len(doc.Pictures()))
	}

	plan, err := doc.Edit().
		RemovePictures(func(p wl.Picture) bool { return p.Type == wl.PicBackCover }).
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	pics := mustParseBytes(t, applyToBytes(t, withPics, plan)).Pictures()
	if len(pics) != 1 || pics[0].Type != wl.PicFrontCover {
		t.Errorf("after RemovePictures, got %d pictures (%v), want 1 front cover", len(pics), pics)
	}
}

// onlyReader hides ReaderAt/Seeker so the source looks non-seekable.
type onlyReader struct{ r io.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }

func TestOpenSourceTeesAndEdits(t *testing.T) {
	ctx := context.Background()
	data := readFixture(t, sampleFLAC)

	src, err := wl.OpenSource(ctx, onlyReader{bytes.NewReader(data)})
	if err != nil {
		t.Fatalf("OpenSource: %v", err)
	}
	doc := src.Document()
	if doc.Fields().Title != "Original Title" {
		t.Errorf("Title = %q", doc.Fields().Title)
	}

	// Edit and write without supplying a source: the teed bytes are used.
	plan, err := doc.Edit().Set(tag.Title, "Streamed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, _, err := plan.Execute(ctx, wl.WriteTo(&out, nil)); err != nil {
		t.Fatalf("WriteTo with teed source: %v", err)
	}
	if got := mustParseBytes(t, out.Bytes()).Fields().Title; got != "Streamed" {
		t.Errorf("re-read Title = %q, want Streamed", got)
	}

	// After Close, the document is still readable (it is detached).
	if err := src.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if doc.Fields().Title != "Original Title" {
		t.Error("document became unreadable after Source.Close")
	}
}

func TestDocumentIsDetachedAfterParseFile(t *testing.T) {
	// The whole point of detachment: no fd is retained, so the document stays
	// valid and editable after the file is gone.
	path := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, path)
	src := readFixture(t, path)

	// Editing+writing via an explicit source works even though ParseFile closed
	// its own handle.
	plan, err := doc.Edit().Set(tag.Title, "Detached").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&out, wl.BytesSource(src))); err != nil {
		t.Fatal(err)
	}
	if got := mustParseBytes(t, out.Bytes()).Fields().Title; got != "Detached" {
		t.Errorf("Title = %q", got)
	}
}
