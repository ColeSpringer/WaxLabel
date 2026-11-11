package mp3

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// lameFrame is a 44.1 kHz frame whose Xing header, its fields chosen by flags, counts count frames
// and is followed by the extension encoder writes, stating delay and padding.
func lameFrame(flags byte, count uint32, encoder string, delay, padding int) []byte {
	f := frame44()
	copy(f[36:], "Info")
	f[43] = flags
	at := 44
	if flags&1 != 0 {
		binary.BigEndian.PutUint32(f[at:], count)
		at += 4
	}
	if flags&2 != 0 {
		at += 4
	}
	if flags&4 != 0 {
		at += 100
	}
	if flags&8 != 0 {
		at += 4
	}
	copy(f[at:at+9], encoder)
	v := delay<<12 | padding
	f[at+21], f[at+22], f[at+23] = byte(v>>16), byte(v>>8), byte(v)
	return f
}

// The delay and padding an encoder states behind the Xing header are read wherever the header's
// optional fields put them, and only for the encoder strings ffmpeg trusts.
func TestReadVBRReadsEncoderDelays(t *testing.T) {
	for _, c := range []struct {
		name           string
		flags          byte
		encoder        string
		delay, padding int
		trusted        bool
	}{
		{"every field ahead", 0x0F, "LAME3.100", 576, 1080, true},
		{"frame count alone", 0x01, "LAME3.100", 576, 1080, true},
		{"no optional field", 0x00, "Lavc61.19", 1105, 4095, true},
		{"libavformat", 0x07, "Lavf61.7.", 576, 0, true},
		{"an encoder ffmpeg does not trust", 0x0F, "Gogo3.13 ", 576, 1080, false},
	} {
		frame := lameFrame(c.flags, 100, c.encoder, c.delay, c.padding)
		info, ok := decodeHeader(frame)
		if !ok {
			t.Fatal("setup: the frame header does not decode")
		}
		readVBR(frame, &info)
		wantDelay, wantPadding := c.delay, c.padding
		if !c.trusted {
			wantDelay, wantPadding = 0, 0
		}
		if info.encDelay != wantDelay || info.encPadding != wantPadding {
			t.Errorf("%s: delay %d, padding %d; want %d, %d", c.name, info.encDelay, info.encPadding, wantDelay, wantPadding)
		}
	}
	// A frame the extension does not fit in states nothing.
	short := lameFrame(0x0F, 100, "LAME3.100", 576, 1080)[:160]
	info, _ := decodeHeader(short)
	readVBR(short, &info)
	if info.encDelay != 0 || info.encPadding != 0 || info.vbrFrames != 100 {
		t.Errorf("cut frame: delay %d, padding %d, frames %d; want the frame count alone", info.encDelay, info.encPadding, info.vbrFrames)
	}
}

// A VBR header is read from the frame that holds it: one too short for the fields its flags
// promise states no delay, whatever the next frame holds where they would lie.
func TestParseMPEGReadsTheHeaderInsideItsFrame(t *testing.T) {
	short := frameOf(104, 0x10) // 32 kbps
	copy(short[36:], "Info")
	short[43] = 0x0F // frame count, byte count, seek table and quality: 120 bytes the frame lacks
	binary.BigEndian.PutUint32(short[44:], 7)
	next := frameOf(104, 0x10)
	// Where the extension would lie, 156 bytes into the window: an encoder name and delays.
	const ext = 36 + 8 + 4 + 4 + 100 + 4 - 104
	copy(next[ext:], "LAME3.100")
	next[ext+21], next[ext+22], next[ext+23] = 0x24, 0x04, 0x38
	_, info, ok := parseMPEG(slices.Concat(short, next, frames(frameOf(104, 0x10), 3)))
	if !ok || info.vbrFrames != 7 || info.encDelay != 0 || info.encPadding != 0 {
		t.Errorf("ok %v, frames %d, delay %d, padding %d; want the frame count and no delays", ok, info.vbrFrames, info.encDelay, info.encPadding)
	}
}

