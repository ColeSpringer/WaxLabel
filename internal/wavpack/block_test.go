package wavpack

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/tail"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/waxerr"
)

// The 40-bit total is stored as the reference's encoder stores it, a low word and a high byte holding
// total + total/0xFFFFFFFF so the low word never takes the all-ones escape, and decodes as
// low + high<<32 - high.
func TestBlockHeaderTotalSamples(t *testing.T) {
	header := func(high byte, low uint32) []byte {
		b := make([]byte, blockHeaderLen)
		copy(b, blockMagic)
		binary.LittleEndian.PutUint32(b[4:8], blockHeaderLen-8)
		binary.LittleEndian.PutUint16(b[8:10], maxVersion)
		b[11] = high
		binary.LittleEndian.PutUint32(b[12:16], low)
		return b
	}
	for _, total := range []uint64{1, 66150, 0xFFFFFFFE, 0xFFFFFFFF, 1 << 32, 0x12_3456_789A} {
		stored := total + total/0xFFFFFFFF
		h, err := parseBlockHeader(header(byte(stored>>32), uint32(stored)), 0)
		if err != nil {
			t.Fatal(err)
		}
		if h.totalSamples != total || !h.statesTotal() {
			t.Errorf("total %#x stored as %#x: read %#x (stated %v)", total, stored, h.totalSamples, h.statesTotal())
		}
	}
	// The all-ones low word is the encoder never learning the length; ffmpeg writing to a pipe
	// leaves 0. Neither states a total.
	for _, low := range []uint32{0xFFFFFFFF, 0} {
		h, err := parseBlockHeader(header(0, low), 0)
		if err != nil {
			t.Fatal(err)
		}
		if h.statesTotal() {
			t.Errorf("a low word of %#x states a total of %d", low, h.totalSamples)
		}
	}
}

// wvBlock is a mono WavPack block of samples at index, the first and final of its sample group,
// stating total for the stream, followed by body.
func wvBlock(index uint64, samples uint32, total uint64, body []byte) []byte {
	b := make([]byte, blockHeaderLen)
	copy(b, blockMagic)
	binary.LittleEndian.PutUint32(b[4:8], uint32(blockHeaderLen-8+len(body)))
	binary.LittleEndian.PutUint16(b[8:10], maxVersion)
	stored := total + total/0xFFFFFFFF
	b[11] = byte(stored >> 32)
	binary.LittleEndian.PutUint32(b[12:16], uint32(stored))
	binary.LittleEndian.PutUint32(b[16:20], uint32(index))
	binary.LittleEndian.PutUint32(b[20:24], samples)
	binary.LittleEndian.PutUint32(b[24:28], flagMono|flagInitialBlock|flagFinalBlock)
	return append(b, body...)
}

// The block walk reads only inside the file, so a block header read that fails is an error, not an
// unknown end; a header past the end of the file, where a boundary beyond it puts the walk, leaves
// the end unknown with no error.
func TestBlockChainEndFailedRead(t *testing.T) {
	stream := slices.Concat(wvBlock(0, 100, 300, make([]byte, 64)), wvBlock(100, 100, 300, make([]byte, 64)),
		wvBlock(200, 100, 300, make([]byte, 64)))
	second := blockHeaderLen + 64
	c, err := blockChainEnd(context.Background(), &testfile.Faulty{Src: core.BytesSource(stream), Fails: testfile.From(int64(second + 4))}, 0, int64(len(stream)), 1<<20, 0)
	if !errors.Is(err, syscall.EIO) || errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("a failing header read: %+v, %v; want the read's own failure", c, err)
	}
	if c, err := blockChainEnd(context.Background(), core.BytesSource(stream[:second+10]), 0, int64(len(stream)), 1<<20, 0); err != nil || c.end != -1 {
		t.Errorf("a header past the end of the file: %+v, %v; want an unknown end and no error", c, err)
	}
	if c, err := blockChainEnd(context.Background(), core.BytesSource(stream), 0, int64(len(stream)), 1<<20, 0); err != nil || c.end != int64(len(stream)) {
		t.Errorf("the whole stream: %+v, %v; want its end", c, err)
	}
}

