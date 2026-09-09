// Package mpeg4audio decodes the MPEG-4 AudioSpecificConfig (ISO/IEC 14496-3
// §1.6.2.1) and the sampling-frequency and channel-configuration tables it shares
// with the ADTS header. Both the raw AAC codec and the MP4 esds box need them, so
// they live here rather than in either; the package holds no mutable state, and the Huffman
// decoders are built on first use.
//
// The config decoder answers one question: what geometry does this stream declare? It
// reads the header, follows the hierarchical SBR/PS signalling of AOT 5 and 29,
// and reads the backward-compatible extension tail that an AAC-LC config appends
// to say whether SBR is present. It deliberately stops at the first field it
// cannot locate - a program config element, an extensionFlag3 payload, a codec
// whose specific config it does not walk - and reports what it read up to there.
//
// [ParseRawDataBlock] and [ParseSBRSingleChannel] answer the same question for a stream that
// declares nothing: raw ADTS has no field for SBR, so an HE-AAC stream there is only visible
// in the frames. They walk the AAC-LC syntax to the fill element that carries the SBR
// payload, and that payload to the extension that carries parametric stereo. Neither
// reconstructs audio; both succeed only by landing exactly where the syntax says the element
// ends, so a misread surfaces as a failure rather than a wrong answer.
//
// The AAC and SBR Huffman codebooks and the scalefactor band offsets those walks need are
// generated from the text of the specification by the nested gentables module, which is run
// by hand and needs a copy of the specification (never checked in):
//
//	cd gentables && go run . -spec /path/to/iso14496-3-2009.pdf -out ..
//
// Everything here is reimplemented from the specification; no reference implementation was
// copied.
package mpeg4audio

// sampleRates maps the 4-bit samplingFrequencyIndex to Hz. Indices 13 and 14 are
// reserved and 15 escapes to a 24-bit rate in the bitstream, so none of the three
// has a table entry.
var sampleRates = [13]int{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350,
}

// channelCounts maps the 4-bit channelConfiguration to a channel count.
// Configuration 0 means a program config element carries the layout, so the count is
// unknown; 8, 9, 10 and 15 are reserved. The layouts ISO/IEC 14496-3:2009/Amd.4 added
// past the original table: 11 is 6.1, 12 is 7.1, 13 is 22.2, and 14 is 5.1.2.
var channelCounts = [16]int{0, 1, 2, 3, 4, 5, 6, 8, 0, 0, 0, 7, 8, 24, 8, 0}

// SampleRate maps a samplingFrequencyIndex to Hz, returning 0 for the reserved
// indices 13 and 14, for 15 (an explicit rate follows in the bitstream), and for
// anything out of range. A caller that cannot handle an explicit rate can reject
// on the zero.
func SampleRate(index int) int {
	if index < 0 || index >= len(sampleRates) {
		return 0
	}
	return sampleRates[index]
}

// ChannelCount maps a channelConfiguration to a channel count, returning 0 for
// configuration 0 (the layout is in a program config element) and for the
// reserved values.
func ChannelCount(config int) int {
	if config < 0 || config >= len(channelCounts) {
		return 0
	}
	return channelCounts[config]
}

// ObjectTypeName names an MPEG-4 audio object type for a track's codec profile.
// Only the types a file's profile field can meaningfully distinguish are named;
// everything else is the generic "AAC", which callers treat as "no detail worth
// reporting".
func ObjectTypeName(aot int) string {
	switch aot {
	case 1:
		return "AAC Main"
	case 2:
		return "AAC LC"
	case 3:
		return "AAC SSR"
	case 4:
		return "AAC LTP"
	case 23:
		return "AAC LD"
	case 39:
		return "AAC ELD"
	case 42:
		return "xHE-AAC"
	}
	return "AAC"
}

// Config is what an AudioSpecificConfig declares about its stream.
//
// SampleRate and Channels are the core coder's, which for an SBR stream is half
// the rate a player outputs; OutputSampleRate and OutputChannels give the played
// geometry. SBRSignalled separates "the config says SBR is absent" from "the
// config says nothing about SBR", a distinction callers need: an AAC-LC config
// written by ffmpeg carries an extension tail that explicitly denies SBR, while
// an implicitly signalled HE-AAC stream's config is silent. A config truncated
// before the flag counts as silent, not as a denial.
type Config struct {
	ObjectType    int  // core audio object type, after any SBR or PS wrapper (2 = AAC LC)
	SampleRate    int  // core sample rate in Hz, 0 when the index is reserved
	ChannelConfig int  // the 4-bit channelConfiguration; 0 = the layout is in a program config element
	Channels      int  // ChannelCount(ChannelConfig)
	SBRSignalled  bool // the config states SBR presence either way, whether declared or denied
	SBR           bool // SBR is present
	PS            bool // parametric stereo is present
	ExtensionRate int  // extensionSamplingFrequency when SBR is present, else 0
}

