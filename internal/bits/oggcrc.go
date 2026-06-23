package bits

// Ogg uses a CRC-32 that is *not* the reflected IEEE variant Go's
// hash/crc32 provides: polynomial 0x04C11DB7, initial value 0, no input or
// output reflection, and no final XOR (MSB-first). Using crc32.MakeTable here
// would silently produce wrong checksums, so we build the table ourselves.
//
// The table is validated against libogg's published crc_lookup values in tests and
// lives in the shared bits package because both Ogg Vorbis and Ogg Opus need it.
const oggPoly = 0x04C11DB7

var oggTable = makeOggTable()

func makeOggTable() *[256]uint32 {
	var t [256]uint32
	for n := 0; n < 256; n++ {
		c := uint32(n) << 24
		for k := 0; k < 8; k++ {
			if c&0x80000000 != 0 {
				c = (c << 1) ^ oggPoly
			} else {
				c <<= 1
			}
		}
		t[n] = c
	}
	return &t
}

// OggCRC returns the Ogg CRC-32 of p (init 0, no final XOR).
func OggCRC(p []byte) uint32 {
	return UpdateOggCRC(0, p)
}

// UpdateOggCRC continues an Ogg CRC over additional bytes, so a page checksum
// can be computed without concatenating the header and body.
func UpdateOggCRC(crc uint32, p []byte) uint32 {
	for _, b := range p {
		crc = (crc << 8) ^ oggTable[byte(crc>>24)^b]
	}
	return crc
}

// UpdateOggCRCZeros continues an Ogg CRC over n zero bytes without allocating a
// zero buffer. It is the hot path for CRC "patching": because this CRC has
// init 0 and no final XOR it is linear (CRC(a^b) == CRC(a)^CRC(b)), so a page
// whose sequence number changed can have its checksum recomputed as
// oldCRC ^ CRC(delta-bytes followed by zeros to the page end) - see the Ogg
// codec's page-renumber path.
func UpdateOggCRCZeros(crc uint32, n int64) uint32 {
	for ; n > 0; n-- {
		crc = (crc << 8) ^ oggTable[byte(crc>>24)]
	}
	return crc
}
