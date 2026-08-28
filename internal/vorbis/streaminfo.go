package vorbis

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// StreamInfoLen is the byte length of a FLAC STREAMINFO block body.
const StreamInfoLen = 34

// ParseStreamInfo decodes the 34-byte STREAMINFO body into an AudioTrack. The
// bit-packed region holds a 20-bit sample rate, 3-bit channel count (less
// one), 5-bit bits-per-sample (less one), and 36-bit total sample count.
//
// It lives here rather than in internal/flac because both FLAC containers need
// it: the native .flac stream and the Ogg FLAC mapping, whose identification
// packet ends with the same STREAMINFO block.
func ParseStreamInfo(body []byte) (core.AudioTrack, error) {
	if len(body) < StreamInfoLen {
		return core.AudioTrack{}, fmt.Errorf("%w: STREAMINFO is %d bytes, need %d", waxerr.ErrInvalidData, len(body), StreamInfoLen)
	}
	t := core.AudioTrack{Codec: "flac"}
	t.MinBlockSize = int(binary.BigEndian.Uint16(body[0:2]))
	t.MaxBlockSize = int(binary.BigEndian.Uint16(body[2:4]))

	t.SampleRate = int(body[10])<<12 | int(body[11])<<4 | int(body[12])>>4
	t.Channels = int(body[12]>>1&0x07) + 1
	t.BitsPerSample = int((body[12]&0x01)<<4|body[13]>>4) + 1
	t.TotalSamples = uint64(body[13]&0x0F)<<32 | uint64(body[14])<<24 |
		uint64(body[15])<<16 | uint64(body[16])<<8 | uint64(body[17])
	copy(t.MD5[:], body[18:34])

	if t.SampleRate == 0 {
		return t, fmt.Errorf("%w: STREAMINFO sample rate is zero", waxerr.ErrInvalidData)
	}
	// Guard against pathological inputs (e.g. SampleRate 1 with TotalSamples
	// near 2^36) overflowing the int64 nanoseconds of time.Duration into garbage.
	if ns := float64(t.TotalSamples) / float64(t.SampleRate) * float64(time.Second); ns >= 0 && ns < math.MaxInt64 {
		t.Duration = time.Duration(ns)
	}
	return t, nil
}

// FLAC metadata block type codes. Both FLAC containers - the native stream and the Ogg
// mapping, where each header packet carries one block - walk the same block types, so
// the codes and their names live here with the STREAMINFO decoder rather than being
// declared once per package.
const (
	BlockStreamInfo    = 0
	BlockPadding       = 1
	BlockApplication   = 2
	BlockSeekTable     = 3
	BlockVorbisComment = 4
	BlockCueSheet      = 5
	BlockPicture       = 6
	BlockInvalid       = 127

	// MaxBlockBody is the largest body a block's 24-bit length field can describe.
	MaxBlockBody = 1<<24 - 1
)

// BlockName is the specification's name for a metadata block type, for diagnostics and
// the native view.
func BlockName(code byte) string {
	switch code {
	case BlockStreamInfo:
		return "STREAMINFO"
	case BlockPadding:
		return "PADDING"
	case BlockApplication:
		return "APPLICATION"
	case BlockSeekTable:
		return "SEEKTABLE"
	case BlockVorbisComment:
		return "VORBIS_COMMENT"
	case BlockCueSheet:
		return "CUESHEET"
	case BlockPicture:
		return "PICTURE"
	default:
		return "UNKNOWN"
	}
}
