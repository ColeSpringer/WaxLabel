package mp4

import (
	"fmt"

	"github.com/colespringer/waxlabel/internal/mpeg4audio"
)

// MPEG-4 descriptor tags (ISO/IEC 14496-1). An esds box holds one ES_Descriptor,
// which nests a DecoderConfigDescriptor, which nests the codec's own
// DecoderSpecificInfo, for AAC the AudioSpecificConfig.
const (
	tagES                 = 0x03
	tagDecoderConfig      = 0x04
	tagDecoderSpecificInf = 0x05
)

// decoderConfigFixed is the DecoderConfigDescriptor's fixed part:
// objectTypeIndication (1), streamType (1), bufferSizeDB (3), maxBitrate (4),
// avgBitrate (4). The two bitrates are skipped: the track's bitrate is computed from
// the byte span, which holds for a VBR stream.
const decoderConfigFixed = 13

// esdsHeaderLen is the esds box header plus its FullBox version/flags word, which
// the descriptor walk starts after.
const esdsHeaderLen = 8 + 4

// aacLC is the audio object type HE-AAC is defined over, and so the only core an
// implicitly signalled SBR stream can have.
const aacLC = 2

// The MPEG audio objectTypeIndications: ISO/IEC 13818-3 and 11172-3.
const (
	otiMPEG2Audio = 0x69
	otiMPEG1Audio = 0x6B
)

// objectTypeCodecs names the audio objectTypeIndications that carry no AudioSpecificConfig, as the
// MP4 registration authority lists them; 0xDD is ffmpeg's for Vorbis.
var objectTypeCodecs = map[byte]string{
	0xA5: "AC-3",
	0xA6: "E-AC-3",
	0xA9: "DTS",
	0xAA: "DTS-HD High Resolution",
	0xAB: "DTS-HD Master Audio",
	0xAC: "DTS Express",
	0xAD: "Opus",
	0xDD: "Vorbis",
	0xE1: "QCELP",
}

// mpegAudioLayer returns the codec name for the layer an MPEG audio frame header at b declares,
// "" when b opens with no such header.
func mpegAudioLayer(b []byte) string {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return ""
	}
	// A reserved version, bitrate or sample rate is no frame header.
	if b[1]>>3&3 == 1 || b[2]>>4 == 0xF || b[2]>>2&3 == 3 {
		return ""
	}
	switch b[1] >> 1 & 3 {
	case 3:
		return "MP1"
	case 2:
		return "MP2"
	case 1:
		return "MP3"
	}
	return ""
}

// esdsConfig decodes the sample entry's esds box at b[off:end], returning the codec
// name and AudioSpecificConfig it declares. Anything malformed, truncated, or of a
// stream type this parser does not decode yields an empty config, which leaves the
// sample entry's own fields standing. cut says the box runs past end: its descriptors
// are then read as far as they go, which reaches the object type.
func esdsConfig(b []byte, off, end int, cut bool) entryConfig {
	if end-off < esdsHeaderLen {
		return entryConfig{}
	}
	tag, bodyStart, bodyEnd, ok := readDescriptor(b, off+esdsHeaderLen, end, cut)
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
		tag, start, stop, ok := readDescriptor(b, off, bodyEnd, cut)
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
// only the four MPEG-4 audio indications carry an AudioSpecificConfig. Any other names
// the codec.
func decoderConfig(b []byte, off, end int) entryConfig {
	if end-off < decoderConfigFixed {
		return entryConfig{}
	}
	oti := b[off]
	off += decoderConfigFixed
	switch oti {
	case otiMPEG2Audio, otiMPEG1Audio:
		// The indication names no layer; the first frame does (mpegAudioLayer).
		return entryConfig{codec: "MP3"}
	case 0x40, 0x66, 0x67, 0x68:
	default:
		// mp4a is the MPEG-4 Systems wrapper, so calling the stream AAC by its four-cc
		// would be wrong for anything else an encoder puts in it.
		if name, ok := objectTypeCodecs[oti]; ok {
			return entryConfig{codec: name}
		}
		return entryConfig{codec: fmt.Sprintf("MPEG-4 object type 0x%02X", oti)}
	}
	for off < end {
		tag, start, stop, ok := readDescriptor(b, off, end, false)
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
// and the bounds of its body. A body declared past end is refused, or with cut ends there.
func readDescriptor(b []byte, off, end int, cut bool) (tag byte, bodyStart, bodyEnd int, ok bool) {
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
		if !cut {
			return 0, 0, 0, false
		}
		length = end - off
	}
	return tag, off, off + length, true
}
