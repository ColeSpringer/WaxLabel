package waxlabel_test

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestAIFFCPacketizedEssenceUnchanged: the packet arithmetic touches only the reported
// track. The aiff-ssnd-v2 salt carries the COMM geometry and compression type, never the
// frame count, and the hashed range is the SSND sample bytes, so an ima4 digest keeps its
// extent, survives a tag edit, and stays the value minted before the packet count was
// understood.
func TestAIFFCPacketizedEssenceUnchanged(t *testing.T) {
	t.Parallel()
	data := aiffFile("AIFC", aiffText("NAME", "Packets"), aiffCOMMC(1, 690, 4, 44100, "ima4"), aiffSSND(690*34))
	before := essenceOf(t, data)
	if got := before.String(); got != "sha256/aiff-ssnd-v2:b09ea364c32d3e22d90f62b4c103efec8317b786beed62a940f66b7e124e0484" {
		t.Errorf("digest = %s, want the value minted before the packet fix", got)
	}
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if after := essenceOf(t, applyToBytes(t, data, plan)); !before.Equal(after) {
		t.Error("essence changed across a tag edit of an ima4 AIFF-C")
	}
}

// aiffGeometryRow is one AIFF-C layout the reader sizes: the COMM it is built from, the
// SSND bytes a whole file of it holds, and what the track must read. oracle says ffmpeg's
// AIFF demuxer opens the file, so ffprobe can witness the same figures; the rows it
// rejects (an ISOBMFF-only fourcc, a type it has no decoder for, the "ms" spelling) or
// misreads (a 20-bit sowt, which it takes for 16-bit while reading the big-endian twin at
// 24) are pinned by these expectations alone.
type aiffGeometryRow struct {
	name       string
	channels   int
	count      int
	size       int
	comp       string
	ssnd       int
	samples    uint64
	depth      int
	bitrate    int
	halfSamps  uint64 // reported when only half the SSND bytes survive
	overstated bool   // COMM declares more packets than the whole SSND holds
	oracle     bool
}

var aiffGeometryRows = []aiffGeometryRow{
	{"ima4 mono as ffmpeg writes it", 1, 690, 4, "ima4", 690 * 34, 44160, 4, 187425, 22080, false, true},
	// QuickTime writes the decoded 16 into sampleSize; the stream is still 4-bit.
	{"ima4 stereo as QuickTime writes it", 2, 690, 16, "ima4", 690 * 68, 44160, 4, 374850, 22080, false, true},
	{"MAC3 stereo", 2, 100, 8, "MAC3", 100 * 4, 600, 8, 235200, 300, false, true},
	{"MAC6 mono", 1, 100, 8, "MAC6", 100 * 1, 600, 8, 58800, 300, false, true},
	{"mac3 lowercase, as ffmpeg also reads it", 2, 100, 8, "mac3", 100 * 4, 600, 8, 235200, 300, false, true},
	{"ulaw mono", 1, 44100, 8, "ulaw", 44100, 44100, 8, 352800, 22050, false, true},
	{"alaw stereo claiming 16 bits", 2, 44100, 16, "alaw", 44100 * 2, 44100, 8, 705600, 22050, false, true},
	{"in24 stereo", 2, 1000, 24, "in24", 1000 * 6, 1000, 24, 2116800, 500, false, true},
	{"fl64 mono", 1, 1000, 64, "fl64", 1000 * 8, 1000, 64, 2822400, 500, false, true},
	{"NONE stereo", 2, 1000, 16, "NONE", 1000 * 4, 1000, 16, 1411200, 500, false, true},
	{"sowt 20-bit in 3 bytes", 2, 1000, 20, "sowt", 1000 * 6, 1000, 20, 2116800, 500, false, false},
	// An ISOBMFF spelling with no AIFF-C meaning of its own: byte-linear at COMM's
	// width, as the shared table says, rather than an unknown layout.
	{"fpcm at COMM's 32 bits", 2, 1000, 32, "fpcm", 1000 * 8, 1000, 32, 2822400, 500, false, false},
	// QuickTime's "ms" + WAVE-tag spellings of the byte-linear tags: mu-law is 8-bit
	// whatever COMM says, PCM is COMM's width.
	{"ms mu-law tag", 1, 44100, 16, "ms\x00\x07", 44100, 44100, 8, 352800, 22050, false, false},
	{"ms PCM tag", 2, 1000, 16, "ms\x00\x01", 1000 * 4, 1000, 16, 1411200, 500, false, false},
	// A writer that stored the spec-literal frame count where QuickTime stores packets:
	// the SSND holds 690 packets, so 690 is what the count is capped to, where ffprobe
	// multiplies out to 64 seconds.
	{"ima4 spec-literal frame count", 1, 44160, 4, "ima4", 690 * 34, 44160, 4, 187425, 22080, true, false},
	// No known layout: the count is COMM's, the file's only statement of length, and no
	// bitrate is derived from it; truncation cannot move the count, since the bytes
	// cannot be mapped to frames.
	{"unknown layout", 2, 100, 16, "QDM2", 64, 100, 16, 0, 100, false, false},
}

