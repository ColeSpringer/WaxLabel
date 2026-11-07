package mp3

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"syscall"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/waxerr"
)

// frame44 and frame48 are MPEG-1 Layer III frames at 128 kbps, 44.1 and 48 kHz, their bodies zero.
func frame44() []byte { return frameOf(417, 0x90) }
func frame48() []byte { return frameOf(384, 0x94) }

func frameOf(n int, rate byte) []byte {
	f := make([]byte, n)
	copy(f, []byte{0xFF, 0xFB, rate, 0x00})
	return f
}

func frames(f []byte, n int) []byte { return bytes.Repeat(f, n) }

// xingFrame is a 44.1 kHz frame whose Xing header counts frames and bytes.
func xingFrame(count, size uint32) []byte {
	f := frame44()
	copy(f[36:], "Xing") // past the header and the 32 bytes of stereo side information
	f[43] = 3            // frame count and byte count present
	binary.BigEndian.PutUint32(f[44:], count)
	binary.BigEndian.PutUint32(f[48:], size)
	return f
}

func junkOf(n int) []byte { return bytes.Repeat([]byte{0xA5}, n) }

// freeXingFrame is an n-byte free-format frame, its length unstated, whose Xing header counts itself.
func freeXingFrame(n int) []byte {
	f := frameOf(n, 0x00)
	copy(f[36:], "Xing")
	f[43] = 3
	binary.BigEndian.PutUint32(f[44:], 1)
	binary.BigEndian.PutUint32(f[48:], uint32(n))
	return f
}

// id3v1Tag is an empty ID3v1 tag, and id3v2Tag an ID3v2.4 tag of n zero bytes after its header.
func id3v1Tag() []byte { return append([]byte("TAG"), make([]byte, 125)...) }

func id3v2Tag(n int) []byte {
	return slices.Concat([]byte{'I', 'D', '3', 4, 0, 0, byte(n >> 21 & 0x7F), byte(n >> 14 & 0x7F), byte(n >> 7 & 0x7F), byte(n & 0x7F)}, make([]byte, n))
}

// span records the extent and volume of the reads a source serves.
type span struct {
	core.ReaderAtSized
	lo, hi, n int64
}

func (s *span) ReadAt(p []byte, off int64) (int, error) {
	s.lo, s.hi = min(s.lo, off), max(s.hi, off+int64(len(p)))
	n, err := s.ReaderAtSized.ReadAt(p, off)
	s.n += int64(n)
	return n, err
}

