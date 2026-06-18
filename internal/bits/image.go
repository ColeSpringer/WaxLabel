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

// SniffImage identifies PNG, JPEG, and GIF data and extracts dimensions. It
// reports ok=false for unrecognized or truncated data; callers should fall
// back to "application/octet-stream" and zero dimensions.
func SniffImage(data []byte) (ImageInfo, bool) {
	switch {
	case hasPrefix(data, pngMagic):
		return sniffPNG(data)
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return sniffJPEG(data)
	case hasPrefix(data, gif87) || hasPrefix(data, gif89):
		return sniffGIF(data)
	default:
		return ImageInfo{}, false
	}
}

var (
	pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	gif87    = []byte("GIF87a")
	gif89    = []byte("GIF89a")
)

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