// OutputSampleRate is the rate a player produces: the SBR extension rate when the
// config declares one, else the core rate. A downsampled SBR stream declares an
// extension rate equal to its core rate, so this never blindly doubles.
func (c Config) OutputSampleRate() int {
	if c.SBR && c.ExtensionRate > 0 {
		return c.ExtensionRate
	}
	return c.SampleRate
}

// OutputChannels is the channel count a player produces: parametric stereo turns a
// mono core into stereo, everything else plays the core layout.
func (c Config) OutputChannels() int {
	if c.PS && c.ChannelConfig == 1 {
		return 2
	}
	return c.Channels
}

// ProfileName names the stream for a track's codec profile, preferring the SBR/PS
// spelling a listener would recognize over the core object type.
func (c Config) ProfileName() string {
	switch {
	case c.SBR && c.PS:
		return "HE-AAC v2"
	case c.SBR:
		return "HE-AAC"
	}
	return ObjectTypeName(c.ObjectType)
}

// Sync extension types that can open the backward-compatible tail: 0x2B7
// introduces the SBR (and, nested, PS) signalling, 0x548 the PS flag inside it.
const (
	syncExtensionSBR = 0x2B7
	syncExtensionPS  = 0x548
)

// explicitRateIndex is the samplingFrequencyIndex that escapes to a 24-bit rate.
const explicitRateIndex = 15

// maxSampleRate bounds an explicit rate. The escape exists to name rates the 4-bit table
// cannot, but the field is 24 bits wide and a corrupt config fills it: 655350 Hz is the
// highest rate any format this library reads can store (FLAC's frame headers cap there),
// so anything above it is not a sample rate, and letting it through would override a
// container field that is almost certainly right.
const maxSampleRate = 655350

// ParseConfig decodes an AudioSpecificConfig. ok is false only when b is too short
// to hold the three fixed header fields (object type, sampling frequency, channel
// configuration); past that the decode never fails, it stops at the first field it
// cannot read or locate and reports what it has.
func ParseConfig(b []byte) (Config, bool) {
	r := &bitReader{b: b}
	var c Config

	aot, ok := readObjectType(r)
	if !ok {
		return Config{}, false
	}
	rate, ok := readRate(r)
	if !ok {
		return Config{}, false
	}
	chanCfg, ok := r.read(4)
	if !ok {
		return Config{}, false
	}
	c.ObjectType, c.SampleRate, c.ChannelConfig = aot, rate, chanCfg
	c.Channels = ChannelCount(chanCfg)

	// Hierarchical signalling: AOT 5 (SBR) and 29 (PS) wrap the core object type,
	// which follows the extension rate.
	if aot == 5 || aot == 29 {
		c.SBRSignalled, c.SBR, c.PS = true, true, aot == 29
		extRate, ok := readRate(r)
		if !ok {
			return c.finish(), true
		}
		c.ExtensionRate = extRate
		core, ok := readObjectType(r)
		if !ok {
			return c.finish(), true
		}
		if core == 5 || core == 29 {
			// An SBR wrapper around another SBR wrapper is non-conformant. The whole
			// extension block is suspect, so drop its rate and report the core one.
			c.ExtensionRate = 0
			return c.finish(), true
		}
		c.ObjectType = core
		if core == 22 {
			if _, ok := r.read(4); !ok { // extensionChannelConfiguration
				return c.finish(), true
			}
		}
		aot = core
	}

	// Only the general audio object types have a specific config short enough to
	// walk to the extension tail. The error-resilient types (17, 19..23) end in an
	// epConfig, and the rest (CELP, HVXC, ALS, USAC, ...) have configs of their own;
	// for all of them the tail cannot be located, so the header is the answer.
	switch aot {
	case 1, 2, 3, 4, 6, 7:
		if !parseGASpecificConfig(r, c.ChannelConfig, aot) {
			return c.finish(), true
		}
	default:
		return c.finish(), true
	}

	// Backward-compatible tail. A config that already signalled SBR hierarchically
	// does not repeat it. The read happens at the current bit position only: scanning
	// for the sync word would find it in payload bits.
	if c.SBRSignalled || r.remaining() < 16 {
		return c.finish(), true
	}
	sync, _ := r.read(11)
	if sync != syncExtensionSBR {
		return c.finish(), true
	}
	extType, ok := readObjectType(r)
	if !ok {
		return c.finish(), true
	}
	switch extType {
	case 5:
		// The flag denies SBR as often as it declares it: every ffmpeg-native AAC-LC
		// config ends here with a zero, which is why SBRSignalled is recorded separately.
		// It is set only once the flag has actually been read - a config truncated at this
		// point says nothing about SBR, and reporting it as a denial is worse than
		// reporting the silence.
		sbr, ok := r.read(1)
		if !ok {
			return c.finish(), true
		}
		c.SBRSignalled = true
		if sbr == 0 {
			return c.finish(), true
		}
		c.SBR = true
		if extRate, ok := readRate(r); ok {
			c.ExtensionRate = extRate
		}
		if r.remaining() < 12 {
			return c.finish(), true
		}
		if sync, _ := r.read(11); sync == syncExtensionPS {
			if ps, ok := r.read(1); ok && ps == 1 {
				c.PS = true
			}
		}
	case 22:
		sbr, ok := r.read(1)
		if !ok {
			return c.finish(), true
		}
		c.SBRSignalled = true
		if sbr == 1 {
			c.SBR = true
			if extRate, ok := readRate(r); ok {
				c.ExtensionRate = extRate
			}
		}
		r.read(4) // extensionChannelConfiguration
	}
	return c.finish(), true
}

