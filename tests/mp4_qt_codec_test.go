package waxlabel_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// mp3MOV is copied from WaxFlow's container/mp4 corpus, where it is one remux of a single
// encode (ffmpeg 8.0.1, Ubuntu):
//
//	ffmpeg -f lavfi -i "sine=frequency=440:sample_rate=22050:duration=1" \
//	    -ac 1 -c:a libmp3lame -b:a 32k mp3.mp3
//	ffmpeg -i mp3.mp3 -c copy mp3.mov
//
// A "qt  " brand with wide+mdat ahead of the moov, carrying the MP3 stream in a QuickTime
// version 1 ".mp3" sample entry - mono, 22050 Hz, samplesize 16, a chan child, no esds.
const mp3MOV = "../testdata/mp3.mov"

// mp4QTChan is the channel-layout box a QuickTime sound entry carries beside its geometry,
// in the form ffmpeg writes for a mono track. It is a sibling of the codec configuration
// boxes and must not be mistaken for one.
func mp4QTChan() []byte {
	return mp4Atom("chan", slices.Concat(
		[]byte{0, 0, 0, 0},  // FullBox version/flags
		mp4be32(0x00010000), // mChannelLayoutTag: take the channels from the bitmap
		mp4be32(4),          // mChannelBitmap: center
		mp4be32(0),          // mNumberChannelDescriptions
	))
}

// TestMP4QuickTimeFourccCodecs: a fourcc is a container spelling, not a codec name, so
// each one reports the canonical codec with the fourcc kept as the profile - the same
// stream in an mp4a/esds entry and in a QuickTime one must read alike. The depth comes
// from wherever the format keeps it: the fourcc for the fixed-width PCM forms, a pcmC box
// for the ISOBMFF ipcm/fpcm pair, and the entry's own samplesize field only where that
// field is authoritative.
func TestMP4QuickTimeFourccCodecs(t *testing.T) {
	for _, c := range []struct {
		name     string
		entry    []byte
		rate     int
		codec    string
		profile  string
		channels int
		depth    int
	}{
		// A QuickTime MP3 track, the shape mp3.mov carries. The 16 in the entry is
		// decoration for a lossy codec; the CLI's bit-depth gate is what suppresses it.
		{"mp3 v1", mp4StsdEntryV1(".mp3", 1, 16, 22050, mp4QTChan()), 22050, "MP3", ".mp3", 1, 16},
		// QTFF's Windows-codec spelling: "ms" then the WAVE format tag. The fourcc holds a
		// NUL, so it can never be the profile - the tag names the codec outright.
		{"ms wave tag", mp4StsdEntry("ms\x00\x55", 2, 16, 44100), 44100, "MP3", "", 2, 16},

		// sowt is 16-bit here and 24-bit there: for the entries whose fourcc names no
		// width, the samplesize field is the only witness and stands.
		{"sowt 16", mp4StsdEntry("sowt", 2, 16, 44100), 44100, "PCM", "sowt", 2, 16},
		{"sowt 24", mp4StsdEntry("sowt", 2, 24, 44100), 44100, "PCM", "sowt", 2, 24},

		// in24 and fl64 name their width, and a v1 entry writes 16 regardless.
		{"in24 v1", mp4StsdEntryV1("in24", 2, 16, 44100), 44100, "PCM", "in24", 2, 24},
		{"fl64 v1", mp4StsdEntryV1("fl64", 2, 16, 44100), 44100, "IEEE float64", "fl64", 2, 64},
		{"ulaw", mp4StsdEntry("ulaw", 1, 16, 8000), 8000, "mu-law", "ulaw", 1, 8},
		{"ima4 v1", mp4StsdEntryV1("ima4", 2, 16, 44100), 44100, "IMA ADPCM", "ima4", 2, 4},

		// A v2 entry's format flags are the only place a float lpcm stream declares itself;
		// without the flag the same fourcc is integer PCM.
		{"lpcm v2 float", mp4StsdEntryV2Flags("lpcm", 96000, 2, 32, 0x9), 96000, "IEEE float", "", 2, 32},
		{"lpcm v2 int", mp4StsdEntryV2("lpcm", 96000, 2, 32), 96000, "PCM", "lpcm", 2, 32},

		// A width no float format defines leaves the entry naming itself, rather than
		// publishing a float at a width that cannot be one.
		{"lpcm v2 float odd width", mp4StsdEntryV2Flags("lpcm", 48000, 2, 24, 0x9), 48000, "PCM", "lpcm", 2, 24},
		{"lpcm v2 float no width", mp4StsdEntryV2Flags("lpcm", 48000, 2, 0, 0x9), 48000, "PCM", "lpcm", 2, 0},

		// The ISOBMFF pair keep their width in a pcmC box. At 64 bits the fourcc alone
		// would name the 32-bit float form, so the box names the codec too.
		{"ipcm pcmC 24", mp4StsdEntry("ipcm", 2, 16, 48000, mp4PcmC(24)), 48000, "PCM", "ipcm", 2, 24},
		{"ipcm pcmC 32", mp4StsdEntry("ipcm", 2, 16, 48000, mp4PcmC(32)), 48000, "PCM", "ipcm", 2, 32},
		{"fpcm pcmC 32", mp4StsdEntry("fpcm", 2, 16, 48000, mp4PcmC(32)), 48000, "IEEE float", "fpcm", 2, 32},
		{"fpcm pcmC 64", mp4StsdEntry("fpcm", 2, 16, 48000, mp4PcmC(64)), 48000, "IEEE float64", "", 2, 64},
		// A pcmC behind a sibling box is still found: the walk skips what is not a codec
		// configuration rather than stopping at the first child.
		{"ipcm pcmC behind chan", mp4StsdEntry("ipcm", 2, 16, 48000, mp4QTChan(), mp4PcmC(24)), 48000, "PCM", "ipcm", 2, 24},
		// With no pcmC the entry field stands for ipcm, a width integer PCM can have. For
		// fpcm it is the fixed 16 every writer stores and no 16-bit float exists, so the
		// width is reported as unknown rather than as an impossible format.
		{"ipcm no pcmC", mp4StsdEntry("ipcm", 2, 16, 48000), 48000, "PCM", "ipcm", 2, 16},
		{"fpcm no pcmC", mp4StsdEntry("fpcm", 2, 16, 48000), 48000, "IEEE float", "fpcm", 2, 0},

		// The canonical codec table folds case, so the width the same fourcc names must
		// fold with it: an uppercase entry cannot read as float at the entry's fixed 16.
		{"FL32 uppercase", mp4StsdEntryV1("FL32", 2, 16, 44100), 44100, "IEEE float", "FL32", 2, 32},
		{"IN24 uppercase", mp4StsdEntryV1("IN24", 2, 16, 44100), 44100, "PCM", "IN24", 2, 24},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := mp4AssembleStsd(mp4Stsd(c.entry), nil, nil, nil, c.rate)
			tr := mustParseBytes(t, data).Properties().First()
			if tr.Codec != c.codec || tr.CodecProfile != c.profile {
				t.Errorf("codec = %q / profile %q, want %q / %q", tr.Codec, tr.CodecProfile, c.codec, c.profile)
			}
			if tr.BitsPerSample != c.depth {
				t.Errorf("bits per sample = %d, want %d", tr.BitsPerSample, c.depth)
			}
			if tr.SampleRate != c.rate || tr.Channels != c.channels {
				t.Errorf("geometry = %d Hz / %d ch, want %d/%d", tr.SampleRate, tr.Channels, c.rate, c.channels)
			}
		})
	}
}

