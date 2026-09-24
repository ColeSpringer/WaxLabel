package id3

import "bytes"

// deunsync reverses ID3v2 unsynchronisation: every 0x00 inserted after a 0xFF
// is removed (0xFF 0x00 -> 0xFF). The scheme keeps a false MPEG sync out of a
// tag; Render writes clean tags, so only the read side is needed.
func deunsync(b []byte) []byte {
	// Fast path: no 0xFF 0x00 stuffing sequence, so nothing to undo. Matching
	// the pair, not a lone 0xFF, lets audio-like bodies full of 0xFF skip the
	// copy.
	if !bytes.Contains(b, []byte{0xFF, 0x00}) {
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
