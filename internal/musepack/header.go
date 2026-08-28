package musepack

import (
	"encoding/binary"
	"fmt"

	"github.com/colespringer/waxlabel/waxerr"
)

// Musepack has two incompatible stream formats. SV7 opens with "MP+" and a version
// byte and puts the whole description in one fixed header; SV8 opens with "MPCK" and
// is a stream of keyed packets, the first of which ("SH") carries the description.
// Both are common in real libraries, so both are read.
const (
	sv7Magic = "MP+"
	sv8Magic = "MPCK"

	// sv7HeaderLen is the leading region SV7 files always have: the magic and version
	// byte, the frame count, and 16 bytes of stream configuration.
	sv7HeaderLen = 24
	// sv7FrameSamples is SV7's fixed decoded frame size. The format stores a frame
	// count rather than a sample count, and the final frame's true length is not
	// recorded, so a whole-frame product is what every decoder reports.
	sv7FrameSamples = 1152
	// sv7Version and sv7VersionAlt are the two stream-version bytes SV7 files carry.
	sv7Version    = 0x07
	sv7VersionAlt = 0x17
)

// sampleRates is the four-entry rate table both stream versions index into.
var sampleRates = [4]int{44100, 48000, 37800, 32000}

// header is the decoded stream description, assembled from whichever version the
// file uses so the rest of the codec sees one shape.
type header struct {
	streamVersion int
	sampleRate    int
	channels      int
	totalSamples  uint64
	// headerLen is the on-disk length of the description region measured from the
	// start of the Musepack stream: the earliest offset a trailing tag could begin at.
	headerLen int64
}

// parseHeader decodes whichever Musepack header b begins with.
func parseHeader(b []byte) (header, error) {
	switch {
	case len(b) >= 4 && string(b[0:4]) == sv8Magic:
		return parseSV8(b)
	case len(b) >= 4 && string(b[0:3]) == sv7Magic:
		return parseSV7(b)
	}
	return header{}, fmt.Errorf("%w: missing the MPCK or MP+ stream marker", waxerr.ErrInvalidData)
}

// parseSV7 decodes the fixed SV7 header: "MP+", the version byte, a 32-bit frame
// count, and a 16-byte configuration block whose third byte holds the sample-rate
// index in its low two bits. SV7 is always two channels.
func parseSV7(b []byte) (header, error) {
	if len(b) < sv7HeaderLen {
		return header{}, fmt.Errorf("%w: SV7 header is %d bytes, need %d", waxerr.ErrInvalidData, len(b), sv7HeaderLen)
	}
	version := int(b[3])
	if version != sv7Version && version != sv7VersionAlt {
		return header{}, fmt.Errorf("%w: Musepack SV7 stream version %#02x", waxerr.ErrUnsupportedFormat, version)
	}
	frames := binary.LittleEndian.Uint32(b[4:8])
	return header{
		streamVersion: 7,
		sampleRate:    sampleRates[b[10]&3],
		channels:      2,
		totalSamples:  uint64(frames) * sv7FrameSamples,
		headerLen:     sv7HeaderLen,
	}, nil
}

// parseSV8 walks the packet stream for the "SH" stream header, which must be the
// first packet after the magic. Later packets (seek table, replay gain, encoder
// info, audio) are not decoded here: the tag store is the trailing APEv2 tag, and
// the audio is copied verbatim.
func parseSV8(b []byte) (header, error) {
	pos := len(sv8Magic)
	for pos+2 <= len(b) {
		key := string(b[pos : pos+2])
		size, n, ok := readSize(b[pos+2:])
		if !ok || size < uint64(2+n) || size > uint64(len(b)-pos) {
			break
		}
		payload := b[pos+2+n : pos+int(size)]
		if key == "SH" {
			h, err := parseSV8StreamHeader(payload)
			if err != nil {
				return header{}, err
			}
			h.headerLen = int64(pos) + int64(size)
			return h, nil
		}
		pos += int(size)
	}
	return header{}, fmt.Errorf("%w: Musepack SV8 stream has no SH stream header", waxerr.ErrInvalidData)
}

// parseSV8StreamHeader decodes an SH packet payload: a CRC, the stream version, the
// sample and beginning-silence counts as variable-length numbers, then a bit-packed
// tail holding the sample-rate index, band count, channel count, and block size.
func parseSV8StreamHeader(b []byte) (header, error) {
	if len(b) < 5 {
		return header{}, fmt.Errorf("%w: SV8 stream header is %d bytes", waxerr.ErrInvalidData, len(b))
	}
	h := header{streamVersion: int(b[4])}
	pos := 5
	samples, n, ok := readSize(b[pos:])
	if !ok {
		return header{}, fmt.Errorf("%w: SV8 stream header has no sample count", waxerr.ErrInvalidData)
	}
	pos += n
	silence, n, ok := readSize(b[pos:])
	if !ok {
		return header{}, fmt.Errorf("%w: SV8 stream header has no beginning-silence count", waxerr.ErrInvalidData)
	}
	pos += n
	if pos+2 > len(b) {
		return header{}, fmt.Errorf("%w: SV8 stream header is truncated before its configuration", waxerr.ErrInvalidData)
	}
	// byte 0: sample-frequency index (3 bits) then max used bands (5 bits).
	// byte 1: channel count less one (4 bits), mid/side flag (1 bit), block size (3 of 5 bits).
	//
	// The index is three bits wide but only four values are defined; the rest are
	// reserved. Masking to two bits would alias a reserved index onto a real rate, so an
	// out-of-table value leaves the rate unset instead.
	if i := int(b[pos] >> 5); i < len(sampleRates) {
		h.sampleRate = sampleRates[i]
	}
	h.channels = int(b[pos+1]>>4) + 1
	if samples > silence {
		h.totalSamples = samples - silence
	}
	return h, nil
}

// readSize decodes Musepack's variable-length number: seven bits per byte,
// big-endian, with the high bit set on every byte but the last. It returns the value
// and the bytes consumed.
func readSize(b []byte) (uint64, int, bool) {
	var v uint64
	for i := 0; i < len(b); i++ {
		// Nine continuation bytes would be 63 bits; refuse a tenth rather than wrap.
		if i >= 9 {
			return 0, 0, false
		}
		v = v<<7 | uint64(b[i]&0x7F)
		if b[i]&0x80 == 0 {
			return v, i + 1, true
		}
	}
	return 0, 0, false
}