// wvGroup is a stereo sample group at index split in two mono blocks, the first initial and the
// second final, both stating total.
func wvGroup(index uint64, samples uint32, total uint64) []byte {
	a, b := wvBlock(index, samples, total, make([]byte, 64)), wvBlock(index, samples, total, make([]byte, 64))
	binary.LittleEndian.PutUint32(a[24:28], flagMono|flagInitialBlock)
	binary.LittleEndian.PutUint32(b[24:28], flagMono|flagFinalBlock)
	return slices.Concat(a, b)
}

// wvStream is n mono blocks of 100 samples each, the stream's total stated by every block.
func wvStream(n int) []byte { return wvRun(n, 100) }

// wvRun is n mono blocks of 100 samples, the last holding last samples, the total stated by every block.
func wvRun(n int, last uint32) []byte {
	total := uint64(n-1)*100 + uint64(last)
	var out []byte
	for i := range n {
		samples := uint32(100)
		if i == n-1 {
			samples = last
		}
		out = append(out, wvBlock(uint64(i)*100, samples, total, make([]byte, 64))...)
	}
	return out
}

// reads counts the bytes a source serves.
type reads struct {
	core.ReaderAtSized
	n int64
}

func (r *reads) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.ReaderAtSized.ReadAt(p, off)
	r.n += int64(n)
	return n, err
}

