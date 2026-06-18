package bits

import "encoding/binary"

// ImageInfo is the result of sniffing an embedded picture: its MIME type and,
// when derivable cheaply from the header, pixel dimensions and color depth.
// WaxLabel never decodes pixels; it only reads headers, so a caller can fill a
// FLAC PICTURE block's width/height/depth without an image library.
type ImageInfo struct {
	MIME   string
	Width  int
	Height int
	Depth  int // bits per pixel across all channels; 0 if unknown
}

// SniffImage identifies PNG, JPEG, GIF, WebP, BMP, and TIFF data and extracts
// dimensions where the header carries them cheaply. It reports ok=false for
// unrecognized or truncated data; callers should fall back to
// "application/octet-stream" and zero dimensions. Dimension extraction is
// best-effort for the RIFF/IFD-based formats (WebP, TIFF): a recognized header
// always yields the correct MIME even when the size fields cannot be read.
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
)

// isWebP reports whether data is a RIFF container carrying a WEBP form: "RIFF",
// a 4-byte size, then "WEBP". The form-type check keeps a WAV file (RIFF...WAVE)
// from matching.
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

// sniffPNG reads the IHDR chunk (always first): width, height, bit depth, and
// color type, which together give bits per pixel.
func sniffPNG(data []byte) (ImageInfo, bool) {
	// 8 magic + 4 length + 4 "IHDR" + 13 IHDR data = 29 bytes minimum.
	if len(data) < 29 || string(data[12:16]) != "IHDR" {
		return ImageInfo{}, false
	}
	w := int(binary.BigEndian.Uint32(data[16:20]))
	h := int(binary.BigEndian.Uint32(data[20:24]))
	bitDepth := int(data[24])
	colorType := data[25]
	channels := map[byte]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[colorType]
	return ImageInfo{MIME: "image/png", Width: w, Height: h, Depth: bitDepth * channels}, true
}

// sniffJPEG scans marker segments for a Start-Of-Frame, which carries sample
// precision, height, and width.
func sniffJPEG(data []byte) (ImageInfo, bool) {
	i := 2 // skip SOI
	for i < len(data) {
		if data[i] != 0xFF {
			i++
			continue
		}
		// A marker is 0xFF followed by a non-0xFF byte; runs of 0xFF are fill
		// padding and must be skipped, or a 0xFF run is mistaken for a marker.
		for i < len(data) && data[i] == 0xFF {
			i++
		}
		if i >= len(data) {
			break
		}
		marker := data[i]
		i++ // i now points at the segment (length field), if any
		// Standalone markers (SOI, EOI, RSTn, TEM, and 0x00 byte stuffing)
		// carry no length.
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 || marker == 0x00 {
			continue
		}
		if i+2 > len(data) {
			break
		}
		segLen := int(binary.BigEndian.Uint16(data[i : i+2]))
		if segLen < 2 {
			break // malformed length (must include its own 2 bytes)
		}
		// SOF0-SOF15 except DHT(C4), JPG(C8), DAC(CC) carry frame geometry.
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if i+7 >= len(data) {
				return ImageInfo{MIME: "image/jpeg"}, true
			}
			precision := int(data[i+2])
			h := int(binary.BigEndian.Uint16(data[i+3 : i+5]))
			w := int(binary.BigEndian.Uint16(data[i+5 : i+7]))
			components := int(data[i+7])
			return ImageInfo{MIME: "image/jpeg", Width: w, Height: h, Depth: precision * components}, true
		}
		i += segLen // length field includes its own 2 bytes
	}
	// Recognized as JPEG even if no SOF was located.
	return ImageInfo{MIME: "image/jpeg"}, true
}

// sniffGIF reads the logical-screen descriptor for dimensions and the global
// color table size for depth.
func sniffGIF(data []byte) (ImageInfo, bool) {
	if len(data) < 13 {
		return ImageInfo{}, false
	}
	w := int(binary.LittleEndian.Uint16(data[6:8]))
	h := int(binary.LittleEndian.Uint16(data[8:10]))
	packed := data[10]
	depth := int(packed&0x07) + 1 // bits per primary color in the GCT
	return ImageInfo{MIME: "image/gif", Width: w, Height: h, Depth: depth}, true
}