// A walked count takes the place of a frame count the header does not state: the header's own
// frame is no audio, and the delay and padding it states are trimmed as with a count.
func TestBuildTrackWalkedBehindAHeader(t *testing.T) {
	frame := lameFrame(0x00, 0, "LAME3.100", 576, 1080)
	info, _ := decodeHeader(frame)
	readVBR(frame, &info)
	if !info.vbrHeader {
		t.Fatal("setup: the header went unread")
	}
	tr := buildTrack(info, 417+100*626, frameRun{frames: 101, bytes: 417 + 100*626})
	if tr.TotalSamples != 100*1152-576-1080 {
		t.Errorf("TotalSamples %d, want %d", tr.TotalSamples, 100*1152-576-1080)
	}
	if want := core.AverageBitrate(100*626, core.SamplesToDuration(100*1152, 44100).Seconds()); tr.Bitrate != want {
		t.Errorf("bitrate %d, want the audio frames' %d", tr.Bitrate, want)
	}
}

// The played length is the frames' samples less the encoder's delay and padding; figures that
// would leave nothing are not believed. The bitrate stays the average over the frames as encoded.
func TestBuildTrackTrimsEncoderDelays(t *testing.T) {
	info, _ := decodeHeader(frame44())
	info.vbrFrames = 100
	untrimmed := buildTrack(info, 100*417, frameRun{})
	info.encDelay, info.encPadding = 576, 1080
	tr := buildTrack(info, 100*417, frameRun{})
	if tr.TotalSamples != 100*1152-576-1080 || tr.Duration != core.SamplesToDuration(tr.TotalSamples, 44100) {
		t.Errorf("TotalSamples %d, duration %v; want %d samples", tr.TotalSamples, tr.Duration, 100*1152-576-1080)
	}
	if tr.Bitrate != untrimmed.Bitrate || tr.Bitrate == 0 {
		t.Errorf("bitrate %d, want the untrimmed stream's %d", tr.Bitrate, untrimmed.Bitrate)
	}
	info.vbrFrames, info.encDelay, info.encPadding = 1, 4095, 4095
	if got := buildTrack(info, 417, frameRun{}).TotalSamples; got != 1152 {
		t.Errorf("TotalSamples = %d, want the untrimmed 1152", got)
	}
}

// A walked frame count gives a stream with no VBR header its length, and the bytes of those
// frames its average bitrate, whatever else the essence holds.
func TestBuildTrackFromWalkedFrames(t *testing.T) {
	info, _ := decodeHeader(frame44())
	run := frameRun{frames: 20, bytes: 10*417 + 10*626}
	tr := buildTrack(info, run.bytes+1<<20, run)
	if tr.TotalSamples != 20*1152 || tr.Duration != core.SamplesToDuration(20*1152, 44100) {
		t.Errorf("TotalSamples %d, duration %v; want 20 frames' worth", tr.TotalSamples, tr.Duration)
	}
	if want := core.AverageBitrate(run.bytes, tr.Duration.Seconds()); tr.Bitrate != want {
		t.Errorf("bitrate = %d, want the frames' average %d", tr.Bitrate, want)
	}
}

// With no frame count at all the bytes at the frame's constant bitrate give the length.
func TestBuildTrackEstimatesAConstantBitrate(t *testing.T) {
	info, _ := decodeHeader(frame44())
	tr := buildTrack(info, 100*417, frameRun{})
	if tr.Bitrate != 128000 || tr.Duration != 100*417*8*time.Second/128000 {
		t.Errorf("bitrate %d, duration %v; want 128 kbps over the bytes", tr.Bitrate, tr.Duration)
	}
}

// Every version and layer keeps the name it had.
func TestCodecNames(t *testing.T) {
	for header, want := range map[byte]string{0xFB: "MPEG-1 Layer 3", 0xFD: "MPEG-1 Layer 2", 0xFF: "MPEG-1 Layer 1",
		0xF3: "MPEG-2 Layer 3", 0xF5: "MPEG-2 Layer 2", 0xE3: "MPEG-2.5 Layer 3", 0xE7: "MPEG-2.5 Layer 1"} {
		if info, ok := decodeHeader([]byte{0xFF, header, 0x90, 0x00}); !ok || info.codec != want {
			t.Errorf("header FF %02X: codec %q (ok %v), want %q", header, info.codec, ok, want)
		}
	}
}

