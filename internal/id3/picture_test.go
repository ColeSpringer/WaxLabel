package id3

import (
	"bytes"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

func TestCutDescription(t *testing.T) {
	png := testfile.PNG() // its IHDR length field has a NUL at offset 8, where a missing terminator splits
	junk := []byte("not an image at all")
	jpeg := testfile.JPEG() // complete, as the sniffer needs
	cases := []struct {
		name     string
		declared string
		rest     []byte
		wantDesc string
		wantData []byte
		wantOK   bool
	}{
		{"terminated before an image", "image/png", append([]byte("Front\x00"), png...), "Front", png, true},
		{"unterminated before an image", "image/png", png, "", png, true},
		{"unterminated before junk", "image/png", junk, "", junk, false},
		{"terminated before junk", "image/png", append([]byte("Front\x00"), junk...), "Front", junk, true},
		// The sniffer accepts a bare "BM" prefix, so a description starting with one must
		// not make the whole remainder read as an image; the frame declares a PNG.
		{"description starting with an image signature", "image/png", append([]byte("BM cover art\x00"), junk...), "BM cover art", junk, true},
		// The same where the declared type is the one the description imitates: the bytes
		// after the terminator are still what the frame is about.
		{"image signature description under a matching type", "image/bmp", append([]byte("BM cover art\x00"), junk...), "", append([]byte("BM cover art\x00"), junk...), true},
		{"image signature description before an image", "image/png", append([]byte("BMP scan\x00"), png...), "BMP scan", png, true},
		// The declarations real taggers write: a non-canonical subtype, an upper-cased
		// type, and a bare format name still identify the same bytes.
		{"non-canonical declaration", "image/jpg", jpeg, "", jpeg, true},
		{"upper-cased declaration", "IMAGE/JPEG", jpeg, "", jpeg, true},
		{"bare format declaration", "JPEG", jpeg, "", jpeg, true},
		{"declaration with a parameter", "image/png; charset=binary", png, "", png, true},
		{"unplaceable declaration matches nothing", "application/octet-stream", append([]byte("Front\x00"), junk...), "Front", junk, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			desc, data, ok := cutDescription(0, c.declared, c.rest)
			if ok != c.wantOK || desc != c.wantDesc || !bytes.Equal(data, c.wantData) {
				t.Errorf("cutDescription = %q, %d bytes, %v; want %q, %d bytes, %v", desc, len(data), ok, c.wantDesc, len(c.wantData), c.wantOK)
			}
		})
	}
}
