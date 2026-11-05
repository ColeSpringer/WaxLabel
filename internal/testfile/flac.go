package testfile

import "slices"

// FLAC is "fLaC", a STREAMINFO (44100 Hz, 2 channels, 16-bit), the given blocks with the last one
// flagged (STREAMINFO when there are none), and a frame sync.
func FLAC(blocks ...[]byte) []byte {
	all := append([][]byte{FLACBlock(0, streamInfo())}, blocks...)
	last := slices.Clone(all[len(all)-1])
	last[0] |= 0x80
	all[len(all)-1] = last
	return slices.Concat([]byte("fLaC"), slices.Concat(all...), []byte{0xFF, 0xF8})
}

// FLACBlock is an unflagged metadata block; FLAC and OggFLAC flag the last one.
func FLACBlock(code byte, body []byte) []byte {
	return append([]byte{code, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
}

// VorbisComments is a Vorbis comment list: the vendor, then each entry as given, with no framing
// bit.
func VorbisComments(vendor string, entries ...string) []byte {
	out := slices.Concat(LE32(len(vendor)), []byte(vendor), LE32(len(entries)))
	for _, e := range entries {
		out = append(append(out, LE32(len(e))...), e...)
	}
	return out
}

// CommentBlock is a VORBIS_COMMENT block of NAME=value entries.
func CommentBlock(vendor string, entries ...string) []byte {
	return FLACBlock(4, VorbisComments(vendor, entries...))
}

// TaggedFLAC is a FLAC carrying one VORBIS_COMMENT block and a small padding.
func TaggedFLAC(vendor string, entries ...string) []byte {
	return FLAC(CommentBlock(vendor, entries...), FLACBlock(1, make([]byte, 4)))
}

func streamInfo() []byte {
	si := make([]byte, 34)
	si[0], si[2] = 0x10, 0x10 // 4096-sample blocks
	si[10], si[11], si[12], si[13] = 0x0A, 0xC4, 0x40|1<<1, 15<<4
	return si
}