// A stream whose every block states its total ends at the tags when the blocks in the window before
// them run there and the last sample group among them reaches the total, metadata blocks of no
// samples after it: then the first block and that window are all the end costs, however long the
// stream. Every other shape is walked, block by block, and ends where the walk says: a break, a
// restart, a block of another version or another stream's total in the window, blocks stating no
// total, a stream that opens in it past the first, a block with samples behind the window's leading
// metadata blocks, or a first block of no samples.
func TestBlockChainEnd(t *testing.T) {
	long := wvStream(5000)
	tail := wvBlock(500000, 0, 500000, make([]byte, 24)) // a metadata block after the audio
	var groups []byte
	for i := range 2000 {
		groups = append(groups, wvGroup(uint64(i)*100, 100, 200000)...)
	}
	restart := slices.Concat(wvStream(3000), wvStream(200)) // a second stream joined in the window
	var past []byte                                         // a group past the total after the one reaching it
	for i := range 3001 {
		past = append(past, wvBlock(uint64(i)*100, 100, 300000, make([]byte, 64))...)
	}
	cut := wvStream(3000)
	binary.LittleEndian.PutUint32(cut[12:16], 300100) // states a group more than it holds
	broken := wvStream(3000)
	broken = slices.Concat(broken[:len(broken)-960], make([]byte, 50), broken[len(broken)-960:])
	versioned := wvStream(3000)
	binary.LittleEndian.PutUint16(versioned[len(versioned)-96+8:], maxVersion-1)
	var big []byte // blocks longer than the window, then a short stream of the same total behind junk
	for i := range 3 {
		big = append(big, wvBlock(uint64(i)*100, 100, 300, make([]byte, 70000))...)
	}
	opens := slices.Concat(big, make([]byte, 100), wvStream(3))
	// A second stream opening on the window's first header, stating another total there and none after.
	second := slices.Concat(big, wvBlock(0, 100, 350, make([]byte, 64)), wvBlock(100, 100, 0, make([]byte, 64)),
		wvBlock(200, 150, 0, make([]byte, 64)))
	var two []byte // then another stream's block on the window's first header, and one of the first's
	for i := range 2 {
		two = append(two, wvBlock(uint64(i)*100, 100, 300, make([]byte, 70000))...)
	}
	stray := slices.Concat(two, wvBlock(0, 100, 350, make([]byte, 64)), wvBlock(200, 100, 300, make([]byte, 64)))
	md5 := wvBlock(300000, 0, 300000, make([]byte, 24))
	behind := slices.Concat(wvStream(3000), md5, wvStream(3000)[2318*96:]) // a join behind the metadata block
	empty := wvBlock(0, 0, 300050, nil)                                    // its samples start at 50, past the empty first block
	for i := range 3000 {
		empty = append(empty, wvBlock(50+uint64(i)*100, 100, 300050, make([]byte, 64))...)
	}
	unstated := wvStream(3000) // as ffmpeg writes it: the total stated by the first block alone
	for off := 96; off < len(unstated); off += 96 {
		unstated[off+11] = 0
		binary.LittleEndian.PutUint32(unstated[off+12:], 0)
	}
	for _, c := range []struct {
		name   string
		data   []byte
		end    int64
		walked bool // the end needs a walk of the blocks
	}{
		{"a long stream", long, int64(len(long)), false},
		{"a metadata block after the audio", slices.Concat(long, tail), int64(len(long) + len(tail)), false},
		{"stereo groups of two blocks", groups, int64(len(groups)), false},
		{"junk after the stream", slices.Concat(long, make([]byte, 100)), int64(len(long)), true},
		{"a stream joined in the window", restart, int64(len(restart) - 200*96), true},
		{"a group past the total", past, int64(len(past) - 96), true},
		{"a stream short of its total", cut, int64(len(cut)), true},
		{"a break in the window", broken, int64(len(broken) - 50 - 960), true},
		{"a block of another version in the window", versioned, int64(len(versioned)), true},
		{"a stream of nearly the same length joined behind the window", slices.Concat(wvStream(3000), wvRun(3000, 150)), 3000 * 96, true},
		{"a stream that opens in the window past the first", opens, int64(len(big)), true},
		{"a stream that opens on the window's first header", second, int64(len(big)), true},
		{"later blocks stating no total", unstated, int64(len(unstated)), true},
		{"another stream's block on the window's first header", stray, int64(len(two)), true},
		{"a block with samples behind the window's leading metadata block", behind, 3000*96 + int64(len(md5)), true},
		{"a first block of no samples", empty, int64(len(empty)), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := &reads{ReaderAtSized: core.BytesSource(c.data)}
			got, err := blockChainEnd(context.Background(), src, 0, int64(len(c.data)), 1<<20, 0)
			if err != nil || got.end != c.end || !got.audio {
				t.Fatalf("blockChainEnd = %+v, %v; want end %d with audio", got, err, c.end)
			}
			if walked := src.n > blockHeaderLen+tailWindow; walked != c.walked {
				t.Errorf("read %d bytes; want a walk of the blocks %v", src.n, c.walked)
			}
			walk, err := walkBlocks(context.Background(), core.BytesSource(c.data), 0, int64(len(c.data)), 1<<20, 0)
			if err != nil || walk != got {
				t.Errorf("the walk finds %+v, %v; want it to agree with %+v", walk, err, got)
			}
		})
	}
}

// Every fixture's stream ends where a walk of its blocks ends it, the tags at the end of the file
// peeled. The ffmpeg-written fixtures state their total in the first block only and are walked;
// sine-s16.wv, from the reference encoder, states it in every block and is settled from the tail.
func TestBlockChainEndFixtures(t *testing.T) {
	paths, err := filepath.Glob("../../testdata/*.wv")
	if err != nil || len(paths) == 0 {
		t.Fatalf("fixtures %v, %v", paths, err)
	}
	for _, path := range append(paths, "testdata/sine-s16.wv") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if settled := agreesWithWalk(t, path); settled != (filepath.Base(path) == "sine-s16.wv") {
				t.Errorf("settled from the tail %v, want it only for the reference encoder's file", settled)
			}
		})
	}
}