// TestAIFFCPacketizedGeometry: COMM's numSampleFrames counts packets, not frames, for the
// QuickTime packetized types, so an ima4 stream read 64x short. The sample count, duration
// and bitrate follow the packet layout (64 frames in 34 bytes per channel for ima4, 6
// frames in 2 or 1 bytes per channel for MACE 3:1 and 6:1), the width is the one the type
// fixes whatever COMM stores, a byte-linear type stores whole bytes, and the count is
// capped by the packets the SSND holds, half of them gone or the declaration merely
// overstated, with the truncated-audio warning either way. The oracle rows' figures are
// ffprobe's for the same COMM.
func TestAIFFCPacketizedGeometry(t *testing.T) {
	t.Parallel()
	for _, c := range aiffGeometryRows {
		t.Run(c.name, func(t *testing.T) {
			comm := aiffCOMMC(c.channels, c.count, c.size, 44100, c.comp)
			doc := mustParseBytes(t, aiffFile("AIFC", comm, aiffSSND(c.ssnd)))
			tr := doc.Properties().First()
			if tr.TotalSamples != c.samples {
				t.Errorf("total samples = %d, want %d", tr.TotalSamples, c.samples)
			}
			if tr.BitsPerSample != c.depth {
				t.Errorf("bits per sample = %d, want %d", tr.BitsPerSample, c.depth)
			}
			if tr.Bitrate != c.bitrate {
				t.Errorf("bitrate = %d, want %d", tr.Bitrate, c.bitrate)
			}
			if !withinDuration(tr.Duration, sampleDuration(c.samples, 44100), time.Millisecond) {
				t.Errorf("duration = %v, want ~%v", tr.Duration, sampleDuration(c.samples, 44100))
			}
			// A whole file's chunks agree, so nothing warns, unless COMM overstates by design.
			if hasWarning(doc, wl.WarnTruncatedAudio) != c.overstated {
				t.Errorf("truncated-audio warning = %v, want %v for a whole file", !c.overstated, c.overstated)
			}

			// Half the SSND bytes gone: the surviving packets set the reported length.
			cut := mustParseBytes(t, aiffFile("AIFC", comm, truncatedSSND(8+c.ssnd, 8+c.ssnd/2)))
			if !hasWarning(cut, wl.WarnTruncatedAudio) {
				t.Error("a truncated SSND should flag truncated-audio")
			}
			ct := cut.Properties().First()
			if ct.TotalSamples != c.halfSamps {
				t.Errorf("truncated total samples = %d, want %d", ct.TotalSamples, c.halfSamps)
			}
			if !withinDuration(ct.Duration, sampleDuration(c.halfSamps, 44100), time.Millisecond) {
				t.Errorf("truncated duration = %v, want ~%v", ct.Duration, sampleDuration(c.halfSamps, 44100))
			}
			// Truncation costs length, never the stream's identity.
			if ct.BitsPerSample != c.depth || ct.Bitrate != c.bitrate {
				t.Errorf("truncated width %d / bitrate %d, want %d / %d", ct.BitsPerSample, ct.Bitrate, c.depth, c.bitrate)
			}
		})
	}
}

