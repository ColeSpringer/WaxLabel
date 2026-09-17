package musepack

import (
	"encoding/binary"
	"fmt"

	"github.com/colespringer/waxlabel/waxerr"
)

// Two stream formats: SV7 ("MP+" + version, fixed header) and SV8 ("MPCK", keyed
// packets; "SH" carries the description). Both are read.
const (
	sv7Magic = "MP+"
	sv8Magic = "MPCK"

	// sv7HeaderLen: magic, version, frame count, 16-byte config.
	sv7HeaderLen = 24
	// sv7FrameSamples: fixed decoded frame size. Format stores frame count only;
	// final-frame length is unknown, so whole-frame product is what decoders report.
	sv7FrameSamples = 1152
	sv7Version    = 0x07
	sv7VersionAlt = 0x17
)

// sampleRates is the four-entry table both versions index into.
var sampleRates = [4]int{44100, 48000, 37800, 32000}

// header is the decoded stream description in one shape for either version.
type header struct {
	streamVersion int
	sampleRate    int
	channels      int
	totalSamples  uint64
	// headerLen: description region from stream start (floor for a trailing tag).
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

// parseSV7: "MP+", version, frame count, config (rate index in byte 10 low 2 bits).
// Always two channels.
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

// parseSV8 walks for the first "SH" after the magic. Later packets are not decoded
// here (tags are trailing APEv2; audio is copied verbatim). Key must be two A-Z
// letters (reference decoder rule).
func parseSV8(b []byte) (header, error) {
	pos := len(sv8Magic)
	for pos < len(b) {
		p, ok := parsePacket(b[pos:], int64(len(b)-pos))
		if !ok {
			break
		}
		if p.key == "SH" {
			h, err := parseSV8StreamHeader(b[pos+p.hdrLen : pos+int(p.size)])
			if err != nil {
				return header{}, err
			}
			h.headerLen = int64(pos) + p.size
			return h, nil
		}
		pos += int(p.size)
	}
	return header{}, fmt.Errorf("%w: Musepack SV8 stream has no SH stream header", waxerr.ErrInvalidData)
}

// parseSV8StreamHeader: CRC, version (must be 8), varlen sample/silence counts,
// then bit-packed rate/bands/channels/block size.
func parseSV8StreamHeader(b []byte) (header, error) {
	if len(b) < 5 {
		return header{}, fmt.Errorf("%w: SV8 stream header is %d bytes", waxerr.ErrInvalidData, len(b))
	}
	if b[4] != 8 {
		return header{}, fmt.Errorf("%w: Musepack packet stream declares stream version %d", waxerr.ErrUnsupportedFormat, b[4])
	}
	h := header{streamVersion: 8}
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
	// byte 0: rate index (3 bits), max bands (5). byte 1: channels-1 (4), mid/side (1),
	// block size (3 of 5). Index is 3 bits but only four rates are defined; masking to
	// 2 bits would alias reserved indexes, so out-of-table leaves rate unset.
	if i := int(b[pos] >> 5); i < len(sampleRates) {
		h.sampleRate = sampleRates[i]
	}
	h.channels = int(b[pos+1]>>4) + 1
	if samples > silence {
		h.totalSamples = samples - silence
	}
	return h, nil
}

// readSize: 7 bits/byte, big-endian, high bit set on all but the last.
func readSize(b []byte) (uint64, int, bool) {
	var v uint64
	for i := 0; i < len(b); i++ {
		// Nine continuation bytes = 63 bits; refuse a tenth rather than wrap.
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
