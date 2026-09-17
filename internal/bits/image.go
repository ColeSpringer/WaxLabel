package bits

import "encoding/binary"

// ImageInfo is MIME and header-derived dimensions. Header values are untrusted;
// this package never allocates from them.
type ImageInfo struct {
	MIME   string
	Width  int
	Height int
	Depth  int // bits per pixel across all channels; 0 if unknown
	// Colors is palette entry count for indexed PNG/GIF; 0 otherwise.
	Colors int
}

// SniffImage identifies PNG, JPEG, GIF, WebP, BMP, TIFF, HEIF/HEIC, AVIF, and JXL.
// ok=false for unknown/truncated data. WebP/TIFF may yield MIME without dimensions.
//
// RecognizedFormats lists formats [SniffImage] handles; keep in sync with the switch.
const RecognizedFormats = "PNG/JPEG/GIF/WebP/BMP/TIFF/HEIF/AVIF/JXL"

func SniffImage(data []byte) (ImageInfo, bool) {
	switch {
	case hasPrefix(data, pngMagic):
		return sniffPNG(data)
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return sniffJPEG(data)
	case hasPrefix(data, gif87) || hasPrefix(data, gif89):
		return sniffGIF(data)
	case isWebP(data):
		return sniffWebP(data)
	case hasPrefix(data, bmpMagic):
		return sniffBMP(data)
	case hasPrefix(data, tiffLE) || hasPrefix(data, tiffBE):
		return sniffTIFF(data)
	// HEIF/AVIF/JXL last; signatures do not collide with cases above.
	case isFtyp(data):
		return sniffISOBMFF(data)
	case isJXL(data):
		return sniffJXL(data)
	default:
		return ImageInfo{}, false
	}
}

var (
	pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	gif87    = []byte("GIF87a")
	gif89    = []byte("GIF89a")
	bmpMagic = []byte("BM")
	tiffLE   = []byte{'I', 'I', 0x2A, 0x00}
	tiffBE   = []byte{'M', 'M', 0x00, 0x2A}
	// JPEG XL codestream and container signatures.
	jxlCodestream = []byte{0xFF, 0x0A}
	jxlContainer  = []byte{0x00, 0x00, 0x00, 0x0C, 'J', 'X', 'L', ' ', 0x0D, 0x0A, 0x87, 0x0A}
)

// isobmffImageBrands maps ftyp brands to image MIME types. Movies/M4A are excluded.
var isobmffImageBrands = map[string]string{
	"heic": "image/heic", "heix": "image/heic",
	"mif1": "image/heif", "heim": "image/heif", "heis": "image/heif",
	"avif": "image/avif",
	"hevc": "image/heic-sequence", "hevx": "image/heic-sequence",
	"msf1": "image/heif-sequence", "hevm": "image/heif-sequence", "hevs": "image/heif-sequence",
	"avis": "image/avif-sequence",
}

// ImageExtension returns ".ext" for MIME types [SniffImage] reports, else "".
func ImageExtension(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/tiff":
		return ".tiff"
	case "image/heic":
		return ".heic"
	case "image/heif":
		return ".heif"
	case "image/avif":
		return ".avif"
	case "image/jxl":
		return ".jxl"
	case "image/heic-sequence":
		return ".heics"
	case "image/heif-sequence":
		return ".heifs"
	case "image/avif-sequence":
		return ".avifs"
	}
	return ""
}

// isFtyp reports an ftyp box header.
func isFtyp(data []byte) bool {
	return len(data) >= 12 && string(data[4:8]) == "ftyp"
}

// sniffISOBMFF maps ftyp brands to image MIME. Unusable box size (0, 1, past buffer)
// scans major brand only, avoiding false positives in movie payload. No dimensions.
func sniffISOBMFF(data []byte) (ImageInfo, bool) {
	end := int(binary.BigEndian.Uint32(data[0:4]))
	if end < 16 || end > len(data) {
		end = 12 // major brand only
	}
	for pos := 8; pos+4 <= end; pos += 4 {
		if pos == 12 {
			continue // skip minor_version
		}
		if mime, ok := isobmffImageBrands[string(data[pos:pos+4])]; ok {
			return ImageInfo{MIME: mime}, true
		}
	}
	return ImageInfo{}, false
}

// isJXL reports a JPEG XL signature prefix.
func isJXL(data []byte) bool {
	return hasPrefix(data, jxlCodestream) || hasPrefix(data, jxlContainer)
}

// jxlMaxDim caps reported dimensions; larger values wrap on 32-bit builds.
const jxlMaxDim = 1<<31 - 1

// jxlRatios maps aspect-ratio selector to width:height (0 means explicit width).
var jxlRatios = [8][2]uint64{1: {1, 1}, 2: {12, 10}, 3: {4, 3}, 4: {3, 2}, 5: {16, 9}, 6: {5, 4}, 7: {2, 1}}

