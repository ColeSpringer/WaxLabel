package bits

import (
	"encoding/binary"
	"fmt"
	"testing"
)

func TestSniffImage(t *testing.T) {
	// 1x1 RGBA PNG header (IHDR only; pixel data omitted, not needed).
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, // width 1
		0x00, 0x00, 0x00, 0x01, // height 1
		0x08, 0x06, 0x00, 0x00, 0x00, // bitdepth 8, colortype 6 (RGBA)
	}
	// 3x5 GIF89a with a Global Color Table (flag 0x80) and size field 7 -> depth 8, 256 colors.
	gif := append([]byte("GIF89a"), 0x03, 0x00, 0x05, 0x00, 0xF7, 0x00, 0x00)
	// 3x5 GIF89a with NO Global Color Table (high bit clear): depth and colors stay zero
	// rather than being fabricated from the then-reserved size field.
	gifNoGCT := append([]byte("GIF89a"), 0x03, 0x00, 0x05, 0x00, 0x77, 0x00, 0x00)
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
		{"gif", gif, ImageInfo{MIME: "image/gif", Width: 3, Height: 5, Depth: 8, Colors: 256}},
		{"gif-no-gct", gifNoGCT, ImageInfo{MIME: "image/gif", Width: 3, Height: 5}},
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

// TestSniffBMPOS2Header checks that an OS/2 2.x BITMAPINFOHEADER2 (a 16-byte DIB
// header) shares the BITMAPINFOHEADER width/height/depth field offsets (18/22/28), so its
// dimensions are read rather than reported as 0x0.
func TestSniffBMPOS2Header(t *testing.T) {
	os2 := []byte{
		'B', 'M', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // 14-byte file header
		16, 0, 0, 0, // DIB header size 16 (OS/2 2.x BITMAPINFOHEADER2)
		3, 0, 0, 0, // width 3
		5, 0, 0, 0, // height 5
		1, 0, // planes
		24, 0, // bit count (depth 24)
	}
	got, ok := SniffImage(os2)
	if !ok {
		t.Fatal("SniffImage did not recognize the OS/2 BMP")
	}
	if got.Width != 3 || got.Height != 5 || got.Depth != 24 {
		t.Errorf("OS/2 BMP = %dx%d depth %d, want 3x5 depth 24 (16-byte DIB header now read)", got.Width, got.Height, got.Depth)
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

// zeroMinor is the all-zero minor version most ISOBMFF images carry.
const zeroMinor = "\x00\x00\x00\x00"

// realAVIFHeader is the first 32 bytes of an AVIF written by ffmpeg: the ftyp box verbatim,
// so the synthetic boxes below are anchored to what an encoder actually emits.
func realAVIFHeader() []byte {
	return []byte("\x00\x00\x00\x20ftypavif\x00\x00\x00\x00avifmif1miafMA1A")
}

// ftypBox builds an ISOBMFF ftyp box: a big-endian size, the "ftyp" type, the major
// brand, a minor version, then the compatible brands.
func ftypBox(major, minor string, compatible ...string) []byte {
	b := []byte{0, 0, 0, 0}
	b = append(b, "ftyp"...)
	b = append(b, major...)
	b = append(b, minor...)
	for _, c := range compatible {
		b = append(b, c...)
	}
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	return b
}

// TestSniffISOBMFF covers the still-image brands of the ISO base media container: each
// maps to its own MIME with no dimensions, a brand listed only as compatible still
// counts, and a movie brand is not an image.
func TestSniffISOBMFF(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"heic", ftypBox("heic", zeroMinor, "mif1"), "image/heic"},
		{"heix", ftypBox("heix", zeroMinor), "image/heic"},
		{"mif1", ftypBox("mif1", zeroMinor), "image/heif"},
		{"heim", ftypBox("heim", zeroMinor), "image/heif"},
		{"heis", ftypBox("heis", zeroMinor), "image/heif"},
		{"avif", ftypBox("avif", zeroMinor, "mif1", "miaf"), "image/avif"},
		// The sequence brands register their own media types and must not report a still image.
		{"hevc", ftypBox("hevc", zeroMinor), "image/heic-sequence"},
		{"hevx", ftypBox("hevx", zeroMinor), "image/heic-sequence"},
		{"msf1", ftypBox("msf1", zeroMinor), "image/heif-sequence"},
		{"hevm", ftypBox("hevm", zeroMinor), "image/heif-sequence"},
		{"hevs", ftypBox("hevs", zeroMinor), "image/heif-sequence"},
		{"avis", ftypBox("avis", zeroMinor), "image/avif-sequence"},
		{"compatible-brand-only", ftypBox("mp42", zeroMinor, "mp41", "avif"), "image/avif"},
		// A real AVIF header, byte for byte from an ffmpeg encode.
		{"real-avif", realAVIFHeader(), "image/avif"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SniffImage(tc.data)
			if !ok {
				t.Fatalf("SniffImage(%s) not recognized", tc.name)
			}
			if (got != ImageInfo{MIME: tc.want}) {
				t.Errorf("got %+v, want %s with no dimensions", got, tc.want)
			}
		})
	}

	// A plain movie brand shares the container but is not cover art.
	if _, ok := SniffImage(ftypBox("isom", "\x00\x00\x02\x00", "iso2", "mp41")); ok {
		t.Error("an isom movie must not sniff as an image")
	}
	// The minor version sits between the major and compatible brands; reading it as a
	// brand would turn any movie whose version happens to spell one into an image.
	if _, ok := SniffImage(ftypBox("isom", "avif", "mp41")); ok {
		t.Error("the minor version must not be read as a brand")
	}
	// Too short to hold even a major brand.
	if _, ok := SniffImage([]byte{0, 0, 0, 0x10, 'f', 't', 'y', 'p'}); ok {
		t.Error("a truncated ftyp box must not sniff as an image")
	}
	// The declared box size bounds the brand scan: a movie's payload after the box is not
	// made of brands, so a 4-aligned "avif" in it must not turn the file into an image.
	movie := append(ftypBox("isom", zeroMinor, "iso2"), []byte("\x00\x00\x01\x18moovavif")...)
	if _, ok := SniffImage(movie); ok {
		t.Error("a brand string in the payload past the ftyp box must not sniff as an image")
	}
}

