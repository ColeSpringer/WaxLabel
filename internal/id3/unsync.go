package id3

import "bytes"

// deunsync reverses ID3v2 unsynchronisation: every inserted 0x00 that follows a
// 0xFF is removed (0xFF 0x00 -> 0xFF), restoring the original bytes. The scheme
// exists so a tag never contains a false MPEG sync; we always write clean
// (un-unsynchronised) tags, so only the read side is needed.
func deunsync(b []byte) []byte {
	// Fast path: no stuffing sequence present, so there is nothing to undo. This
	// matches the actual 0xFF 0x00 pattern rather than a lone 0xFF, so audio-like
	// bodies full of 0xFF but never unsynchronised skip the copy entirely.
	if bytes.Index(b, []byte{0xFF, 0x00}) < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		out = append(out, b[i])
		if b[i] == 0xFF && i+1 < len(b) && b[i+1] == 0x00 {
			i++ // drop the stuffing byte
		}
	}
	return out
}
