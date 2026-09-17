package aac

import "github.com/colespringer/waxlabel/internal/mpeg4audio"

// adtsHeader is a decoded ADTS fixed header: static stream config plus frame
// length. Config feeds the essence digest; length advances the duration walk
// (totalADTSSamples) and is kept out of the digest (Codec.EssenceExtent).
type adtsHeader struct {
	objectType  int // MPEG-4 AOT (= profile+1): 1 Main, 2 LC, 3 SSR; profile 3 rejected
	sfIndex     int // sampling-frequency index (0..12)
	sampleRate  int // Hz
	chanConfig  int // channel-configuration (0..7)
	channels    int // decoded count (0 when chanConfig is 0 / in AOT config)
	frameLength int // total frame bytes (header + payload)
	rawBlocks   int // number_of_raw_data_blocks_in_frame (0..3); frame holds rawBlocks+1 blocks
	// protectionAbsent: when clear, a 2-byte CRC follows the fixed header.
	protectionAbsent bool
}

// headerLen is bytes before the first raw data block (fixed header + optional CRC).
func (h adtsHeader) headerLen() int {
	if h.protectionAbsent {
		return adtsHeaderSize
	}
	return adtsHeaderSize + 2
}

// adtsHeaderSize is the fixed header without optional CRC. decodeADTS reads only
// the fixed header; CRC (when present) sits inside frameLength and is copied on write.
const adtsHeaderSize = 7

// decodeADTS validates the ADTS fixed header at b. Shared by Sniff, the root
// front-ID3 peek, and parse.
//
// Strict enough to separate ADTS from MP3/arbitrary bytes: syncword 0xFFF,
// layer == 00 (MP3 rejects this), non-reserved object type, sfIndex < 13, frame
// length >= header size. ok is false on failure or short buffer.
func decodeADTS(b []byte) (adtsHeader, bool) {
	if len(b) < adtsHeaderSize {
		return adtsHeader{}, false
	}
	// Syncword: 12 bits all 1.
	if b[0] != 0xFF || b[1]&0xF0 != 0xF0 {
		return adtsHeader{}, false
	}
	// Layer (byte 1, bits 2..1) must be 00 for ADTS.
	if b[1]&0x06 != 0 {
		return adtsHeader{}, false
	}
	profile := int(b[2] >> 6)
	if profile == 3 {
		// Reserved (MPEG-2) / AOT 4 LTP (MPEG-4). Reject both; keeps objectType in 1-3.
		return adtsHeader{}, false
	}
	sfIndex := int(b[2] >> 2 & 0x0F)
	// 13-14 reserved; 15 means explicit rate (never in ADTS). All yield no rate.
	sampleRate := mpeg4audio.SampleRate(sfIndex)
	if sampleRate == 0 {
		return adtsHeader{}, false
	}
	chanConfig := int(b[2]&0x01)<<2 | int(b[3]>>6)
	frameLength := int(b[3]&0x03)<<11 | int(b[4])<<3 | int(b[5]>>5)
	if frameLength < adtsHeaderSize {
		return adtsHeader{}, false
	}
	return adtsHeader{
		objectType:  profile + 1,
		sfIndex:     sfIndex,
		sampleRate:  sampleRate,
		chanConfig:  chanConfig,
		channels:    mpeg4audio.ChannelCount(chanConfig),
		frameLength: frameLength,
		// Last 2 bits of byte 6: frame holds rawBlocks+1 AAC blocks (1..4).
		rawBlocks:        int(b[6] & 0x03),
		protectionAbsent: b[1]&0x01 != 0,
	}, true
}
