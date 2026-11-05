// Package testfile builds small media files for tests.
package testfile

import "encoding/binary"

func LE16(n int) []byte { return binary.LittleEndian.AppendUint16(nil, uint16(n)) }
func LE32(n int) []byte { return binary.LittleEndian.AppendUint32(nil, uint32(n)) }
func BE16(n int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(n)) }
func BE32(n int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(n)) }

// PNG is a 1x1 RGBA PNG header with no IDAT: enough to sniff and to round-trip byte-exact.
func PNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
	}
}

// GIF is a 3x5 GIF89a header with no global color table.
func GIF() []byte { return append([]byte("GIF89a"), 0x03, 0x00, 0x05, 0x00, 0x77, 0x00, 0x00) }

// JPEG is a complete JPEG header: SOI and a 3x5 SOF0.
func JPEG() []byte {
	return []byte{
		0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08,
		0x00, 0x05, 0x00, 0x03, 0x03,
		0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01,
	}
}
