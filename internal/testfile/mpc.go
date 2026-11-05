package testfile

import (
	"encoding/binary"
	"hash/crc32"
	"slices"
)

// MPCVarlen is Musepack's variable-length number: seven bits per byte, big-endian, the high bit
// set on every byte but the last.
func MPCVarlen(n uint64) []byte {
	out := []byte{byte(n & 0x7F)}
	for n >>= 7; n > 0; n >>= 7 {
		out = append([]byte{byte(n&0x7F) | 0x80}, out...)
	}
	return out
}

// MPCPacket frames an SV8 packet: its key, its varlen total size, and the payload.
func MPCPacket(key string, payload []byte) []byte {
	total := 3 + len(payload)
	for {
		size := MPCVarlen(uint64(total))
		if 2+len(size)+len(payload) == total {
			return slices.Concat([]byte(key), size, payload)
		}
		total = 2 + len(size) + len(payload)
	}
}

// MPCStream is an SV8 file: a stream header with the given geometry and a valid CRC, then packets.
func MPCStream(samples uint64, rateIndex, channels int, packets ...[]byte) []byte {
	sh := slices.Concat([]byte{0, 0, 0, 0, 8}, MPCVarlen(samples), MPCVarlen(0),
		[]byte{byte(rateIndex<<5) | 20, byte((channels - 1) << 4), 0})
	binary.BigEndian.PutUint32(sh, crc32.ChecksumIEEE(sh[4:]))
	return slices.Concat([]byte("MPCK"), MPCPacket("SH", sh), slices.Concat(packets...))
}

// MPCChapterTag is the tag a chapter packet carries: the APEv2 header record minus its preamble,
// then the items.
func MPCChapterTag(items ...[2]string) []byte {
	if len(items) == 0 {
		return nil
	}
	var body []byte
	for _, it := range items {
		body = append(body, slices.Concat(LE32(len(it[1])), LE32(0), []byte(it[0]), []byte{0}, []byte(it[1]))...)
	}
	head := slices.Concat(LE32(2000), LE32(len(body)), LE32(len(items)),
		binary.LittleEndian.AppendUint32(nil, 0xA0000000), make([]byte, 8))
	return append(head, body...)
}

// MPCChapter is a CT packet starting at sample.
func MPCChapter(sample uint64, tag []byte) []byte {
	return MPCPacket("CT", slices.Concat(MPCVarlen(sample), []byte{0, 0, 0, 0}, tag))
}

func MPCTitled(sample uint64, title string) []byte {
	return MPCChapter(sample, MPCChapterTag([2]string{"Title", title}))
}

func MPCAudio(n int) []byte { return MPCPacket("AP", make([]byte, n)) }

func MPCEnd() []byte { return MPCPacket("SE", nil) }

// MPCChaptered is a 120 s stereo SV8 stream with three titled chapters stored out of time order.
func MPCChaptered() []byte {
	return MPCStream(44100*120, 0, 2, MPCAudio(64),
		MPCTitled(0, "Intro"), MPCTitled(44100*90, "Finale"), MPCTitled(44100*30, "Middle"), MPCEnd())
}
