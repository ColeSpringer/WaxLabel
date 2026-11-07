package testfile

import (
	"fmt"
	"slices"
)

func Syncsafe(n int) []byte {
	return []byte{byte(n>>21) & 0x7f, byte(n>>14) & 0x7f, byte(n>>7) & 0x7f, byte(n) & 0x7f}
}

// ID3v2 wraps frames in an ID3v2 tag header of the given major version (3 for v2.3).
func ID3v2(version byte, frames ...[]byte) []byte {
	body := slices.Concat(frames...)
	return slices.Concat([]byte{'I', 'D', '3', version, 0, 0}, Syncsafe(len(body)), body)
}

// ID3Frame wraps a body in a v2.3/v2.4 frame header: v2.4 sizes are sync-safe, v2.3 sizes plain.
func ID3Frame(version byte, id string, body []byte) []byte {
	size := BE32(len(body))
	if version >= 4 {
		size = Syncsafe(len(body))
	}
	return slices.Concat([]byte(id), size, []byte{0, 0}, body)
}

// TextFrame is a Latin-1 text frame.
func TextFrame(version byte, id, text string) []byte {
	return ID3Frame(version, id, append([]byte{0}, text...))
}

// FrontCover is a v2.3 APIC front-cover frame.
func FrontCover(mime string, data []byte) []byte {
	return ID3Frame(3, "APIC", slices.Concat([]byte{0}, []byte(mime), []byte{0, 3, 0}, data))
}

// OverrunFrame is a frame header whose size runs past the end of the tag it sits in, then a short
// body.
func OverrunFrame(id string) []byte {
	return slices.Concat([]byte(id), Syncsafe(1<<20), []byte{0, 0}, []byte("short"))
}

// ID3v1 is a 128-byte ID3v1 trailer.
func ID3v1(title, artist, album, year, comment string, genre byte) []byte {
	b := make([]byte, 128)
	copy(b, "TAG")
	copy(b[3:33], title)
	copy(b[33:63], artist)
	copy(b[63:93], album)
	copy(b[93:97], year)
	copy(b[97:127], comment)
	b[127] = genre
	return b
}

// Lyrics3v1 is a Lyrics3 v1.00 block, the lyrics between LYRICSBEGIN and LYRICSEND.
func Lyrics3v1(lyrics string) []byte {
	return slices.Concat([]byte("LYRICSBEGIN"), []byte(lyrics), []byte("LYRICSEND"))
}

// Lyrics3v2 is a Lyrics3 v2.00 block holding fields, each a three-letter ID and its value.
func Lyrics3v2(fields ...[2]string) []byte {
	b := []byte("LYRICSBEGIN")
	for _, f := range fields {
		b = fmt.Appendf(b, "%s%05d%s", f[0], len(f[1]), f[1])
	}
	return fmt.Appendf(b, "%06dLYRICS200", len(b))
}