// frame192 is an MPEG-1 Layer III frame at 192 kbps and 44.1 kHz.
func frame192() []byte { return frameOf(626, 0xB0) }

func TestMixedBitrates(t *testing.T) {
	f, _ := decodeHeader(frame44())
	for _, c := range []struct {
		name   string
		window []byte
		want   bool
	}{
		{"one bitrate", frames(frame44(), 20), false},
		{"a padded frame of the same bitrate", slices.Concat(frame44(), frameOf(418, 0x92), frame44()), false},
		{"a second bitrate", slices.Concat(frames(frame44(), 5), frame192(), frame44()), true},
		{"a second bitrate behind junk", slices.Concat(frames(frame44(), 5), junkOf(9), frame192()), false},
		{"a last frame the window cuts", slices.Concat(frames(frame44(), 5), frame192()[:3]), false},
	} {
		if got := mixedBitrates(c.window, f); got != c.want {
			t.Errorf("%s: mixedBitrates = %v, want %v", c.name, got, c.want)
		}
	}
}

// The walk counts the whole frames of the stream in the ranges it is given, across the 64 KiB it
// reads at a time, and goes on past bytes that are no frame of it at the next frame another confirms.
func TestWalkMPEGFrames(t *testing.T) {
	f, _ := decodeHeader(frame44())
	mixed := slices.Concat(frames(frame44(), 300), frames(frame192(), 300), frames(frame44(), 7))
	lone := slices.Concat(frame44()[:4], junkOf(40)) // a header no frame follows
	walk := func(ctx context.Context, data []byte, limit int64, ranges ...[2]int64) (uint64, error) {
		if ranges == nil {
			ranges = [][2]int64{{0, int64(len(data))}}
		}
		run, err := walkMPEGFrames(ctx, core.BytesSource(data), f, ranges, limit)
		return run.frames, err
	}
	for _, c := range []struct {
		name   string
		data   []byte
		ranges [][2]int64 // nil for the whole of data
		frames uint64
	}{
		{"mixed bitrates across reads", mixed, nil, 607},
		{"from an offset", slices.Concat(junkOf(13), mixed), [][2]int64{{13, 13 + int64(len(mixed))}}, 607},
		{"junk between frames", slices.Concat(frames(frame44(), 3), junkOf(50), frames(frame44(), 3)), nil, 6},
		{"a tag between frames", slices.Concat(frames(frame44(), 3), id3v2Tag(70000), frames(frame192(), 2)), nil, 5},
		{"a header in the junk that no frame follows", slices.Concat(frames(frame44(), 3), lone, frames(frame44(), 2)), nil, 5},
		{"one frame after the junk, ending the stream", slices.Concat(frames(frame44(), 3), junkOf(50), frame44()), nil, 4},
		{"frames opening where a read of junk ends", slices.Concat(frame44(), junkOf(walkBuffer-417), frames(frame44(), 3)), nil, 4},
		{"another stream after the frames", slices.Concat(frames(frame44(), 3), frames(frame48(), 3)), nil, 3},
		{"a cut last frame", slices.Concat(frames(frame44(), 3), frame44()[:200]), nil, 3},
		{"a free-format frame", slices.Concat(frames(frame44(), 2), frameOf(417, 0x00)), nil, 2},
		{"two ranges around a tag", slices.Concat(frames(frame44(), 3), frames(frame192(), 1), frames(frame44(), 2)),
			[][2]int64{{0, 3 * 417}, {3*417 + 626, 5*417 + 626}}, 5},
		{"a second range opening with a lone header", slices.Concat(frames(frame44(), 3), lone, frames(frame44(), 2)),
			[][2]int64{{0, 3 * 417}, {3 * 417, 5*417 + 44}}, 5},
	} {
		if n, err := walk(context.Background(), c.data, 1<<20, c.ranges...); err != nil || n != c.frames {
			t.Errorf("%s: %d frames, err %v; want %d", c.name, n, err, c.frames)
		}
	}

	// The bytes counted are the frames' own, not the junk walked past.
	junked := slices.Concat(frames(frame44(), 3), junkOf(70000), frames(frame192(), 2))
	if run, _ := walkMPEGFrames(context.Background(), core.BytesSource(junked), f, [][2]int64{{0, int64(len(junked))}}, 1<<20); run != (frameRun{5, 3*417 + 2*626}) {
		t.Errorf("run past junk = %+v, want 5 frames of %d bytes", run, 3*417+2*626)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := walk(ctx, mixed, 1<<20); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled walk: err = %v, want context.Canceled", err)
	}
	// A range the source ends inside is walked as far as the source goes.
	short := frames(frame44(), 5)
	if run, err := walkMPEGFrames(context.Background(), core.BytesSource(short), f, [][2]int64{{0, int64(len(short)) + 5000}}, 1<<20); err != nil || run.frames != 5 {
		t.Errorf("a range past the end of the source: %+v, err %v; want the 5 frames it holds", run, err)
	}
	// The walk reads into one buffer: a long stream costs no allocation per read.
	long := core.BytesSource(frames(frame44(), 2000))
	if allocs := testing.AllocsPerRun(5, func() {
		if _, err := walkMPEGFrames(context.Background(), long, f, [][2]int64{{0, 2000 * 417}}, 1<<20); err != nil {
			t.Fatal(err)
		}
	}); allocs > 6 {
		t.Errorf("%v allocations for a 13-read walk, want the buffer and little else", allocs)
	}
	// A read stays inside the allocation limit.
	if _, err := walk(context.Background(), mixed, 1024); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("walk under a 1 KiB limit: err = %v, want ErrSizeTooLarge", err)
	}
}

