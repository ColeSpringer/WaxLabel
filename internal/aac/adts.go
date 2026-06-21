package aac

// adtsHeader is the decoded ADTS fixed header of a frame: the static stream
// configuration (object type, sample rate, channels) plus the frame length. The
// static config identifies the stream and feeds the essence digest; the frame
// length advances the header walk that derives an accurate duration (see
// totalADTSSamples) and is deliberately kept out of the digest (see
// Codec.EssenceExtent).
type adtsHeader struct {
	objectType  int // MPEG-4 Audio Object Type (= profile field + 1): 1 Main, 2 LC, 3 SSR (AOT 4 / LTP is rejected at decode)
	sfIndex     int // sampling-frequency index (0..12)
	sampleRate  int // decoded sample rate in Hz
	chanConfig  int // channel-configuration field (0..7)
	channels    int // decoded channel count (0 when chanConfig is 0 / carried in the AOT config)
	frameLength int // total bytes of this frame (header + payload)
	rawBlocks   int // number_of_raw_data_blocks_in_frame (0..3); the frame holds rawBlocks+1 AAC blocks
}

// adtsSampleRates maps the 4-bit sampling-frequency index to Hz. Indices 13-14
// are reserved and 15 means "explicit rate in the AOT-specific config" (which is
// never present in an ADTS header); all three are rejected by decodeADTS.
var adtsSampleRates = [13]int{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350,
}

// adtsChannels maps the 3-bit channel configuration to a channel count. Config 0
// means the layout is carried in the AOT-specific config (absent from ADTS), so
// the count is unknown (0); config 7 is 7.1 (8 channels).
var adtsChannels = [8]int{0, 1, 2, 3, 4, 5, 6, 8}

// adtsHeaderSize is the ADTS fixed header length without the optional 2-byte CRC
// (present when protection_absent is 0). decodeADTS only reads the fixed header;
// the CRC, when present, is inside frameLength and copied verbatim on write.
const adtsHeaderSize = 7

// decodeADTS decodes and validates the ADTS fixed header at the start of b. It
// is the single ADTS recognizer, shared by Sniff, the root front-ID3 detection
// peek, and parse, so the strictness lives in one place.
//
// Validation is deliberately strict to keep raw ADTS from being confused with
// MP3 or arbitrary bytes (the front-ID3 ambiguity now has a third party - MP3 vs
// FLAC vs AAC): syncword 0xFFF, layer == 00 (always so for ADTS, and the value
// MP3 frame decoding rejects, which gives the two formats mutual exclusivity), a
// non-reserved object type (profile field != 3), a valid sampling-frequency
// index (< 13), and a frame length at least the header size. ok is false on any
// failure or a short buffer.
func decodeADTS(b []byte) (adtsHeader, bool) {
	if len(b) < adtsHeaderSize {
		return adtsHeader{}, false
	}
	// Syncword: 12 bits all 1 (byte 0, then the top nibble of byte 1).
	if b[0] != 0xFF || b[1]&0xF0 != 0xF0 {
		return adtsHeader{}, false
	}
	// Layer (byte 1, bits 2..1) must be 00 for ADTS.
	if b[1]&0x06 != 0 {
		return adtsHeader{}, false
	}
	profile := int(b[2] >> 6)
	if profile == 3 {
		// Reserved in MPEG-2 ADTS; AOT 4 (LTP) in MPEG-4. Rejected either way: LTP
		// is effectively unused in practice, and excluding one of the four profile
		// values tightens the false-positive guard. So objectType is always 1-3.
		return adtsHeader{}, false
	}
	sfIndex := int(b[2] >> 2 & 0x0F)
	if sfIndex >= len(adtsSampleRates) {
		return adtsHeader{}, false // 13/14 reserved, 15 explicit (never in ADTS)
	}
	chanConfig := int(b[2]&0x01)<<2 | int(b[3]>>6)
	frameLength := int(b[3]&0x03)<<11 | int(b[4])<<3 | int(b[5]>>5)
	if frameLength < adtsHeaderSize {
		return adtsHeader{}, false
	}
	return adtsHeader{
		objectType:  profile + 1,
		sfIndex:     sfIndex,
		sampleRate:  adtsSampleRates[sfIndex],
		chanConfig:  chanConfig,
		channels:    adtsChannels[chanConfig],
		frameLength: frameLength,
		// number_of_raw_data_blocks_in_frame: the last 2 bits of byte 6. A frame holds
		// rawBlocks+1 AAC blocks (1..4), each samplesPerAACFrame samples, so the duration
		// walk must not assume a flat one block per frame.
		rawBlocks: int(b[6] & 0x03),
	}, true
}

// aotName names the AAC object type for the track's Codec field. Only AOT 1-3
// can reach here - decodeADTS rejects profile field 3 (AOT 4 / LTP) - so there
// is deliberately no LTP case; an unexpected value falls back to plain "AAC".
func aotName(objectType int) string {
	switch objectType {
	case 1:
		return "AAC Main"
	case 2:
		return "AAC LC"
	case 3:
		return "AAC SSR"
	default:
		return "AAC"
	}
}