// The audio ends where the VBR header's byte count says when those bytes end by the tags, on the end
// of a frame, and no frame of the stream follows them; otherwise where the frame chain in the window
// before the tags ends, the bytes it is picked up past counted as a gap and the ID3 tags it goes on
// past as tags. When the byte count runs past the tags, fewer bytes than a header after the chain's
// last whole frame are the frame the cut left, and the audio runs to the tags; a count past the
// chain's end by less than the shortest frame of the stream counts no frame the file lacks. A chain
// whose last frame the tags cut, or no chain, leaves the end unknown. However long the file, the reads
// stay within that window, the frames before the counted bytes and one probe after them.
func TestAudioEnd(t *testing.T) {
	const limit = 1 << 20
	whole := 300 * 417
	second := slices.Concat(xingFrame(40, 40*417), frames(frame44(), 39))
	tag := bytes.Repeat([]byte{'A'}, 82) // what a tag between two runs of frames holds
	for _, c := range []struct {
		name   string
		data   []byte
		want   int
		past   int  // how far the byte count runs past the audio end, or the tags when it is unknown
		joined bool // the stream goes on where the count ends
		gap    int  // the bytes the chain is picked up past
		tags   int  // the ID3 tags between joined streams
	}{
		{"chain runs to the tags", frames(frame44(), 300), whole, 0, false, 0, 0},
		{"junk after the chain", slices.Concat(frames(frame44(), 300), junkOf(500)), whole, 0, false, 0, 0},
		{"last frame cut by the tags", slices.Concat(frames(frame44(), 300), frame44()[:200]), -1, 0, false, 0, 0},
		{"no chain in the window", slices.Concat(frames(frame44(), 10), junkOf(70000)), -1, 0, false, 0, 0},
		{"chain picked up after a break", slices.Concat(frames(frame44(), 200), junkOf(100), frames(frame44(), 100)), whole + 100, 0, false, 100, 0},
		{"a tag between two runs of frames", slices.Concat(frames(frame44(), 40), tag, frames(frame44(), 40), junkOf(10)), 80*417 + len(tag), 0, false, len(tag), 0},
		{"ID3 tags between two runs of frames", slices.Concat(frames(frame44(), 40), id3v2Tag(10), id3v1Tag(), frames(frame44(), 40), junkOf(10)),
			80*417 + 20 + 128, 0, false, 0, 2},
		{"an ID3 tag no frame follows", slices.Concat(frames(frame44(), 40), id3v2Tag(10), junkOf(500)), 40 * 417, 0, false, 0, 0},
		{"frames of another stream after the audio", slices.Concat(frames(frame44(), 300), frames(frame48(), 5)), whole, 0, false, 0, 0},
		{"a long stream read only at its end", frames(frame44(), 5000), 5000 * 417, 0, false, 0, 0},
		{"byte count reaching the tags", slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 299)), whole, 0, false, 0, 0},
		{"byte count short of the tags", slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 299), junkOf(500)), whole, 0, false, 0, 0},
		{"byte count short of junk longer than the window", slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 299), junkOf(70000)), whole, 0, false, 0, 0},
		{"byte count past the tags, junk after the frames", slices.Concat(xingFrame(300, uint32(whole+1000)), frames(frame44(), 299), junkOf(500)), whole, 1000, false, 0, 0},
		{"byte count past the tags, four bytes after the frames", slices.Concat(xingFrame(300, uint32(whole+1000)), frames(frame44(), 299), junkOf(4)), whole, 1000, false, 0, 0},
		{"byte count past the tags, a few bytes of the cut frame left", slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 298), frame44()[:3]), 299*417 + 3, 417 - 3, false, 0, 0},
		{"byte count past the tags, the cut frame's header left", slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 298), frame44()[:200]), -1, 217, false, 0, 0},
		{"byte count ending at the tags a joined file opens with", slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 299),
			id3v1Tag(), id3v2Tag(40), id3v2Tag(10), second, junkOf(500)), whole + 128 + 50 + 20 + len(second), 0, true, 0, 3},
		{"byte count ending at tags before the window", slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 299),
			id3v1Tag(), id3v2Tag(10), frames(frame44(), 200), junkOf(500)), whole + 128 + 20 + 200*417, 0, true, 0, 2},
		// The window opens 47 bytes before the tag, so its first chain starts behind it.
		{"byte count ending at a tag the window's first chain starts past", slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 299),
			id3v2Tag(10), frames(frame44(), 157)), whole + 20 + 157*417, 0, true, 0, 1},
		{"byte count ending at a tag running past the tags", slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 299), id3v2Tag(5000)[:300]), whole, 0, false, 0, 0},
		{"byte count inside the first frame", slices.Concat(xingFrame(300, 100), frames(frame44(), 299), junkOf(500)), whole, 0, false, 0, 0},
		{"byte count a byte short of the tags", slices.Concat(xingFrame(300, uint32(whole-1)), frames(frame44(), 299)), whole, 0, false, 0, 0},
		{"byte count ending in the junk after the frames", slices.Concat(xingFrame(300, uint32(whole+10)), frames(frame44(), 299), junkOf(500)), whole, 0, false, 0, 0},
		{"byte count a byte past the tags", slices.Concat(xingFrame(300, uint32(whole+1)), frames(frame44(), 299)), whole, 0, false, 0, 0},
		{"byte count past the frames by less than a frame, junk after them", slices.Concat(xingFrame(300, uint32(whole+100)), frames(frame44(), 299), junkOf(50)), whole, 0, false, 0, 0},
		{"byte count past the tags by a whole frame", slices.Concat(xingFrame(301, uint32(whole+417)), frames(frame44(), 299)), whole, 417, false, 0, 0},
		{"byte count of a short stream, junk after it", slices.Concat(xingFrame(3, 3*417), frames(frame44(), 2), junkOf(500)), 3 * 417, 0, false, 0, 0},
		{"byte count ending a frame behind a gap", slices.Concat(xingFrame(300, uint32(whole+10)), frames(frame44(), 298), junkOf(10), frame44(), junkOf(500)),
			whole + 10, 0, false, 0, 0},
		{"byte count ending a frame behind an ID3v1 tag", slices.Concat(xingFrame(300, uint32(whole+128)), frames(frame44(), 298), id3v1Tag(), frame44(), junkOf(500)),
			whole + 128, 0, false, 0, 0},
		{"byte count past the frames by the shortest frame, junk after them", slices.Concat(xingFrame(300, uint32(whole+104)), frames(frame44(), 299), junkOf(50)), whole, 104, false, 0, 0},
		{"byte count past the frames by a byte less, junk after them", slices.Concat(xingFrame(300, uint32(whole+103)), frames(frame44(), 299), junkOf(50)), whole, 0, false, 0, 0},
		{"byte count of a free-format stream, junk after it", slices.Concat(freeXingFrame(1000), junkOf(500)), 1000, 0, false, 0, 0},
		{"byte count a byte short of a short stream, junk after it", slices.Concat(xingFrame(3, 3*417-1), frames(frame44(), 2), junkOf(500)), 3 * 417, 0, false, 0, 0},
		{"byte count of a stream another follows", slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 299), second, junkOf(500)), whole + len(second), 0, true, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, f, ok := parseMPEG(c.data[:min(len(c.data), scanWindow)])
			if !ok {
				t.Fatal("no first frame")
			}
			src := &span{ReaderAtSized: core.BytesSource(c.data), lo: math.MaxInt64}
			boundary := int64(len(c.data))
			got, err := audioEnd(context.Background(), src, f, 0, boundary, limit)
			if err != nil || got.end != int64(c.want) || got.past != int64(c.past) || got.joined != c.joined || got.gap != int64(c.gap) ||
				got.tags != c.tags {
				t.Errorf("audioEnd = %+v, %v; want end %d, past %d, joined %v, gap %d, tags %d", got, err, c.want, c.past, c.joined, c.gap, c.tags)
			}
			if bound := int64(resyncWindow + countSpan + maxFrameLen + 4 + core.MaxLeadingTags*128); src.n > bound || src.hi > boundary {
				t.Errorf("read %d bytes up to offset %d; want at most %d bytes, none past %d", src.n, src.hi, bound, boundary)
			}
			if f.vbrBytes == 0 && src.lo < boundary-resyncWindow {
				t.Errorf("read from offset %d, before the window that starts at %d", src.lo, boundary-resyncWindow)
			}
		})
	}
}