// sniffJXL accepts container by signature; codestream only if SizeHeader decodes (like JPEG SOF).
// Container reports MIME only; dimensions need a codestream box past header sniff.
func sniffJXL(data []byte) (ImageInfo, bool) {
	if hasPrefix(data, jxlContainer) {
		return ImageInfo{MIME: "image/jxl"}, true
	}
	b := &jxlBits{data: data[len(jxlCodestream):], ok: true}
	small := b.u(1) == 1
	height, width := uint64(0), uint64(0)
	if small {
		height = (uint64(b.u(5)) + 1) * 8
	} else {
		height = uint64(b.u32())
	}
	switch ratio := b.u(3); {
	case ratio == 0 && small:
		width = (uint64(b.u(5)) + 1) * 8
	case ratio == 0:
		width = uint64(b.u32())
	default:
		width = height * jxlRatios[ratio][0] / jxlRatios[ratio][1]
	}
	if !b.ok {
		return ImageInfo{}, false
	}
	info := ImageInfo{MIME: "image/jxl"}
	if width <= jxlMaxDim && height <= jxlMaxDim {
		info.Width, info.Height = int(width), int(height)
	}
	return info, true
}

// jxlBits reads JPEG XL bit-packed fields LSB-first. Overrun clears ok.
type jxlBits struct {
	data []byte
	pos  int // in bits
	ok   bool
}

func (b *jxlBits) u(n int) uint32 {
	var v uint32
	for i := range n {
		if b.pos>>3 >= len(b.data) {
			b.ok = false
			return 0
		}
		v |= uint32((b.data[b.pos>>3]>>(b.pos&7))&1) << i
		b.pos++
	}
	return v
}

// u32 reads SizeHeader U32: selector picks width; value is 1 + field.
func (b *jxlBits) u32() uint32 {
	widths := [4]int{9, 13, 18, 30}
	return 1 + b.u(widths[b.u(2)])
}

// isWebP reports RIFF....WEBP (not WAVE).
func isWebP(data []byte) bool {
	return len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

func hasPrefix(b, prefix []byte) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i := range prefix {
		if b[i] != prefix[i] {
			return false
		}
	}
	return true
}

// sniffPNG reads IHDR for dimensions and depth.
func sniffPNG(data []byte) (ImageInfo, bool) {
	// 8 magic + 4 len + 4 "IHDR" + 13 data
	if len(data) < 29 || string(data[12:16]) != "IHDR" {
		return ImageInfo{}, false
	}
	w := int(binary.BigEndian.Uint32(data[16:20]))
	h := int(binary.BigEndian.Uint32(data[20:24]))
	bitDepth := int(data[24])
	colorType := data[25]
	channels := map[byte]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[colorType]
	info := ImageInfo{MIME: "image/png", Width: w, Height: h, Depth: bitDepth * channels}
	if colorType == 3 { // indexed-color PNG: PLTE length / 3
		info.Colors = pngPaletteColors(data)
	}
	return info, true
}

// pngPaletteColors finds PLTE and returns entry count (len/3). Subtraction bounds
// hostile chunk lengths on 32-bit. Missing/truncated PLTE returns 0.
func pngPaletteColors(data []byte) int {
	for pos := 8; pos+8 <= len(data); {
		l := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		// l data + 4 CRC; negative l means int overflow on 32-bit
		if l < 0 || l > len(data)-pos-8-4 {
			break
		}
		if string(data[pos+4:pos+8]) == "PLTE" {
			return l / 3
		}
		pos += 8 + l + 4
	}
	return 0
}

// sniffJPEG scans for Start-Of-Frame dimensions.
func sniffJPEG(data []byte) (ImageInfo, bool) {
	i := 2 // skip SOI
	for i < len(data) {
		if data[i] != 0xFF {
			i++
			continue
		}
		// Skip 0xFF fill before the marker byte.
		for i < len(data) && data[i] == 0xFF {
			i++
		}
		if i >= len(data) {
			break
		}
		marker := data[i]
		i++
		// Standalone markers have no length field.
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 || marker == 0x00 {
			continue
		}
		if i+2 > len(data) {
			break
		}
		segLen := int(binary.BigEndian.Uint16(data[i : i+2]))
		if segLen < 2 {
			break
		}
		// SOF markers except DHT/JPG/DAC carry geometry.
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if i+7 >= len(data) {
				return ImageInfo{}, false
			}
			precision := int(data[i+2])
			h := int(binary.BigEndian.Uint16(data[i+3 : i+5]))
			w := int(binary.BigEndian.Uint16(data[i+5 : i+7]))
			components := int(data[i+7])
			return ImageInfo{MIME: "image/jpeg", Width: w, Height: h, Depth: precision * components}, true
		}
		i += segLen
	}
	// Require a readable SOF, not magic alone.
	return ImageInfo{}, false
}

