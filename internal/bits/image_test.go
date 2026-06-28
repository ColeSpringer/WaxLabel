package bits

import "testing"

func TestSniffImage(t *testing.T) {
	// 1x1 RGBA PNG header (IHDR only; pixel data omitted, not needed).
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, // width 1
		0x00, 0x00, 0x00, 0x01, // height 1
		0x08, 0x06, 0x00, 0x00, 0x00, // bitdepth 8, colortype 6 (RGBA)
	}
	// 3x5 GIF89a, GCT depth 8.
	gif := append([]byte("GIF89a"), 0x03, 0x00, 0x05, 0x00, 0x77, 0x00, 0x00)
	// 3x5 baseline JPEG: SOI then SOF0 (precision 8, 3 components).
	jpeg := []byte{
		0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08,
		0x00, 0x05, // height 5
		0x00, 0x03, // width 3
		0x03, // components
		0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
	}
	// Same JPEG but with 0xFF fill bytes before the SOF marker, which the
	// sniffer must skip rather than mistake for a marker.
	jpegFill := []byte{
		0xFF, 0xD8, 0xFF, 0xFF, 0xFF, 0xC0, 0x00, 0x11, 0x08,
		0x00, 0x05, 0x00, 0x03, 0x03,
		0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
	}
	// 3x5 WebP, extended (VP8X) form: RIFF/WEBP, then VP8X with a 24-bit canvas
	// stored as (width-1, height-1).
	webpVP8X := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X"),
		0x0A, 0x00, 0x00, 0x00, // chunk size
		0x00, 0x00, 0x00, 0x00, // flags
		0x02, 0x00, 0x00, // width-1 = 2
		0x04, 0x00, 0x00) // height-1 = 4
	// 3x5 WebP, lossy (VP8) form: frame tag, the 9d 01 2a start code, then the
	// 14-bit width and height.
	webpVP8 := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		0x0A, 0x00, 0x00, 0x00, // chunk size
		0x00, 0x00, 0x00, // frame tag
		0x9d, 0x01, 0x2a, // start code
		0x03, 0x00, // width 3
		0x05, 0x00) // height 5
	// 3x5 WebP, lossless (VP8L) form: 0x2f signature, then (width-1, height-1)
	// packed 14 bits each, little-endian.
	webpVP8L := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8L"),
		0x05, 0x00, 0x00, 0x00, // chunk size
		0x2f,                   // signature
		0x02, 0x00, 0x01, 0x00) // (w-1)=2 in bits 0-13, (h-1)=4 in bits 14-27
	// 3x5 24-bit BMP with a BITMAPINFOHEADER.
	bmp := []byte{
		'B', 'M', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // file header
		40, 0, 0, 0, // DIB header size
		3, 0, 0, 0, // width 3
		5, 0, 0, 0, // height 5
		1, 0, // planes
		24, 0, // bit count
	}
	// 3x5 little-endian TIFF: two IFD entries (ImageWidth, ImageLength) as SHORTs.
	tiff := []byte{
		'I', 'I', 0x2A, 0x00, // little-endian magic
		0x08, 0, 0, 0, // first IFD at offset 8
		0x02, 0x00, // entry count
		0x00, 0x01, 0x03, 0x00, 0x01, 0, 0, 0, 0x03, 0x00, 0x00, 0x00, // ImageWidth = 3
		0x01, 0x01, 0x03, 0x00, 0x01, 0, 0, 0, 0x05, 0x00, 0x00, 0x00, // ImageLength = 5
	}

	cases := []struct {
		name string
		data []byte
		want ImageInfo
	}{
		{"png", png, ImageInfo{MIME: "image/png", Width: 1, Height: 1, Depth: 32}},
		{"gif", gif, ImageInfo{MIME: "image/gif", Width: 3, Height: 5, Depth: 8}},
		{"jpeg", jpeg, ImageInfo{MIME: "image/jpeg", Width: 3, Height: 5, Depth: 24}},
		{"jpeg-fill", jpegFill, ImageInfo{MIME: "image/jpeg", Width: 3, Height: 5, Depth: 24}},
		{"webp-vp8x", webpVP8X, ImageInfo{MIME: "image/webp", Width: 3, Height: 5}},
		{"webp-vp8", webpVP8, ImageInfo{MIME: "image/webp", Width: 3, Height: 5}},
		{"webp-vp8l", webpVP8L, ImageInfo{MIME: "image/webp", Width: 3, Height: 5}},
		{"bmp", bmp, ImageInfo{MIME: "image/bmp", Width: 3, Height: 5, Depth: 24}},
		{"tiff", tiff, ImageInfo{MIME: "image/tiff", Width: 3, Height: 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SniffImage(tc.data)
			if !ok {
				t.Fatalf("SniffImage(%s) not recognized", tc.name)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}

	if _, ok := SniffImage([]byte("not an image")); ok {
		t.Error("expected unrecognized data to return ok=false")
	}

	// A RIFF/WAVE container (a WAV file) shares WebP's "RIFF" prefix but a
	// different form type, so it must not be sniffed as an image.
	if _, ok := SniffImage([]byte("RIFF\x00\x00\x00\x00WAVEfmt ")); ok {
		t.Error("RIFF/WAVE should not sniff as a WebP image")
	}

	// A recognized header too short to carry dimensions still yields its MIME with
	// zero dimensions, rather than reporting the data unrecognized.
	if got, ok := SniffImage([]byte("RIFF\x00\x00\x00\x00WEBP")); !ok || got != (ImageInfo{MIME: "image/webp"}) {
		t.Errorf("short WebP: got %+v ok=%v, want image/webp with zero dimensions", got, ok)
	}
}

// TestSniffJPEGRequiresSOF checks that a JPEG must carry a readable Start-Of-Frame
// before the sniffer accepts it. A bare magic number or a SOF truncated before its
// geometry is rejected; intact JPEGs are still covered by TestSniffImage.
func TestSniffJPEGRequiresSOF(t *testing.T) {
	if _, ok := SniffImage([]byte{0xFF, 0xD8, 0xFF}); ok {
		t.Error("a 3-byte FF D8 FF magic (no SOF) must not sniff as a valid JPEG")
	}
	// SOF0 marker with a declared length but cut off before the 7 geometry bytes.
	if _, ok := SniffImage([]byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08}); ok {
		t.Error("a JPEG truncated mid-SOF must not sniff as valid")
	}
}

// TestSniffBMPNegativeWidth checks a hostile BMP with the sign bit set in the
// width field yields a non-negative dimension (its magnitude), so it cannot wrap
// to a huge value when later stored as an unsigned 32-bit picture width.
func TestSniffBMPNegativeWidth(t *testing.T) {
	bmp := []byte{
		'B', 'M', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		40, 0, 0, 0,
		0xFB, 0xFF, 0xFF, 0xFF, // width = -5 as int32
		0x05, 0x00, 0x00, 0x00, // height = 5
		1, 0, 24, 0,
	}
	got, ok := SniffImage(bmp)
	if !ok {
		t.Fatal("BMP not recognized")
	}
	if got.Width != 5 {
		t.Errorf("width = %d, want 5 (negative width normalized to its magnitude)", got.Width)
	}
	if got.Height != 5 {
		t.Errorf("height = %d, want 5", got.Height)
	}
}

// TestSniffTIFFIgnoresMultiValueCount checks that an ImageWidth/ImageLength IFD
// entry whose value count is not 1 - meaning its 4-byte field is a file offset,
// not an inline value - is skipped, rather than mistaking the offset for a
// dimension.
func TestSniffTIFFIgnoresMultiValueCount(t *testing.T) {
	tiff := []byte{
		'I', 'I', 0x2A, 0x00, // little-endian magic
		0x08, 0, 0, 0, // first IFD at offset 8
		0x02, 0x00, // entry count
		// ImageWidth with count=2: the field is an offset (0x0539), to be skipped.
		0x00, 0x01, 0x03, 0x00, 0x02, 0, 0, 0, 0x39, 0x05, 0x00, 0x00,
		// ImageLength with count=1: a genuine inline height of 5.
		0x01, 0x01, 0x03, 0x00, 0x01, 0, 0, 0, 0x05, 0x00, 0x00, 0x00,
	}
	got, ok := SniffImage(tiff)
	if !ok {
		t.Fatal("TIFF not recognized")
	}
	if got.Width != 0 {
		t.Errorf("width = %d, want 0 (a multi-value entry must be skipped, not read as an offset)", got.Width)
	}
	if got.Height != 5 {
		t.Errorf("height = %d, want 5", got.Height)
	}
}

// pngChunk builds a single PNG chunk: a 4-byte big-endian length, the 4-byte type,
// the data, and a placeholder CRC (the sniffer does not validate it).
func pngChunk(typ string, data []byte) []byte {
	b := []byte{byte(len(data) >> 24), byte(len(data) >> 16), byte(len(data) >> 8), byte(len(data))}
	b = append(b, typ...)
	b = append(b, data...)
	return append(b, 0, 0, 0, 0) // CRC placeholder
}

// TestSniffIndexedColors covers palette counts: indexed PNG reads its PLTE entry
// count, GIF reads the global color table size, non-indexed formats stay 0, and a
// garbage chunk length is bounded instead of panicking.
func TestSniffIndexedColors(t *testing.T) {
	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	ihdr := func(colorType byte) []byte {
		return pngChunk("IHDR", []byte{0, 0, 0, 4, 0, 0, 0, 4, 8, colorType, 0, 0, 0}) // 4x4, bitdepth 8
	}
	// Indexed PNG: IHDR(type 3) + a PLTE of 3 RGB triplets (9 bytes -> 3 colors).
	idxPNG := append(append(append([]byte{}, pngMagic...), ihdr(3)...), pngChunk("PLTE", make([]byte, 9))...)
	// Indexed PNG with no PLTE chunk: the palette count is unknown -> 0.
	noPLTE := append(append([]byte{}, pngMagic...), ihdr(3)...)
	// Non-indexed (truecolor) PNG with a stray PLTE must not report colors.
	truecolor := append(append(append([]byte{}, pngMagic...), ihdr(2)...), pngChunk("PLTE", make([]byte, 9))...)
	// Color-type-3 PNG whose post-IHDR chunk claims a 0xFFFFFFFF length: the bound
	// must break the walk, not slice out of range.
	garbage := append(append(append([]byte{}, pngMagic...), ihdr(3)...),
		0xFF, 0xFF, 0xFF, 0xFF, 'P', 'L', 'T', 'E')
	// Indexed GIF: GCT flag set (0x80) with size field 2 -> 2^(2+1) = 8 colors.
	idxGIF := append([]byte("GIF89a"), 0x04, 0x00, 0x04, 0x00, 0x82, 0x00, 0x00)

	cases := []struct {
		name       string
		data       []byte
		wantColors int
	}{
		{"indexed-png", idxPNG, 3},
		{"png-no-plte", noPLTE, 0},
		{"truecolor-png-stray-plte", truecolor, 0},
		{"garbage-chunk-length", garbage, 0}, // must not panic
		{"indexed-gif", idxGIF, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SniffImage(tc.data)
			if !ok {
				t.Fatalf("SniffImage(%s) not recognized", tc.name)
			}
			if got.Colors != tc.wantColors {
				t.Errorf("Colors = %d, want %d (info %+v)", got.Colors, tc.wantColors, got)
			}
		})
	}
}
