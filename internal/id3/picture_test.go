package id3

import (
	"bytes"
	"testing"
)

// pngHeader is a 1x1 RGBA PNG header, enough for the sniffer. Its IHDR length field
// carries a NUL at offset 8, which is what a missing description terminator splits on.
func pngHeader() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
	}
}

func TestCutDescription(t *testing.T) {
	png := pngHeader()
	junk := []byte("not an image at all")
	// A minimal complete JPEG: SOI then a 3x5 SOF0, which the sniffer needs to recognize it.
	jpeg := []byte{
		0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08, 0x00, 0x05, 0x00, 0x03,
		0x03, 0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
	}
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
		// The sniffer accepts a bare "BM" prefix, so a description that starts with one must
		// not make the whole remainder read as an image and swallow the description. The
		// frame declares a PNG, and "BM cover art..." is not one.
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
