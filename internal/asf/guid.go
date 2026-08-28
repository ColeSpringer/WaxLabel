package asf

import "encoding/binary"

// ASF identifies every object by a 128-bit GUID stored in the mixed-endian
// Microsoft layout: the first three fields little-endian, the last eight bytes in
// order. The constants below are the on-disk byte sequences, so no conversion is
// needed at read time.
type guid [16]byte

// asfGUID builds the on-disk byte sequence from the canonical
// {d1-d2-d3-d4} textual form's numeric fields.
func asfGUID(d1 uint32, d2, d3 uint16, tail [8]byte) guid {
	var g guid
	binary.LittleEndian.PutUint32(g[0:4], d1)
	binary.LittleEndian.PutUint16(g[4:6], d2)
	binary.LittleEndian.PutUint16(g[6:8], d3)
	copy(g[8:16], tail[:])
	return g
}

// The two GUID tails every object in this file uses.
var (
	tailASF  = [8]byte{0xA6, 0xD9, 0x00, 0xAA, 0x00, 0x62, 0xCE, 0x6C}
	tailHdr  = [8]byte{0x8E, 0xE4, 0x00, 0xC0, 0x0C, 0x20, 0x53, 0x65}
	tailHdr3 = [8]byte{0x8E, 0xE3, 0x00, 0xC0, 0x0C, 0x20, 0x53, 0x65}
	tailHdr6 = [8]byte{0x8E, 0xE6, 0x00, 0xC0, 0x0C, 0x20, 0x53, 0x65}
)

var (
	// guidHeader is the top-level Header Object, which is also the file signature.
	guidHeader = asfGUID(0x75B22630, 0x668E, 0x11CF, tailASF)
	// guidFileProps carries the play duration, preroll, and file size.
	guidFileProps = asfGUID(0x8CABDCA1, 0xA947, 0x11CF, tailHdr)
	// guidStreamProps carries the per-stream type and its WAVEFORMATEX.
	guidStreamProps = asfGUID(0xB7DC0791, 0xA9B7, 0x11CF, tailHdr6)
	// guidHeaderExt nests the Metadata and Metadata Library objects.
	guidHeaderExt = asfGUID(0x5FBF03B5, 0xA92E, 0x11CF, tailHdr3)
	// guidContentDesc is the five fixed text fields (title, author, copyright,
	// description, rating).
	guidContentDesc = asfGUID(0x75B22633, 0x668E, 0x11CF, tailASF)
	// guidExtContentDesc is the open-ended "WM/*" descriptor list where everything
	// beyond those five fields lives, cover art included.
	guidExtContentDesc = asfGUID(0xD2D0A440, 0xE307, 0x11D2, [8]byte{0x97, 0xF0, 0x00, 0xA0, 0xC9, 0x5E, 0xA8, 0x50})
	// guidMetadata and guidMetadataLibrary are the per-stream descriptor lists inside
	// the Header Extension object.
	guidMetadata        = asfGUID(0xC5F8CBEA, 0x5BAF, 0x4877, [8]byte{0x84, 0x67, 0xAA, 0x8C, 0x44, 0xFA, 0x4C, 0xCA})
	guidMetadataLibrary = asfGUID(0x44231C94, 0x9498, 0x49D1, [8]byte{0xA1, 0x41, 0x1D, 0x13, 0x4E, 0x45, 0x70, 0x54})
	// guidData is the top-level Data Object holding the media packets. It is a sibling
	// of the Header Object, not a child, so it is located by stepping past the header.
	guidData = asfGUID(0x75B22636, 0x668E, 0x11CF, tailASF)
	// guidCodecList names the codecs in use; read only for the native view.
	guidCodecList = asfGUID(0x86D15240, 0x311D, 0x11D0, [8]byte{0xA3, 0xA4, 0x00, 0xA0, 0xC9, 0x03, 0x48, 0xF6})
	// guidStreamBitrate carries per-stream average bitrates; native view only.
	guidStreamBitrate = asfGUID(0x7BF875CE, 0x468D, 0x11D1, [8]byte{0x8D, 0x82, 0x00, 0x60, 0x97, 0xC9, 0xA2, 0xB2})
	// guidContentEncryption and guidExtContentEncryption mark DRM-protected content.
	guidContentEncryption    = asfGUID(0x2211B3FB, 0xBD23, 0x11D2, [8]byte{0xB4, 0xB7, 0x00, 0xA0, 0xC9, 0x55, 0xFC, 0x6E})
	guidExtContentEncryption = asfGUID(0x298AE614, 0x2622, 0x4C17, [8]byte{0xB9, 0x35, 0xDA, 0xE0, 0x7E, 0xE9, 0x28, 0x9C})
	// guidAudioMedia marks a Stream Properties object as describing audio.
	guidAudioMedia = asfGUID(0xF8699E40, 0x5B4D, 0x11CF, [8]byte{0xA8, 0xFD, 0x00, 0x80, 0x5F, 0x5C, 0x44, 0x2B})
)