// TestAIFFCOverstatedCOMMWarns: a well-formed file whose COMM declares more frames than
// its SSND chunk holds reports the chunk's length and says so, the same truncated-audio
// code a FLAC raises when its frames stop short of STREAMINFO's count, since nothing
// else tells a caller the two chunks disagree; a file with no audio at all is the
// no-audio-frames condition instead, not this one.
func TestAIFFCOverstatedCOMMWarns(t *testing.T) {
	t.Parallel()
	over := mustParseBytes(t, aiffFile("AIFF", aiffCOMM(2, 1000, 16, 44100), aiffSSND(100*4)))
	if !hasWarning(over, wl.WarnTruncatedAudio) {
		t.Error("COMM overstating the SSND chunk should flag truncated-audio")
	}
	if got := over.Properties().First().TotalSamples; got != 100 {
		t.Errorf("total samples = %d, want the 100 the chunk holds", got)
	}
	whole := mustParseBytes(t, aiffFile("AIFF", stdCOMM(), stdSSND()))
	if hasWarning(whole, wl.WarnTruncatedAudio) {
		t.Error("a file whose chunks agree must not flag truncated-audio")
	}
	none := mustParseBytes(t, aiffFile("AIFF", stdCOMM(), aiffSSND(0)))
	if hasWarning(none, wl.WarnTruncatedAudio) || !hasWarning(none, wl.WarnNoAudioFrames) {
		t.Errorf("an empty SSND is no-audio-frames, not truncated-audio; got %v", none.Warnings())
	}
}

// TestAIFFStoredWidthBitrate: a sample width that is not a whole number of bytes is stored
// rounded up to whole bytes, so the bitrate follows the bytes on disk rather than the
// declared width, in plain AIFF as much as in AIFF-C. ffprobe reports the same figure.
func TestAIFFStoredWidthBitrate(t *testing.T) {
	t.Parallel()
	tr := mustParseBytes(t, aiffFile("AIFF", aiffCOMM(2, 1000, 20, 44100), aiffSSND(1000*6))).Properties().First()
	if tr.Bitrate != 2116800 {
		t.Errorf("bitrate = %d, want 2116800 (a 20-bit sample stores 3 bytes)", tr.Bitrate)
	}
	if tr.BitsPerSample != 20 || tr.TotalSamples != 1000 {
		t.Errorf("width %d / samples %d, want 20 / 1000", tr.BitsPerSample, tr.TotalSamples)
	}
}

// TestAIFFCHostileFrameCountCapped: a COMM declaring the maximum 32-bit packet count under
// ima4 would multiply out to (2^32-1)*64 frames, but the SSND holds two packets, so two is
// what it reports, with the nominal bitrate and no panic, on a 32-bit build as much as a
// 64-bit one.
func TestAIFFCHostileFrameCountCapped(t *testing.T) {
	t.Parallel()
	data := aiffFile("AIFC", aiffCOMMCFrames(1, math.MaxUint32, 4, 44100, "ima4"), aiffSSND(68))
	tr := mustParseBytes(t, data).Properties().First()
	if tr.TotalSamples != 128 || tr.Bitrate != 187425 {
		t.Errorf("samples %d / bitrate %d, want 128 / 187425", tr.TotalSamples, tr.Bitrate)
	}
	if !withinDuration(tr.Duration, sampleDuration(128, 44100), time.Microsecond) {
		t.Errorf("duration = %v, want ~%v", tr.Duration, sampleDuration(128, 44100))
	}
}

