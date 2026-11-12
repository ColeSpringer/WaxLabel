package mp4

// A TrueHD or MLP access unit opens with a 4-byte length and timing word. A major sync, which
// the first unit of a stream carries, follows: the 24-bit sync 0xF8726F, a stream type, then the
// format, whose layout the type decides (Dolby TrueHD, MLP).

// trueHDSyncLen is how much of an access unit parseTrueHDMajorSync reads.
const trueHDSyncLen = 10

const (
	streamTrueHD = 0xBA
	streamMLP    = 0xBB
)

// majorSync is what a major sync declares of its stream.
type majorSync struct {
	rate, bits int
	mlp        bool
}

// parseTrueHDMajorSync reads the major sync of the access unit sample opens with: its sample
// rate (0 for a code that names none) and word length. TrueHD states no word length; its decoders
// deliver 24 bits, which ffmpeg reports too.
func parseTrueHDMajorSync(sample []byte) (sync majorSync, ok bool) {
	if len(sample) < trueHDSyncLen || sample[4] != 0xF8 || sample[5] != 0x72 || sample[6] != 0x6F {
		return majorSync{}, false
	}
	switch sample[7] {
	case streamTrueHD:
		return majorSync{rate: mlpSampleRate(sample[8] >> 4), bits: 24}, true
	case streamMLP:
		// The first substream's word length, then two rate codes.
		bits := 0
		if q := sample[8] >> 4; q <= 2 {
			bits = 16 + 4*int(q)
		}
		return majorSync{rate: mlpSampleRate(sample[9] >> 4), bits: bits, mlp: true}, true
	}
	return majorSync{}, false
}

// mlpSampleRate decodes the 4-bit rate code TrueHD and MLP share: a base of 48 or 44.1 kHz,
// doubled up to twice. Any other code is no rate.
func mlpSampleRate(code byte) int {
	if code&7 > 2 {
		return 0
	}
	base := 48000
	if code&8 != 0 {
		base = 44100
	}
	return base << (code & 7)
}
