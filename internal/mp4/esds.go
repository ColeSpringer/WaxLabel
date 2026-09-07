package mp4

import "github.com/colespringer/waxlabel/internal/mpeg4audio"

// MPEG-4 descriptor tags (ISO/IEC 14496-1). An esds box holds one ES_Descriptor,
// which nests a DecoderConfigDescriptor, which nests the codec's own
// DecoderSpecificInfo - for AAC, the AudioSpecificConfig.
const (
	tagES                 = 0x03
	tagDecoderConfig      = 0x04
	tagDecoderSpecificInf = 0x05
)

// decoderConfigFixed is the DecoderConfigDescriptor's fixed part:
// objectTypeIndication (1), streamType (1), bufferSizeDB (3), maxBitrate (4),
// avgBitrate (4). The two bitrates are read past rather than reported: the track's
// bitrate is computed from the real byte span, which is honest for a VBR stream.
const decoderConfigFixed = 13

// esdsHeaderLen is the esds box header plus its FullBox version/flags word, which
// the descriptor walk starts after.
const esdsHeaderLen = 8 + 4

// aacLC is the audio object type HE-AAC is defined over, and so the only core an
// implicitly signalled SBR stream can have.
const aacLC = 2

// esdsConfig decodes the sample entry's esds box at b[off:end], returning the codec
// name and AudioSpecificConfig it declares. Anything malformed, truncated, or of a
// stream type this parser does not decode yields an empty config, which leaves the
// sample entry's own fields standing.
func esdsConfig(b []byte, off, end int) entryConfig {
	if end-off < esdsHeaderLen {
		return entryConfig{}
	}
	tag, bodyStart, bodyEnd, ok := readDescriptor(b, off+esdsHeaderLen, end)
	if !ok || tag != tagES {
		return entryConfig{}
	}
	// ES_Descriptor: ES_ID (2) and a flags byte selecting three optional fields.
	off = bodyStart + 3
	if off > bodyEnd {
		return entryConfig{}
	}
	flags := b[bodyStart+2]
	if flags&0x80 != 0 {
		off += 2 // dependsOn_ES_ID
	}
	if flags&0x40 != 0 {
		if off >= bodyEnd {
			return entryConfig{}
		}
		off += 1 + int(b[off]) // URLlength, then the URL itself
	}
	if flags&0x20 != 0 {
		off += 2 // OCR_ES_Id
	}
	if off > bodyEnd {
		return entryConfig{}
	}
	for off < bodyEnd {
		tag, start, stop, ok := readDescriptor(b, off, bodyEnd)
		if !ok {
			return entryConfig{}
		}
		if tag == tagDecoderConfig {
			return decoderConfig(b, start, stop)
		}
		off = stop
	}
	return entryConfig{}
}

// decoderConfig decodes a DecoderConfigDescriptor body at b[off:end]. The
// objectTypeIndication decides whether a DecoderSpecificInfo is worth looking for:
// only the four MPEG-4 audio indications carry an AudioSpecificConfig.
func decoderConfig(b []byte, off, end int) entryConfig {
	if end-off < decoderConfigFixed {
		return entryConfig{}
	}
	oti := b[off]
	off += decoderConfigFixed
	switch oti {
	case 0x69, 0x6B:
		// MPEG-2 and MPEG-1 audio in MP4. The indication names no layer, so neither can
		// the codec name; ffmpeg reports these the same way.
		return entryConfig{codec: "MP3"}
	case 0x40, 0x66, 0x67, 0x68:
	default:
		return entryConfig{}
	}
	for off < end {
		tag, start, stop, ok := readDescriptor(b, off, end)
		if !ok {
			return entryConfig{}
		}
		if tag == tagDecoderSpecificInf {
			asc, ok := mpeg4audio.ParseConfig(b[start:stop])
			if !ok {
				return entryConfig{}
			}
			cfg := entryConfig{asc: &asc}
			// The generic name carries no detail the four-cc does not, so leaving it empty
			// keeps the entry's own "mp4a" as the profile.
			if name := asc.ProfileName(); name != "AAC" {
				cfg.codec = name
			}
			return cfg
		}
		off = stop
	}
	return entryConfig{}
}

// readDescriptor reads one MPEG-4 descriptor header at b[off:end], returning its tag
// and the bounds of its body. The size is an expandable class length: up to four
// bytes, each contributing seven bits, with the top bit meaning "another byte
// follows". A fifth continuation, a body running past end, or a header that does not
// fit is not a descriptor, and the caller keeps the sample entry's own values.
func readDescriptor(b []byte, off, end int) (tag byte, bodyStart, bodyEnd int, ok bool) {
	if off < 0 || off >= end {
		return 0, 0, 0, false
	}
	tag = b[off]
	off++
	length := 0
	for i := range 4 {
		if off >= end {
			return 0, 0, 0, false
		}
		c := b[off]
		off++
		length = length<<7 | int(c&0x7F)
		if c&0x80 == 0 {
			break
		}
		if i == 3 {
			return 0, 0, 0, false
		}
	}
	if length > end-off {
		return 0, 0, 0, false
	}
	return tag, off, off + length, true
}
