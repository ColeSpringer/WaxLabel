package testfile

import (
	"encoding/binary"
	"math/bits"
	"slices"
)

// AIFF is a FORM file of formType ("AIFF" or "AIFC").
func AIFF(formType string, chunks ...[]byte) []byte {
	body := slices.Concat([]byte(formType), slices.Concat(chunks...))
	return slices.Concat([]byte("FORM"), BE32(len(body)), body)
}

func AIFFChunk(id string, body []byte) []byte {
	out := slices.Concat([]byte(id), BE32(len(body)), body)
	if len(body)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

// Rate80 is an integer sample rate as the 80-bit extended float COMM stores it.
func Rate80(rate uint32) []byte {
	b := make([]byte, 10)
	if rate == 0 {
		return b
	}
	shift := bits.LeadingZeros64(uint64(rate))
	binary.BigEndian.PutUint16(b, uint16(16383+63-shift))
	binary.BigEndian.PutUint64(b[2:], uint64(rate)<<shift)
	return b
}

// COMM is a plain-AIFF 18-byte COMM chunk.
func COMM(channels, numFrames, sampleSize int, rate uint32) []byte {
	return AIFFChunk("COMM", slices.Concat(BE16(channels), BE32(numFrames), BE16(sampleSize), Rate80(rate)))
}

// COMMC is an AIFF-C COMM chunk: the common fields, a compression type and an empty name.
func COMMC(channels, numFrames, sampleSize int, rate uint32, compType string) []byte {
	return COMMCFrames(channels, uint32(numFrames), sampleSize, rate, compType)
}

// COMMCFrames is COMMC with the frame count as the uint32 the field is, for a count no int holds on
// a 32-bit build.
func COMMCFrames(channels int, numFrames uint32, sampleSize int, rate uint32, compType string) []byte {
	return AIFFChunk("COMM", slices.Concat(BE16(channels), binary.BigEndian.AppendUint32(nil, numFrames),
		BE16(sampleSize), Rate80(rate), []byte(compType), []byte{0, 0}))
}

// SSNDOffset is an SSND chunk whose offset field says align bytes precede the samples.
func SSNDOffset(offset int, align, samples []byte) []byte {
	return AIFFChunk("SSND", slices.Concat(BE32(offset), BE32(0), align, samples))
}

// TruncatedSSND declares declared body bytes but holds only present, unpadded: a cut-off AIFF.
func TruncatedSSND(declared, present int) []byte {
	return slices.Concat([]byte("SSND"), BE32(declared), make([]byte, present))
}
