package bits

import "testing"

// liboggKnownTable is the first 16 entries of libogg's published crc_lookup
// table (the official Ogg CRC vector). If our generated table matches these,
// the polynomial, bit order, and direction are correct.
var liboggKnownTable = [16]uint32{
	0x00000000, 0x04c11db7, 0x09823b6e, 0x0d4326d9,
	0x130476dc, 0x17c56b6b, 0x1a864db2, 0x1e475005,
	0x2608edb8, 0x22c9f00f, 0x2f8ad6d6, 0x2b4bcb61,
	0x350c9b64, 0x31cd86d3, 0x3c8ea00a, 0x384fbdbd,
}

func TestOggCRCTableMatchesLibogg(t *testing.T) {
	for i, want := range liboggKnownTable {
		if got := oggTable[i]; got != want {
			t.Errorf("oggTable[%d] = 0x%08x, want 0x%08x", i, got, want)
		}
	}
}

// bitwiseOggCRC is an independent, table-free reference (MSB-first, init 0, no
// xorout) used to cross-check the table-driven implementation.
func bitwiseOggCRC(p []byte) uint32 {
	var crc uint32
	for _, b := range p {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ oggPoly
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func TestOggCRCMatchesBitwise(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(""),
		[]byte("123456789"),
		[]byte("The quick brown fox jumps over the lazy dog"),
		{0x00},
		{0xFF, 0xFF, 0xFF, 0xFF},
		make([]byte, 257), // crosses the 256-entry table boundary
	}
	for i, c := range cases {
		if got, want := OggCRC(c), bitwiseOggCRC(c); got != want {
			t.Errorf("case %d: OggCRC = 0x%08x, bitwise = 0x%08x", i, got, want)
		}
	}
}

func TestOggCRCIncremental(t *testing.T) {
	full := []byte("The quick brown fox jumps over the lazy dog")
	whole := OggCRC(full)
	part := UpdateOggCRC(0, full[:10])
	part = UpdateOggCRC(part, full[10:])
	if whole != part {
		t.Errorf("incremental CRC 0x%08x != whole 0x%08x", part, whole)
	}
}
