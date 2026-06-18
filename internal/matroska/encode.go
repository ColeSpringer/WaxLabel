package matroska

import (
	"hash/crc32"
)

// This file is the EBML writer: the inverse of the readVINT/readElement decoders
// in ebml.go. An element is ID ++ data-size-VINT ++ payload. Element IDs are
// emitted with their length-descriptor bits intact (the canonical ID form);
// data sizes are VINTs with the marker bit stripped from the value. Values
// (SeekPosition, CueClusterPosition) are plain big-endian unsigned integers, not
// VINTs. Reimplemented from EBML/RFC 8794; nothing is copied.

// idBytes returns an element ID's on-wire bytes. The ID already carries its
// length-descriptor bits, so its magnitude fixes the byte count (0x80–0xFE ⇒ 1
// byte … a 4-byte ID ⇒ 4), mirroring how readVINT(keepMarker=true) decoded it.
func idBytes(id uint64) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

// vintWidth returns the smallest VINT byte length that can hold n as a data
// size. A k-byte VINT stores 7k value bits, but the all-ones pattern is the
// reserved "unknown size" form, so a value that would fill k bytes exactly is
// pushed to k+1 — keeping every size we write a definite one.
func vintWidth(n uint64) int {
	for k := 1; k <= 8; k++ {
		if n < (uint64(1)<<(7*k))-1 {
			return k
		}
	}
	return 8
}

// sizeVINT encodes a data size as a minimal-width VINT.
func sizeVINT(n uint64) []byte {
	return sizeVINTWidth(n, vintWidth(n))
}

// sizeVINTWidth encodes a data size as a width-byte VINT (the marker bit is set
// in the first byte; the remaining bits hold the big-endian value). It is used
// both for minimal sizes and to re-encode a value at a fixed original width so a
// patched element does not change size. ok is false when n cannot fit width
// value bits (or would collide with the all-ones unknown form), so a caller that
// must preserve width can detect the rare overflow instead of corrupting.
func sizeVINTWidth(n uint64, width int) (out []byte) {
	out, _ = sizeVINTWidthOK(n, width)
	return out
}

func sizeVINTWidthOK(n uint64, width int) ([]byte, bool) {
	if width < 1 || width > 8 {
		return nil, false
	}
	maxVal := (uint64(1) << (7 * width)) - 1
	if n >= maxVal { // == maxVal is the unknown-size form; > cannot fit
		return nil, false
	}
	b := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		b[i] = byte(n)
		n >>= 8
	}
	b[0] |= 0x80 >> (width - 1) // length-descriptor / marker bit
	return b, true
}

// uintData encodes v as a big-endian unsigned integer in minWidth..8 bytes,
// widening only as needed. EBML integers are plain (no marker bit); this is the
// payload of a uint element or of a SeekPosition/CueClusterPosition.
func uintData(v uint64) []byte {
	w := 1
	for t := v >> 8; t != 0; t >>= 8 {
		w++
	}
	return uintDataWidth(v, w)
}

// uintDataWidth encodes v as a big-endian integer in exactly width bytes, or
// nil if it does not fit — used to patch a position in place at its original
// width so the surrounding element keeps its size.
func uintDataWidth(v uint64, width int) []byte {
	if width < 1 || width > 8 {
		return nil
	}
	if width < 8 && v >= (uint64(1)<<(8*width)) {
		return nil // would not fit the fixed width
	}
	b := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return b
}

// encElement builds an EBML element: ID ++ size-VINT ++ payload.
func encElement(id uint64, payload []byte) []byte {
	idb := idBytes(id)
	szb := sizeVINT(uint64(len(payload)))
	out := make([]byte, 0, len(idb)+len(szb)+len(payload))
	out = append(out, idb...)
	out = append(out, szb...)
	out = append(out, payload...)
	return out
}

// uintElement builds a uint element with a minimal-width big-endian value.
func uintElement(id, v uint64) []byte { return encElement(id, uintData(v)) }

// stringElement builds a UTF-8 string element.
func stringElement(id uint64, s string) []byte { return encElement(id, []byte(s)) }

// crcElement renders a CRC-32 element (ID 0xBF, 4-byte little-endian value) over
// content. Matroska's CRC-32 is the IEEE polynomial (zlib's crc32) stored
// little-endian — verified against the real fixtures' stored CRCs.
func crcElement(content []byte) []byte {
	sum := crc32.ChecksumIEEE(content)
	return []byte{idCRC32 & 0xFF, 0x84, byte(sum), byte(sum >> 8), byte(sum >> 16), byte(sum >> 24)}
}

// withCRC prepends a CRC-32 element computed over payload, returning the master
// element's content (CRC element ++ payload) — the form mkvmerge writes, where
// the CRC covers everything in the master after itself.
func withCRC(payload []byte) []byte {
	crc := crcElement(payload)
	out := make([]byte, 0, len(crc)+len(payload))
	out = append(out, crc...)
	out = append(out, payload...)
	return out
}

// masterElement builds a master element from rendered children, optionally
// guarded by a leading CRC-32 (when the source element carried one, so the
// rewrite preserves that integrity convention).
func masterElement(id uint64, children []byte, crc bool) []byte {
	if crc {
		return encElement(id, withCRC(children))
	}
	return encElement(id, children)
}
