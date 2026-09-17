package bits

// Ogg CRC-32: poly 0x04C11DB7, init 0, MSB-first, no reflection or final XOR.
// Not Go's hash/crc32. Validated against libogg in tests.
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

// UpdateOggCRC continues an Ogg CRC.
func UpdateOggCRC(crc uint32, p []byte) uint32 {
	for _, b := range p {
		crc = (crc << 8) ^ oggTable[byte(crc>>24)^b]
	}
	return crc
}

// UpdateOggCRCZeros CRCs n zero bytes without allocation. Linear CRC (init 0, no xorout)
// supports page checksum patching after sequence renumber.
func UpdateOggCRCZeros(crc uint32, n int64) uint32 {
	for ; n > 0; n-- {
		crc = (crc << 8) ^ oggTable[byte(crc>>24)]
	}
	return crc
}