// A window past the end of the file is no end, while a read that fails inside the file is an error:
// the window before the tags, and after a VBR byte count the tag headers and the frame probed there,
// and the frames before it. A cancelled parse stops before reading.
func TestAudioEndReads(t *testing.T) {
	data := slices.Concat(frames(frame44(), 300), junkOf(500))
	_, f, _ := parseMPEG(data)
	short := &span{ReaderAtSized: core.BytesSource(data[:1000]), lo: math.MaxInt64}
	if got, err := audioEnd(context.Background(), short, f, 0, int64(len(data)), 1<<20); got.end != -1 || err != nil {
		t.Errorf("a window past the end of the file: audioEnd = %+v, %v; want end -1, nil", got, err)
	}
	whole := 300 * 417
	counted := slices.Concat(xingFrame(300, uint32(whole)), frames(frame44(), 299), junkOf(500))
	for _, c := range []struct {
		name  string
		data  []byte
		fails func(off, n int64) bool
	}{
		{"the window before the tags", data, testfile.From(int64(len(data) - 100))},
		{"the tag headers after the byte count", counted, testfile.From(int64(whole + 10))},
		{"the frame probed after the byte count", counted, testfile.From(int64(whole + 129))},
		{"the frames before the byte count", counted, testfile.Covering(int64(whole - 100))},
	} {
		_, f, _ := parseMPEG(c.data)
		src := &testfile.Faulty{Src: core.BytesSource(c.data), Fails: c.fails}
		if _, err := audioEnd(context.Background(), src, f, 0, int64(len(c.data)), 1<<20); !errors.Is(err, syscall.EIO) ||
			errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("%s failing: err = %v, want the read's own failure", c.name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := &span{ReaderAtSized: core.BytesSource(data), lo: math.MaxInt64}
	if _, err := audioEnd(ctx, src, f, 0, int64(len(data)), 1<<20); err == nil || src.n != 0 {
		t.Errorf("cancelled: err %v after %d bytes read; want the context's error before any read", err, src.n)
	}
}

// The shortest frame a stream can hold is its version, layer and rate at the lowest bitrate its
// channel mode allows, unpadded: MPEG-1 Layer II allows 32 to 56 kbps in mono only.
func TestMinFrameLen(t *testing.T) {
	for _, c := range []struct {
		name   string
		header []byte
		want   int
	}{
		{"MPEG-1 Layer III, 44.1 kHz, padded", []byte{0xFF, 0xFB, 0x92, 0x00}, 104},
		{"MPEG-2 Layer III, 22.05 kHz", []byte{0xFF, 0xF3, 0x90, 0x00}, 26},
		{"MPEG-2.5 Layer III, 8 kHz", []byte{0xFF, 0xE3, 0x98, 0x00}, 72},
		{"MPEG-1 Layer I, 48 kHz", []byte{0xFF, 0xFF, 0x94, 0x00}, 32},
		{"MPEG-1 Layer II, 44.1 kHz, stereo", []byte{0xFF, 0xFD, 0x90, 0x00}, 208},
		{"MPEG-1 Layer II, 44.1 kHz, mono", []byte{0xFF, 0xFD, 0x90, 0xC0}, 104},
	} {
		f, ok := decodeHeader(c.header)
		if got := minFrameLen(f); !ok || got != c.want {
			t.Errorf("%s: %d (decoded %v), want %d", c.name, got, ok, c.want)
		}
	}
}