// TestAIFFDifferentialFFmpegPacketized encodes ima4, mu-law and A-law with the real ffmpeg
// and checks the sample count, duration, bitrate and width against ffprobe reading the same
// bytes: COMM counts ima4 packets, and ffprobe is the independent witness that the packet
// arithmetic lands on the figures a player sees. The .mov twin of each encode carries the
// same stream, so its duration must agree with the .aifc's. Then every geometry row ffmpeg
// can open, MACE included, which no ffmpeg encodes but every ffmpeg reads, is written out
// synthetically and probed the same way, so the packet constants are checked against
// ffmpeg's demuxer rather than only against the arithmetic that produced them.
func TestAIFFDifferentialFFmpegPacketized(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	dir := t.TempDir()

	// agree checks a parsed track against ffprobe's reading of the same file. ffprobe
	// counts duration_ts in the stream time base, which its AIFF demuxer sets to one
	// sample frame; the check is void unless that holds.
	agree := func(t *testing.T, path string, depth int) {
		t.Helper()
		tr := mustParseFile(t, path).Properties().First()
		p := ffprobeStream(t, path)
		if want := "1/" + strconv.Itoa(p.SampleRate); p.TimeBase != want {
			t.Fatalf("ffprobe time base = %q, want %q so duration_ts counts sample frames", p.TimeBase, want)
		}
		if tr.TotalSamples != uint64(p.DurationTS) {
			t.Errorf("total samples = %d, ffprobe duration_ts = %d", tr.TotalSamples, p.DurationTS)
		}
		if !withinDuration(tr.Duration, p.Duration, time.Millisecond) {
			t.Errorf("duration = %v, ffprobe says %v", tr.Duration, p.Duration)
		}
		if tr.Bitrate != p.BitRate {
			t.Errorf("bitrate = %d, ffprobe says %d", tr.Bitrate, p.BitRate)
		}
		// ffprobe names no width for MACE; where it names one, all three must agree.
		if tr.BitsPerSample != depth || (p.BitsPerSample != 0 && p.BitsPerSample != depth) {
			t.Errorf("bits per sample = %d, ffprobe says %d, want %d", tr.BitsPerSample, p.BitsPerSample, depth)
		}
		if tr.SampleRate != p.SampleRate || tr.Channels != p.Channels {
			t.Errorf("geometry = %d Hz / %d ch, ffprobe says %d/%d", tr.SampleRate, tr.Channels, p.SampleRate, p.Channels)
		}
	}

	for _, c := range []struct {
		name     string
		channels int
		codec    string
		depth    int
	}{
		{"ima4 mono", 1, "adpcm_ima_qt", 4},
		{"ima4 stereo", 2, "adpcm_ima_qt", 4},
		{"mulaw mono", 1, "pcm_mulaw", 8},
		{"alaw stereo", 2, "pcm_alaw", 8},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-"))
			aifc := ffmpegSine(t, base+".aifc", c.channels, 44100, "-c:a", c.codec)
			agree(t, aifc, c.depth)
			// ffmpeg pads the last ima4 packet in the .aifc and trims it with an edit list
			// in the .mov, so the two differ by under 2 ms; before the fix the .aifc read
			// 16 ms against the .mov's second.
			mov := ffmpegSine(t, base+".mov", c.channels, 44100, "-c:a", c.codec)
			aifcDur := mustParseFile(t, aifc).Properties().Duration()
			if movDur := mustParseFile(t, mov).Properties().Duration(); !withinDuration(movDur, aifcDur, 5*time.Millisecond) {
				t.Errorf(".mov duration = %v, .aifc duration = %v", movDur, aifcDur)
			}
		})
	}

	for _, c := range aiffGeometryRows {
		if !c.oracle {
			continue
		}
		t.Run("synthetic "+c.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-")+".aifc")
			data := aiffFile("AIFC", aiffCOMMC(c.channels, c.count, c.size, 44100, c.comp), aiffSSND(c.ssnd))
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			agree(t, path, c.depth)
		})
	}
}
