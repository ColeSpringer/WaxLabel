package core

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// TestProjectPicturesSniffs: display projection sniffs MIME; clone leaves originals untouched.
func TestProjectPicturesSniffs(t *testing.T) {
	gif := testfile.GIF()
	orig := []Picture{
		{Type: PicFrontCover, MIME: "image/png", Data: gif},
		{Type: PicFrontCover, MIME: "", Data: gif},
		{Type: PicFrontCover, MIME: "", Data: []byte("not image")},
	}
	want := []string{"image/gif", "image/gif", UnrecognizedMIME}
	proj := ProjectPictures(orig)
	for i := range want {
		if proj[i].MIME != want[i] {
			t.Errorf("ProjectPictures[%d] MIME = %q, want %q", i, proj[i].MIME, want[i])
		}
	}
	if orig[0].MIME != "image/png" || orig[1].MIME != "" {
		t.Errorf("ProjectPictures mutated its input: %q, %q", orig[0].MIME, orig[1].MIME)
	}
}

// An image type declared for bytes of no format the sniffer knows stands: nothing can verify it.
// A type the sniffer knows, declared for other bytes, reads as unrecognized.
func TestSniffStoredKeepsAnUnverifiableImageType(t *testing.T) {
	junk := []byte("<svg/>")
	for _, c := range []struct{ declared, want string }{
		{"image/svg+xml", "image/svg+xml"},
		{"IMAGE/X-Icon", "IMAGE/X-Icon"},
		{"image/jpeg", UnrecognizedMIME},
		{"image/jpg", UnrecognizedMIME},
		{"image/x-png", UnrecognizedMIME},
		{"image/pjpeg", UnrecognizedMIME},
		{"image/x-ms-bmp", UnrecognizedMIME},
		{"image/tif", UnrecognizedMIME},
		{"Image/PNG; charset=binary", UnrecognizedMIME},
		{"image/svg+xml; charset=utf-8", "image/svg+xml; charset=utf-8"},
		{"image/", UnrecognizedMIME},
		{"text/plain", UnrecognizedMIME},
		{"", UnrecognizedMIME},
		{LinkMIME, LinkMIME},
	} {
		p := Picture{MIME: c.declared, Width: 16, Data: junk}
		if SniffStored(&p) {
			t.Errorf("%q: bytes that are no image sniffed", c.declared)
		}
		if p.MIME != c.want {
			t.Errorf("%q: MIME = %q, want %q", c.declared, p.MIME, c.want)
		}
	}
	p := Picture{MIME: "image/svg+xml", Data: testfile.GIF()}
	if !SniffStored(&p) || p.MIME != "image/gif" {
		t.Errorf("recognizable bytes: MIME = %q, want image/gif", p.MIME)
	}
	// What the file declares of a type nothing can verify stands whole, its size with it; the
	// size declared under a type that reads as unrecognized goes, as before.
	svg := Picture{MIME: "image/svg+xml", Width: 512, Height: 256, Depth: 24, Colors: 3, Data: junk}
	if SniffStored(&svg); svg.Width != 512 || svg.Height != 256 || svg.Depth != 24 || svg.Colors != 3 {
		t.Errorf("unverifiable type: size = %dx%dx%d/%d, want the declared 512x256x24/3", svg.Width, svg.Height, svg.Depth, svg.Colors)
	}
	bad := Picture{MIME: "image/png", Width: 512, Height: 256, Depth: 24, Data: junk}
	if SniffStored(&bad); bad.Width != 0 || bad.Height != 0 || bad.Depth != 0 {
		t.Errorf("known type over other bytes: size = %dx%dx%d, want none", bad.Width, bad.Height, bad.Depth)
	}
}

// One table folds the spellings taggers write onto the type the sniffer reports: it is what an
// ID3 frame's declared type is compared by and what counts as a type the sniffer knows.
func TestCanonicalImageMIME(t *testing.T) {
	for declared, want := range map[string]string{
		"image/jpeg": "image/jpeg", "image/jpg": "image/jpeg", "image/pjpeg": "image/jpeg", "JPEG": "image/jpeg", "jpg": "image/jpeg",
		"image/png": "image/png", "image/x-png": "image/png", "PNG": "image/png", " Image/PNG ; charset=binary": "image/png",
		"image/bmp": "image/bmp", "image/x-ms-bmp": "image/bmp", "image/x-bmp": "image/bmp",
		"image/tiff": "image/tiff", "image/tif": "image/tiff", "image/x-tiff": "image/tiff",
		"image/gif": "image/gif", "image/webp": "image/webp", "image/heic": "image/heic", "image/avif": "image/avif", "image/jxl": "image/jxl",
		"image/svg+xml": "", "image/x-icon": "", "image/": "", "": "", "text/plain": "", "-->": "",
	} {
		if got := CanonicalImageMIME(declared); got != want {
			t.Errorf("CanonicalImageMIME(%q) = %q, want %q", declared, got, want)
		}
	}
}