// sniffWebP reads dimensions from the first chunk after the WEBP form type. The
// three bitstream chunks (VP8 lossy, VP8L lossless, VP8X extended) each encode
// the canvas size differently; an unrecognized or truncated chunk yields just
// the MIME. WebP carries no simple bits-per-pixel, so Depth stays zero.
func sniffWebP(data []byte) (ImageInfo, bool) {
	info := ImageInfo{MIME: "image/webp"}
	if len(data) < 16 {
		return info, true
	}
	switch string(data[12:16]) {
	case "VP8 ": // lossy: frame tag(3), start code 9d 01 2a, then 14-bit w,h
		if len(data) >= 30 && data[23] == 0x9d && data[24] == 0x01 && data[25] == 0x2a {
			info.Width = int(binary.LittleEndian.Uint16(data[26:28]) & 0x3FFF)
			info.Height = int(binary.LittleEndian.Uint16(data[28:30]) & 0x3FFF)
		}
	case "VP8L": // lossless: signature 0x2f, then 14-bit (w-1),(h-1) packed
		if len(data) >= 25 && data[20] == 0x2f {
			b := binary.LittleEndian.Uint32(data[21:25])
			info.Width = int(b&0x3FFF) + 1
			info.Height = int((b>>14)&0x3FFF) + 1
		}
	case "VP8X": // extended: 4 flag bytes, then 24-bit canvas (w-1),(h-1)
		if len(data) >= 30 {
			info.Width = (int(data[24]) | int(data[25])<<8 | int(data[26])<<16) + 1
			info.Height = (int(data[27]) | int(data[28])<<8 | int(data[29])<<16) + 1
		}
	}
	return info, true
}

// sniffBMP reads the DIB header for dimensions and bit depth. It handles both the
// common BITMAPINFOHEADER (size >= 40) and the legacy BITMAPCOREHEADER (size 12);
// a top-down bitmap stores a negative height, which is normalized to its
// magnitude.
func sniffBMP(data []byte) (ImageInfo, bool) {
	info := ImageInfo{MIME: "image/bmp"}
	if len(data) < 18 {
		return info, true
	}
	switch dibSize := binary.LittleEndian.Uint32(data[14:18]); {
	case dibSize >= 40 && len(data) >= 30:
		// Width and height are signed: a negative height legitimately encodes a
		// top-down image, while a negative width is malformed. Either way take the
		// magnitude, so a hostile sign bit cannot propagate as a ~4.29e9 value when
		// the dimension is later stored as an unsigned 32-bit field.
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

// sniffTIFF reads the first image file directory for the ImageWidth/ImageLength
// tags. Byte order is set by the "II"/"MM" magic. It is best-effort: a directory
// past the buffer, or width/height in an unexpected field type, leaves the
// dimension zero while still reporting image/tiff.
func sniffTIFF(data []byte) (ImageInfo, bool) {
	info := ImageInfo{MIME: "image/tiff"}
	bo := binary.ByteOrder(binary.BigEndian)
	if data[0] == 'I' {
		bo = binary.LittleEndian
	}
	if len(data) < 8 {
		return info, true
	}
	ifd := int(bo.Uint32(data[4:8]))
	if ifd < 8 || ifd+2 > len(data) {
		return info, true
	}
	count := int(bo.Uint16(data[ifd : ifd+2]))
	for i, entry := 0, ifd+2; i < count && entry+12 <= len(data); i, entry = i+1, entry+12 {
		field := bo.Uint16(data[entry : entry+2])
		typ := bo.Uint16(data[entry+2 : entry+4])
		// The 4-byte value field holds the value inline only when the entry's count
		// is 1; for count > 1 it is a file offset to the values. ImageWidth and
		// ImageLength are single-valued, so a count other than 1 is malformed -
		// skip it rather than read the offset as a dimension.
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

// tiffShortOrLong reads a single dimension value from a TIFF IFD entry's 4-byte
// value field, which holds a SHORT (type 3, left-aligned) or LONG (type 4)
// inline. Any other type is not a dimension this sniffer understands.
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