// finish applies the conformance rules that depend on the whole config.
func (c Config) finish() Config {
	// Parametric stereo synthesizes a stereo image from a mono core; declared over
	// any other layout it is meaningless, so it is dropped rather than reported.
	if c.PS && c.ChannelConfig != 1 {
		c.PS = false
	}
	return c
}

// readObjectType reads an audioObjectType: 5 bits, with 31 escaping to 6 more bits
// biased by 32.
func readObjectType(r *bitReader) (int, bool) {
	aot, ok := r.read(5)
	if !ok {
		return 0, false
	}
	if aot != 31 {
		return aot, true
	}
	ext, ok := r.read(6)
	if !ok {
		return 0, false
	}
	return ext + 32, true
}

// readRate reads a samplingFrequencyIndex and resolves it to Hz: a table lookup,
// or the 24-bit rate index 15 escapes to. A reserved index yields 0, which callers
// read as "the config declares no rate here"; ok is false only on a short read.
func readRate(r *bitReader) (int, bool) {
	index, ok := r.read(4)
	if !ok {
		return 0, false
	}
	if index != explicitRateIndex {
		return SampleRate(index), true
	}
	rate, ok := r.read(24)
	if !ok {
		return 0, false
	}
	if rate > maxSampleRate {
		return 0, true // reads as "the config declares no rate here"
	}
	return rate, true
}

// parseGASpecificConfig walks the GASpecificConfig fields that sit between the
// header and the backward-compatible tail. It reports whether the tail's bit
// position is now known: a program config element (channelConfiguration 0) and an
// extensionFlag3 payload are both variable-length runs this decoder does not walk,
// so either one ends the decode.
func parseGASpecificConfig(r *bitReader, chanCfg, aot int) bool {
	if _, ok := r.read(1); !ok { // frameLengthFlag
		return false
	}
	dependsOnCoreCoder, ok := r.read(1)
	if !ok {
		return false
	}
	if dependsOnCoreCoder == 1 {
		if _, ok := r.read(14); !ok { // coreCoderDelay
			return false
		}
	}
	extensionFlag, ok := r.read(1)
	if !ok {
		return false
	}
	if chanCfg == 0 {
		return false // a variable-length program_config_element follows
	}
	if aot == 6 {
		if _, ok := r.read(3); !ok { // layerNr
			return false
		}
	}
	if extensionFlag == 1 {
		extensionFlag3, ok := r.read(1)
		if !ok || extensionFlag3 == 1 {
			return false
		}
	}
	return true
}