// sniffGIF reads logical screen descriptor and GCT size.
func sniffGIF(data []byte) (ImageInfo, bool) {
	if len(data) < 13 {
		return ImageInfo{}, false
	}
	w := int(binary.LittleEndian.Uint16(data[6:8]))
	h := int(binary.LittleEndian.Uint16(data[8:10]))
	packed := data[10]
	// GCT size bits matter only when GCT present (high bit); else leave depth/colors 0.
	var depth, colors int
	if packed&0x80 != 0 {
		depth = int(packed&0x07) + 1
		colors = 1 << depth
	}
	return ImageInfo{MIME: "image/gif", Width: w, Height: h, Depth: depth, Colors: colors}, true
}

// sniffWebP reads canvas size from VP8/VP8L/VP8X chunk. Depth stays 0.
func sniffWebP(data []byte) (ImageInfo, bool) {
	info := ImageInfo{MIME: "image/webp"}
	if len(data) < 16 {
		return info, true
	}
	switch string(data[12:16]) {
	case "VP8 ":
		if len(data) >= 30 && data[23] == 0x9d && data[24] == 0x01 && data[25] == 0x2a {
			info.Width = int(binary.LittleEndian.Uint16(data[26:28]) & 0x3FFF)
			info.Height = int(binary.LittleEndian.Uint16(data[28:30]) & 0x3FFF)
		}
	case "VP8L":
		if len(data) >= 25 && data[20] == 0x2f {
			b := binary.LittleEndian.Uint32(data[21:25])
			info.Width = int(b&0x3FFF) + 1
			info.Height = int((b>>14)&0x3FFF) + 1
		}
	case "VP8X":
		if len(data) >= 30 {
			info.Width = (int(data[24]) | int(data[25])<<8 | int(data[26])<<16) + 1
			info.Height = (int(data[27]) | int(data[28])<<8 | int(data[29])<<16) + 1
		}
	}
	return info, true
}

// sniffBMP reads DIB dimensions/depth. Headers >=16 share offsets 18/22/28.
// Negative height is top-down; take magnitude (hostile width sign too).
func sniffBMP(data []byte) (ImageInfo, bool) {
	info := ImageInfo{MIME: "image/bmp"}
	if len(data) < 18 {
		return info, true
	}
	switch dibSize := binary.LittleEndian.Uint32(data[14:18]); {
	case dibSize >= 16 && len(data) >= 30:
		// Signed w/h: normalize to magnitude for unsigned storage.
		w := int(int32(binary.LittleEndian.Uint32(data[18:22])))
		if w < 0 {
			w = -w
		}
		info.Width = w
		h := int(int32(binary.LittleEndian.Uint32(data[22:26])))
		if h < 0 {
			h = -h
		}
		info.Height = h
		info.Depth = int(binary.LittleEndian.Uint16(data[28:30]))
	case dibSize == 12 && len(data) >= 26:
		info.Width = int(binary.LittleEndian.Uint16(data[18:20]))
		info.Height = int(binary.LittleEndian.Uint16(data[20:22]))
		info.Depth = int(binary.LittleEndian.Uint16(data[24:26]))
	}
	return info, true
}

// sniffTIFF best-effort reads ImageWidth/ImageLength from first IFD.
func sniffTIFF(data []byte) (ImageInfo, bool) {
	info := ImageInfo{MIME: "image/tiff"}
	bo := binary.ByteOrder(binary.BigEndian)
	if data[0] == 'I' {
		bo = binary.LittleEndian
	}
	if len(data) < 8 {
		return info, true
	}
	// ifd > len(data)-2 avoids uint32 overflow panic on 32-bit (not ifd+2 > len).
	ifd := int(bo.Uint32(data[4:8]))
	if ifd < 8 || ifd > len(data)-2 {
		return info, true
	}
	count := int(bo.Uint16(data[ifd : ifd+2]))
	for i, entry := 0, ifd+2; i < count && entry+12 <= len(data); i, entry = i+1, entry+12 {
		field := bo.Uint16(data[entry : entry+2])
		typ := bo.Uint16(data[entry+2 : entry+4])
		// Inline value only when count==1; else field is offset.
		if bo.Uint32(data[entry+4:entry+8]) != 1 {
			continue
		}
		switch field {
		case 0x0100: // ImageWidth
			info.Width = int(tiffShortOrLong(bo, typ, data[entry+8:entry+12]))
		case 0x0101: // ImageLength (height)
			info.Height = int(tiffShortOrLong(bo, typ, data[entry+8:entry+12]))
		}
	}
	return info, true
}

// tiffShortOrLong reads SHORT or LONG inline IFD value.
func tiffShortOrLong(bo binary.ByteOrder, typ uint16, b []byte) uint32 {
	switch typ {
	case 3: // SHORT
		return uint32(bo.Uint16(b[:2]))
	case 4: // LONG
		return bo.Uint32(b[:4])
	default:
		return 0
	}
}