// agreesWithWalk checks that blockChainEnd and a walk of the blocks end the stream of the file at
// path alike, before the tags its tail scan peels, and reports whether the tail settled it.
func agreesWithWalk(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := core.BytesSource(data)
	boundary := int64(-1)
	if _, err := tail.Scan(src, src.Size(), blockHeaderLen, 1<<30, 0, func(_ core.ReaderAtSized, b int64) (int64, error) {
		boundary = b
		return -1, nil
	}); err != nil || boundary < 0 {
		t.Fatalf("boundary %d, %v", boundary, err)
	}
	got, err := blockChainEnd(context.Background(), src, 0, boundary, 1<<30, 0)
	walk, werr := walkBlocks(context.Background(), src, 0, boundary, 1<<30, 0)
	if err != nil || werr != nil || got != walk || got.end < 0 {
		t.Errorf("end %+v, %v; the walk's %+v, %v", got, err, walk, werr)
	}
	_, settled, err := finalBlock(src, 0, boundary, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	return settled
}

// The window is read only within the allocation limit; under a smaller one the blocks are walked.
func TestBlockChainEndUnderASmallLimit(t *testing.T) {
	stream := wvStream(5000)
	if got, err := blockChainEnd(context.Background(), core.BytesSource(stream), 0, int64(len(stream)), 16<<10, 0); err != nil || got.end != int64(len(stream)) {
		t.Errorf("blockChainEnd = %+v, %v; want end %d", got, err, len(stream))
	}
}

// Two copies of a stream longer than the window, back to back, read as one: the window shows only
// the second's last blocks, stating the first's total, so nothing there marks the join a walk sees.
// A break before the window's first block header goes unseen alike.
func TestBlockChainEndTakesTwoLongCopiesForOne(t *testing.T) {
	one := wvStream(3000)
	two := slices.Concat(one, one)
	got, err := blockChainEnd(context.Background(), core.BytesSource(two), 0, int64(len(two)), 1<<20, 0)
	walk, werr := walkBlocks(context.Background(), core.BytesSource(two), 0, int64(len(two)), 1<<20, 0)
	if err != nil || werr != nil || got.end != int64(len(two)) || walk.end != int64(len(one)) {
		t.Errorf("end %+v, %v, the walk's %+v, %v; want %d, and %d walked", got, err, walk, werr, len(two), len(one))
	}
}

// A break just before the window's first block header, after the block the window opens inside,
// goes unseen there: the stream ends at the tags, where a walk ends it at the break.
func TestBlockChainEndMissesABreakBeforeTheWindowsFirstHeader(t *testing.T) {
	s := wvStream(3000)
	at := 2318 * 96 // the end of the block the window opens inside
	broken := slices.Concat(s[:at], make([]byte, 50), s[at:])
	if lo := len(broken) - tailWindow; lo <= at-96 || lo >= at {
		t.Fatalf("setup: the window opens at %d, not inside the block before %d", lo, at)
	}
	got, err := blockChainEnd(context.Background(), core.BytesSource(broken), 0, int64(len(broken)), 1<<20, 0)
	walk, werr := walkBlocks(context.Background(), core.BytesSource(broken), 0, int64(len(broken)), 1<<20, 0)
	if err != nil || werr != nil || got.end != int64(len(broken)) || walk.end != int64(at) {
		t.Errorf("end %+v, %v, the walk's %+v, %v; want %d, and %d walked", got, err, walk, werr, len(broken), at)
	}
}

// A join on the window's first block header goes unseen too: a copy of a stream's last block
// appended, when that copy is the window's first header, reads as the stream's end.
func TestBlockChainEndTakesAJoinOnTheWindowsFirstHeader(t *testing.T) {
	var s []byte
	for i := range 3 {
		s = append(s, wvBlock(uint64(i)*100, 100, 300, make([]byte, 40000))...)
	}
	last := s[2*40032:]
	joined := slices.Concat(s, last)
	if lo := len(joined) - tailWindow; lo <= 2*40032 || lo >= len(s) {
		t.Fatalf("setup: the window opens at %d, not inside the last block", lo)
	}
	got, err := blockChainEnd(context.Background(), core.BytesSource(joined), 0, int64(len(joined)), 1<<20, 0)
	walk, werr := walkBlocks(context.Background(), core.BytesSource(joined), 0, int64(len(joined)), 1<<20, 0)
	if err != nil || werr != nil || got.end != int64(len(joined)) || walk.end != int64(len(s)) {
		t.Errorf("end %+v, %v, the walk's %+v, %v; want %d, and %d walked", got, err, walk, werr, len(joined), len(s))
	}
}