// TestMP4QuickTimeMP3Fixture reads the committed .mov: the same MP3 stream an mp4a/esds
// entry reports as "MP3" must read the same way from a QuickTime ".mp3" entry, with the
// fourcc demoted to the profile. The digest extent is pinned because none of this touches
// the salt, which keeps the raw entry values.
func TestMP4QuickTimeMP3Fixture(t *testing.T) {
	doc := mustParseFile(t, mp3MOV)
	tr := doc.Properties().First()
	if tr.Codec != "MP3" || tr.CodecProfile != ".mp3" {
		t.Errorf("codec = %q / profile %q, want MP3 / .mp3", tr.Codec, tr.CodecProfile)
	}
	if tr.SampleRate != 22050 || tr.Channels != 1 {
		t.Errorf("geometry = %d Hz / %d ch, want 22050/1", tr.SampleRate, tr.Channels)
	}
	digest, err := doc.HashAudioEssence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if digest.ExtentVersion != "mp4-mdat-v3" {
		t.Errorf("extent = %q, want mp4-mdat-v3", digest.ExtentVersion)
	}
}

// TestMP4DifferentialFFmpegFourccs encodes each uncompressed fourcc with the real ffmpeg
// and checks the codec, profile, geometry and depth against ffprobe reading the same
// bytes. The stream-copy legs are the agreement case the whole table exists for: one MP3
// stream copied into both .mov and .mp4 must read as one codec.
func TestMP4DifferentialFFmpegFourccs(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	dir := t.TempDir()

	encode := func(t *testing.T, name string, args ...string) string {
		t.Helper()
		return ffmpegEncode(t, filepath.Join(dir, name), args...)
	}
	sine := func(t *testing.T, name string, rate int, codecArgs ...string) string {
		t.Helper()
		return ffmpegSine(t, filepath.Join(dir, name), 2, rate, codecArgs...)
	}

	// One subtest per leg: a skip for an ffmpeg that cannot write the container must not
	// take the other half of the comparison with it.
	for _, c := range []struct{ name, profile string }{{"copy.mov", ".mp3"}, {"copy.mp4", ""}} {
		t.Run("mp3 stream copy "+c.name, func(t *testing.T) {
			path := encode(t, c.name, "-i", sampleMP3, "-map", "0:a:0", "-c", "copy")
			tr := mustParseFile(t, path).Properties().Tracks[0]
			if tr.Codec != "MP3" || tr.CodecProfile != c.profile {
				t.Errorf("codec = %q / profile %q, want MP3 / %q", tr.Codec, tr.CodecProfile, c.profile)
			}
			rate, channels, _ := ffprobeAudio(t, path)
			if tr.SampleRate != rate || tr.Channels != channels {
				t.Errorf("geometry = %d Hz / %d ch, ffprobe says %d/%d", tr.SampleRate, tr.Channels, rate, channels)
			}
		})
	}

	for _, c := range []struct {
		name    string
		file    string
		rate    int
		sample  string
		codec   string
		profile string
		depth   int
	}{
		{"in24", "s24.mov", 44100, "pcm_s24le", "PCM", "in24", 24},
		{"fl32", "f32.mov", 44100, "pcm_f32le", "IEEE float", "fl32", 32},
		// At 96 kHz ffmpeg writes a v2 entry, where the float form is a flag and not a fourcc.
		{"lpcm v2", "f32hi.mov", 96000, "pcm_f32le", "IEEE float", "", 32},
		{"ipcm", "s24.mp4", 44100, "pcm_s24le", "PCM", "ipcm", 24},
		{"fpcm", "f32.mp4", 44100, "pcm_f32le", "IEEE float", "fpcm", 32},
		// A hi-res ISOBMFF entry stays v0, whose 16.16 rate field holds nothing above
		// 65535, and pcmC carries no rate: the media timescale is the only witness left.
		{"ipcm hi-res", "s24hi.mp4", 96000, "pcm_s24le", "PCM", "ipcm", 24},
		{"fpcm hi-res", "f32hi.mp4", 96000, "pcm_f32le", "IEEE float", "fpcm", 32},
		{"ulaw", "ulaw.mov", 44100, "pcm_mulaw", "mu-law", "ulaw", 8},
		{"ima4", "ima4.mov", 44100, "adpcm_ima_qt", "IMA ADPCM", "ima4", 4},
		{"sowt", "s16.mov", 44100, "pcm_s16le", "PCM", "sowt", 16},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Encoded inside the subtest so a skip for an unbuildable codec lands on the T
			// running it rather than on the whole table.
			path := sine(t, c.file, c.rate, "-c:a", c.sample)
			tr := mustParseFile(t, path).Properties().Tracks[0]
			if tr.Codec != c.codec || tr.CodecProfile != c.profile {
				t.Errorf("codec = %q / profile %q, want %q / %q", tr.Codec, tr.CodecProfile, c.codec, c.profile)
			}
			p := ffprobeStream(t, path)
			if tr.SampleRate != p.SampleRate || tr.Channels != p.Channels {
				t.Errorf("geometry = %d Hz / %d ch, ffprobe says %d/%d", tr.SampleRate, tr.Channels, p.SampleRate, p.Channels)
			}
			// The width the file declares is the one both readers must agree on. The
			// table's is the width the encode asked for, which binds only where the writer
			// recorded it: ffmpeg 6.1 fills an ipcm track's pcmC from the encoder's sample
			// format rather than its sample width, so a pcm_s24le encode declares 32 there
			// and ffmpeg reads its own file back as pcm_s32le. ffmpeg 8 declares 24.
			switch {
			case tr.BitsPerSample != p.BitsPerSample:
				t.Errorf("bits per sample = %d, ffprobe says %d", tr.BitsPerSample, p.BitsPerSample)
			case p.BitsPerSample != c.depth:
				t.Logf("%s declares %d bits for a %s encode, and ffprobe reads it back the same way",
					c.file, p.BitsPerSample, c.sample)
			}
		})
	}
}
