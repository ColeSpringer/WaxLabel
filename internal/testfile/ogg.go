package testfile

import (
	"encoding/binary"
	"slices"
)

// oggPage lays one packet out as a single Ogg page.
func oggPage(flags byte, granule uint64, serial, seq uint32, pkt []byte) []byte {
	var lacing []byte
	n := len(pkt)
	for ; n >= 255; n -= 255 {
		lacing = append(lacing, 255)
	}
	lacing = append(lacing, byte(n))
	page := slices.Concat([]byte("OggS"), []byte{0, flags}, binary.LittleEndian.AppendUint64(nil, granule),
		binary.LittleEndian.AppendUint32(nil, serial), binary.LittleEndian.AppendUint32(nil, seq), make([]byte, 4),
		[]byte{byte(len(lacing))}, lacing, pkt)
	binary.LittleEndian.PutUint32(page[22:], oggCRC(page))
	return page
}

// OggFLAC is an Ogg FLAC stream: the identification packet with STREAMINFO, one header page per
// block with the last one flagged, and an audio page.
func OggFLAC(blocks ...[]byte) []byte {
	const serial = 0x57ac7557
	id := slices.Concat([]byte("\x7FFLAC\x01\x00"), BE16(len(blocks)), []byte("fLaC"), FLACBlock(0, streamInfo()))
	out := oggPage(0x02, 0, serial, 0, id)
	for i, b := range blocks {
		if i == len(blocks)-1 {
			b = slices.Clone(b)
			b[0] |= 0x80
		}
		out = append(out, oggPage(0, 0, serial, uint32(i+1), b)...)
	}
	return append(out, oggPage(0, 4096, serial, uint32(len(blocks)+1), []byte("AUDIO-FRAME-BYTES"))...)
}

// oggCRC is CRC-32 with polynomial 0x04c11db7, no reflection, zero init and no final XOR.
func oggCRC(b []byte) uint32 {
	var crc uint32
	for _, c := range b {
		crc ^= uint32(c) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
