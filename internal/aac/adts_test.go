package aac

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/waxerr"
)

// buildADTS assembles a 7-byte ADTS fixed header (no CRC) from decoded fields.
// profile is the 2-bit ADTS profile (object type - 1).
func buildADTS(profile, sfIndex, chanConfig, frameLen int) []byte {
	b := make([]byte, 7)
	b[0] = 0xFF
	b[1] = 0xF1 // sync low | MPEG-4 | layer 00 | no CRC
	b[2] = byte(profile<<6) | byte(sfIndex<<2) | byte((chanConfig>>2)&1)
	b[3] = byte((chanConfig&3)<<6) | byte((frameLen>>11)&3)
	b[4] = byte((frameLen >> 3) & 0xFF)
	b[5] = byte((frameLen & 7) << 5)
	return b
}

func TestDecodeADTSValid(t *testing.T) {
	h, ok := decodeADTS(buildADTS(1, 4, 2, 384)) // LC, 44100, stereo
	if !ok {
		t.Fatal("valid ADTS header rejected")
	}
	if h.objectType != 2 || h.sfIndex != 4 || h.sampleRate != 44100 || h.chanConfig != 2 || h.channels != 2 || h.frameLength != 384 {
		t.Errorf("decoded = %+v, want {objectType:2 sfIndex:4 sampleRate:44100 chanConfig:2 channels:2 frameLength:384}", h)
	}
}

func TestDecodeADTSRejects(t *testing.T) {
	valid := buildADTS(1, 4, 2, 384)
	cases := []struct {
		name string
		b    []byte
	}{
		{"short", valid[:6]},
		{"bad sync byte0", append([]byte{0xFE}, valid[1:]...)},
		{"bad sync nibble", func() []byte { b := append([]byte(nil), valid...); b[1] = 0xE1; return b }()},
		{"layer nonzero", func() []byte { b := append([]byte(nil), valid...); b[1] |= 0x02; return b }()},
		{"reserved profile", buildADTS(3, 4, 2, 384)},
		{"reserved sfIndex", buildADTS(1, 13, 2, 384)},
		{"frame length below header", buildADTS(1, 4, 2, 3)},
	}
	for _, c := range cases {
		if _, ok := decodeADTS(c.b); ok {
			t.Errorf("%s: accepted an invalid ADTS header", c.name)
		}
	}
}

// TestDecodeADTSConfigIndependentOfFrameLength: identical static config with
// different frame_length must decode equal (essence digest hashes decoded config).
func TestDecodeADTSConfigIndependentOfFrameLength(t *testing.T) {
	a, ok1 := decodeADTS(buildADTS(1, 4, 2, 384))
	b, ok2 := decodeADTS(buildADTS(1, 4, 2, 700))
	if !ok1 || !ok2 {
		t.Fatal("valid headers rejected")
	}
	if a.frameLength == b.frameLength {
		t.Fatal("test setup: frame lengths should differ")
	}
	if a.objectType != b.objectType || a.sfIndex != b.sfIndex || a.chanConfig != b.chanConfig {
		t.Errorf("static config differs with frame length: %+v vs %+v", a, b)
	}
}

func TestEssenceExtentConfig(t *testing.T) {
	d := &doc{header: adtsHeader{objectType: 2, sfIndex: 4, chanConfig: 2}}
	ver, cfg := Codec{}.EssenceExtent(&core.Media{Native: d})
	if ver != "aac-adts-v2" {
		t.Errorf("extent version = %q, want aac-adts-v2", ver)
	}
	// Static fields only; never per-frame frame_length.
	if !bytes.Equal(cfg, []byte{2, 4, 2}) {
		t.Errorf("config = %v, want [2 4 2]", cfg)
	}
}

// Resync confirms an ADTS frame only by a second header at its frame length, inside the window, that
// agrees on object type, rate index and channel config.
func TestResyncNeedsAnAgreeingSecondHeader(t *testing.T) {
	frame := func(profile, sfIndex, chanConfig int) []byte {
		b := make([]byte, 64)
		copy(b, buildADTS(profile, sfIndex, chanConfig, len(b)))
		return b
	}
	lc := frame(1, 4, 2)
	for _, c := range []struct {
		name   string
		window []byte
		off    int
		ok     bool
	}{
		{"two agreeing frames", slices.Concat([]byte{1, 2}, lc, lc), 2, true},
		{"lone frame", slices.Concat([]byte{1, 2}, lc), 0, false},
		{"second header cut by the window", slices.Concat(lc, lc[:adtsHeaderSize-1]), 0, false},
		{"another object type", slices.Concat(lc, frame(0, 4, 2)), 0, false},
		{"another rate index", slices.Concat(lc, frame(1, 3, 2)), 0, false},
		{"another channel config", slices.Concat(lc, frame(1, 4, 1)), 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if off, ok := (Codec{}).Resync(c.window); off != c.off || ok != c.ok {
				t.Errorf("Resync = %d, %v; want %d, %v", off, ok, c.off, c.ok)
			}
		})
	}
}

// The frame walk reads only inside the file, so a read there that fails is an error: a window of
// frames, and past the ID3 tags a joined stream opens with, the tags' headers and the frame headers
// after them, read from the file when the tags outrun the window. A window past the end of the file,
// where an end beyond it puts the walk, ends the walk with no error.
func TestWalkFailedRead(t *testing.T) {
	frame := slices.Concat(buildADTS(1, 4, 2, 384), make([]byte, 377))
	first, _ := decodeADTS(frame)
	stream := bytes.Repeat(frame, 200) // more than one 64 KiB window
	tag := testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", strings.Repeat("x", 100<<10)))
	joined := slices.Concat(bytes.Repeat(frame, 10), tag, bytes.Repeat(frame, 10))
	after := 10*len(frame) + len(tag) // where the stream goes on past the tag
	for _, c := range []struct {
		name string
		data []byte
		from int
	}{
		{"a window of frames", stream, 70000},
		{"the header of a tag after the one passed", joined, after + 10},
		{"a frame header past the tag", joined, after + len(frame) + 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := adtsWalk{last: -1}
			err := w.walk(context.Background(), &testfile.Faulty{Src: core.BytesSource(c.data), Fails: testfile.From(int64(c.from))}, int64(len(c.data)), 1<<20, 100, first)
			if !errors.Is(err, syscall.EIO) || errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("err = %v, want the read's own failure", err)
			}
		})
	}
	w := adtsWalk{last: -1}
	if err := w.walk(context.Background(), core.BytesSource(stream[:1000]), int64(len(stream)), 1<<20, 100, first); err != nil || w.stop != 0 {
		t.Errorf("a window past the end of the file: stop %d, %v; want the walk ended at 0 with no error", w.stop, err)
	}
}