// walkInMemory is the walk's rule over bytes held whole, without its buffer or its skip to the next
// sync byte.
func walkInMemory(data []byte, f mpegInfo, ranges [][2]int64) (run frameRun) {
	frameAt := func(pos, end int64) int64 {
		if pos+4 > end {
			return 0
		}
		if h, ok := decodeHeader(data[pos:]); ok && sameStream(f, h) {
			return int64(frameLength(h))
		}
		return 0
	}
	confirmed := true
	for _, r := range ranges {
		for pos := r[0]; pos+4 <= r[1]; {
			n := frameAt(pos, r[1])
			if n > 0 && !confirmed && pos+n != r[1] && frameAt(pos+n, r[1]) == 0 {
				n = 0
			}
			switch {
			case n == 0:
				pos, confirmed = pos+1, false
			case pos+n > r[1]:
				pos = r[1]
			default:
				run.frames, run.bytes, pos, confirmed = run.frames+1, run.bytes+n, pos+n, true
			}
		}
		confirmed = false
	}
	return run
}

// Streams of frames, junk dense with sync bytes and headers nothing follows, longer than the walk's
// buffer and cut into ranges anywhere, count the same read 64 KiB at a time as held whole.
func TestWalkMPEGFramesMatchesAnInMemoryWalk(t *testing.T) {
	f, _ := decodeHeader(frame44())
	rng := rand.New(rand.NewPCG(1, 2))
	for range 60 {
		data := frames(frame44(), 1+rng.IntN(3))
		for len(data) < 150<<10 {
			switch rng.IntN(6) {
			case 0:
				data = append(data, frames(frame44(), 1+rng.IntN(40))...)
			case 1:
				data = append(data, frames(frame192(), 1+rng.IntN(40))...)
			case 2:
				data = append(data, junkOf(rng.IntN(9000))...)
			case 3:
				data = append(data, bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, rng.IntN(2000))...)
			case 4:
				data = append(data, bytes.Repeat([]byte{0xFF}, rng.IntN(300))...)
			default:
				data = append(data, frame48()[:rng.IntN(384)]...)
			}
		}
		cut := int64(rng.IntN(len(data)))
		for _, ranges := range [][][2]int64{{{0, int64(len(data))}}, {{0, cut}, {cut + int64(rng.IntN(50)), int64(len(data))}}} {
			ranges[len(ranges)-1][0] = min(ranges[len(ranges)-1][0], int64(len(data)))
			got, err := walkMPEGFrames(context.Background(), core.BytesSource(data), f, ranges, 1<<20)
			if want := walkInMemory(data, f, ranges); err != nil || got != want {
				t.Fatalf("ranges %v of %d bytes: %+v (err %v), want %+v", ranges, len(data), got, err, want)
			}
		}
	}
}