// TestSniffISOBMFFUnusableBoxSize covers the sizes that cannot bound the scan: 0 ("to end of
// file"), 1 (a 64-bit size occupies the major brand's offset), and one past the buffer. Each
// falls back to the major brand alone rather than reading on into the payload.
func TestSniffISOBMFFUnusableBoxSize(t *testing.T) {
	withSize := func(n uint32, b []byte) []byte {
		out := append([]byte(nil), b...)
		binary.BigEndian.PutUint32(out[0:4], n)
		return out
	}
	// Major brand avif: still recognized, since the fallback always reads that one.
	for _, size := range []uint32{0, 1000} {
		if got, ok := SniffImage(withSize(size, ftypBox("avif", zeroMinor, "mif1"))); !ok || got.MIME != "image/avif" {
			t.Errorf("box size %d: got %+v ok=%v, want image/avif from the major brand", size, got, ok)
		}
	}
	// Major brand isom with a compatible avif: the unusable size must stop the scan before
	// the compatible brands, which it could not vouch for.
	for _, size := range []uint32{0, 1, 1000} {
		if _, ok := SniffImage(withSize(size, ftypBox("isom", zeroMinor, "avif"))); ok {
			t.Errorf("box size %d: compatible brands must not be scanned when the size cannot bound them", size)
		}
	}
	// A real 64-bit box puts an 8-byte largesize where the major brand belongs, so there is
	// no brand to read at the offset the fallback trusts.
	large := append([]byte{0, 0, 0, 1}, "ftyp"...)
	large = append(large, make([]byte, 8)...) // largesize
	large = append(large, "avifmif1"...)      // the brands, at offset 16
	if _, ok := SniffImage(large); ok {
		t.Error("a 64-bit box size leaves no brand at offset 8; nothing should sniff")
	}
}

// jxlCodestreamHeaders are the leading bytes of real JPEG XL files written by libjxl, one set
// per SizeHeader path: the small sizes code the height in five bits with an aspect ratio, the
// others code one or both dimensions through the wider U32 fields.
var jxlCodestreamHeaders = map[string][]byte{
	"1x1":       {0xFF, 0x0A, 0x00, 0x90, 0x01, 0x00, 0x13, 0x88},
	"8x8":       {0xFF, 0x0A, 0x41, 0x06, 0x00, 0x13, 0x88, 0x02},
	"64x48":     {0xFF, 0x0A, 0xCB, 0x06, 0x00, 0x13, 0x88, 0x02},
	"256x256":   {0xFF, 0x0A, 0x7F, 0x06, 0x00, 0x13, 0x88, 0x02},
	"300x201":   {0xFF, 0x0A, 0x40, 0x06, 0x56, 0x0E, 0x00, 0x13},
	"17x1000":   {0xFF, 0x0A, 0x3A, 0x1F, 0x00, 0xC2, 0x00, 0x13},
	"2000x1333": {0xFF, 0x0A, 0xA2, 0x29, 0xE8, 0xF9, 0x0C, 0x00},
}

