package waxlabel_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// mp4TrueHD is a file of one mlpa track: the sample entry's rate field, a dmlp box when a rate code
// is given, the media timescale, and the first sample's bytes.
func mp4TrueHD(t *testing.T, entryRate, dmlp, timescale int, sample []byte) []byte {
	t.Helper()
	var kids []byte
	if dmlp >= 0 {
		kids = testfile.MP4Atom("dmlp", slices.Concat([]byte{byte(dmlp << 4), 0, 0, 0}, make([]byte, 6)))
	}
	data := testfile.MP4AssembleStsd(testfile.MP4Stsd(testfile.MP4StsdEntry("mlpa", 2, 16, entryRate, kids)), nil, nil, nil, timescale)
	at, _ := mp4Index(t, data)
	copy(data[at:], sample)
	return data
}

// majorSync is an access unit that opens with a major sync of the given stream type and the two
// bytes after it.
func majorSync(streamType, b8, b9 byte) []byte {
	return []byte{0x00, 0x14, 0x00, 0x00, 0xF8, 0x72, 0x6F, streamType, b8, b9, 0, 0}
}

// A TrueHD track's rate and word length come from the stream's major sync, then the dmlp box's
// rate code, then the media timescale; the sample entry's own fields hold neither.
func TestMP4TrueHDProperties(t *testing.T) {
	for _, c := range []struct {
		name            string
		entryRate, dmlp int
		timescale       int
		sample          []byte
		codec           string
		rate, bits      int
	}{
		{"major sync, 48 kHz", 0, 0, 48000, majorSync(0xBA, 0x00, 0), "TrueHD", 48000, 24},
		{"major sync, 96 kHz over a dmlp of 48", 0, 0, 48000, majorSync(0xBA, 0x10, 0), "TrueHD", 96000, 24},
		{"major sync, 192 kHz", 0, -1, 48000, majorSync(0xBA, 0x20, 0), "TrueHD", 192000, 24},
		{"major sync, 44.1 kHz", 0, -1, 48000, majorSync(0xBA, 0x80, 0), "TrueHD", 44100, 24},
		{"major sync, 176.4 kHz", 0, -1, 48000, majorSync(0xBA, 0xA0, 0), "TrueHD", 176400, 24},
		{"major sync with no rate, dmlp of 96", 0, 1, 48000, majorSync(0xBA, 0xF0, 0), "TrueHD", 96000, 24},
		{"major sync with a reserved rate code", 0, -1, 48000, majorSync(0xBA, 0x30, 0), "TrueHD", 48000, 24},
		{"MLP sync, 20 bits at 88.2 kHz", 0, -1, 48000, majorSync(0xBB, 0x10, 0x90), "MLP", 88200, 20},
		{"MLP sync, 16 bits", 0, -1, 48000, majorSync(0xBB, 0x00, 0x00), "MLP", 48000, 16},
		{"MLP sync, unknown width", 0, -1, 48000, majorSync(0xBB, 0x70, 0x00), "MLP", 48000, 0},
		{"no sync, dmlp of 192", 0, 2, 48000, nil, "TrueHD", 192000, 0},
		{"no sync, dmlp with no rate", 0, 15, 96000, nil, "TrueHD", 96000, 0},
		{"no sync, no dmlp: the timescale", 0, -1, 96000, nil, "TrueHD", 96000, 0},
		{"no sync, the entry's rate", 48000, -1, 44100, nil, "TrueHD", 48000, 0},
		{"another stream type", 0, -1, 48000, majorSync(0xBC, 0x10, 0), "TrueHD", 48000, 0},
		{"sync cut short", 0, -1, 48000, majorSync(0xBA, 0x10, 0)[:9], "TrueHD", 96000, 24},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := mp4TrueHD(t, c.entryRate, c.dmlp, c.timescale, c.sample)
			if c.name == "sync cut short" {
				// The sample is the file's last nine bytes.
				at, _ := mp4Index(t, data)
				data = data[:at+9]
				c.rate, c.bits = 48000, 0
			}
			tr := mustParseBytes(t, data).Properties().Tracks[0]
			if tr.Codec != c.codec || tr.SampleRate != c.rate || tr.BitsPerSample != c.bits || tr.Channels != 2 {
				t.Errorf("%s (%q) at %d Hz, %d bits, %d ch; want %s at %d Hz, %d bits, 2 ch", tr.Codec, tr.CodecProfile, tr.SampleRate, tr.BitsPerSample, tr.Channels, c.codec, c.rate, c.bits)
			}
		})
	}
	// The digest salts with the entry's own fields, whatever the stream says.
	a := essenceOf(t, mp4TrueHD(t, 0, 0, 48000, majorSync(0xBA, 0x00, 0)))
	sync := majorSync(0xBA, 0x00, 0)
	other := mp4TrueHD(t, 0, 1, 96000, sync)
	if b := essenceOf(t, other); !a.Equal(b) {
		t.Errorf("digest %s, want %s: the dmlp box and timescale are no part of it", b, a)
	}
}

// ffmpeg's own TrueHD in MP4, at a rate its sample entry cannot hold as 16.16.
func TestMP4DifferentialTrueHD(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	dir := t.TempDir()
	for _, rate := range []int{48000, 96000} {
		path := ffmpegSine(t, filepath.Join(dir, "truehd.mp4"), 2, rate, "-c:a", "truehd", "-strict", "-2")
		probe := ffprobeStream(t, path)
		tr := mustParseFile(t, path).Properties().Tracks[0]
		if tr.Codec != "TrueHD" || tr.SampleRate != probe.SampleRate || tr.SampleRate != rate || tr.BitsPerSample != 24 {
			t.Errorf("%d Hz: %s (%q) at %d Hz, %d bits; ffprobe %s at %d Hz", rate, tr.Codec, tr.CodecProfile, tr.SampleRate, tr.BitsPerSample, probe.CodecName, probe.SampleRate)
		}
		if tr.Bitrate == 0 || probe.BitRate == 0 || tr.Bitrate < probe.BitRate*9/10 || tr.Bitrate > probe.BitRate*11/10 {
			t.Errorf("%d Hz: bitrate %d, ffprobe %d", rate, tr.Bitrate, probe.BitRate)
		}
	}
}