// TestSniffJXL covers both JPEG XL forms. Real codestream headers must report their exact
// canvas; the container signature reports the MIME alone; and a signature with nothing
// decodable behind it is not an image, the rule sniffJPEG applies to a Start-Of-Frame.
func TestSniffJXL(t *testing.T) {
	for name, header := range jxlCodestreamHeaders {
		t.Run("codestream-"+name, func(t *testing.T) {
			got, ok := SniffImage(header)
			if !ok {
				t.Fatalf("SniffImage(%s) not recognized", name)
			}
			var w, h int
			fmt.Sscanf(name, "%dx%d", &w, &h)
			if (got != ImageInfo{MIME: "image/jxl", Width: w, Height: h}) {
				t.Errorf("got %+v, want image/jxl %dx%d", got, w, h)
			}
		})
	}
	container := []byte{0x00, 0x00, 0x00, 0x0C, 'J', 'X', 'L', ' ', 0x0D, 0x0A, 0x87, 0x0A}
	if got, ok := SniffImage(container); !ok || got != (ImageInfo{MIME: "image/jxl"}) {
		t.Errorf("container: got %+v ok=%v, want image/jxl with no dimensions", got, ok)
	}
	// A lone 0xFF byte shares the codestream's first byte only.
	if _, ok := SniffImage([]byte{0xFF}); ok {
		t.Error("a single 0xFF must not sniff as JPEG XL")
	}
	// The bare two-byte signature, and a size header cut off part way, carry no canvas.
	if _, ok := SniffImage([]byte{0xFF, 0x0A}); ok {
		t.Error("a bare FF 0A signature must not sniff as JPEG XL")
	}
	if _, ok := SniffImage([]byte{0xFF, 0x0A, 0xCB}); ok {
		t.Error("a JPEG XL truncated mid-SizeHeader must not sniff as valid")
	}
}

// TestImageExtensionCoversEverySniffedMIME is the drift guard between the sniffer and the
// file names codecs build from its result: every MIME SniffImage can report must have an
// extension, so teaching the sniffer a format cannot silently leave covers misnamed.
func TestImageExtensionCoversEverySniffedMIME(t *testing.T) {
	mimes := map[string]bool{
		"image/png": true, "image/jpeg": true, "image/gif": true,
		"image/webp": true, "image/bmp": true, "image/tiff": true, "image/jxl": true,
	}
	for _, m := range isobmffImageBrands {
		mimes[m] = true
	}
	for m := range mimes {
		if ImageExtension(m) == "" {
			t.Errorf("SniffImage can report %s but ImageExtension has no extension for it", m)
		}
	}
	if ImageExtension("application/octet-stream") != "" {
		t.Error("a non-image MIME must have no extension, leaving the fallback to the caller")
	}
}

// TestSniffTIFFOverlongIFDOffset: the first-IFD offset is an unvalidated uint32 from the
// file, and cover art reaches this sniffer straight from a tag. On a 32-bit build an
// offset near 2 GiB overflowed the "offset + 2 > len" bounds check to a negative number
// that passed it and then panicked on the slice, so the guard compares against the bytes
// that remain. The file still sniffs as a TIFF with no dimensions, which is what the
// sniffer reports for any IFD it cannot reach.
func TestSniffTIFFOverlongIFDOffset(t *testing.T) {
	for _, c := range []struct {
		name   string
		offset []byte
	}{
		{"near MaxInt32", []byte{0xFF, 0xFF, 0xFF, 0x7F}},
		{"high bit set", []byte{0x00, 0x00, 0x00, 0x80}},
		{"just past the buffer", []byte{0x09, 0x00, 0x00, 0x00}},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := append([]byte{'I', 'I', 0x2A, 0x00}, c.offset...)
			data = append(data, 0x02, 0x00) // an entry count the offset never reaches
			got, ok := SniffImage(data)
			if !ok || got.MIME != "image/tiff" {
				t.Fatalf("SniffImage = %+v, %v, want an image/tiff", got, ok)
			}
			if got.Width != 0 || got.Height != 0 {
				t.Errorf("dimensions = %dx%d, want 0x0: the IFD is out of reach", got.Width, got.Height)
			}
		})
	}
}
