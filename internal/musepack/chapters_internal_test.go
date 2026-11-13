package musepack

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"syscall"
	"testing"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/waxerr"
)

// The packet walk reads packet headers, and the seek-table pointer an SO packet holds, only inside
// the file, so a read there that fails is an error, not an unknown end or no chapters; a header past
// the end of the file, where an end beyond it puts the walk, leaves the end unknown with no error.
func TestPacketWalkFailedRead(t *testing.T) {
	head := len(testfile.MPCStream(44100, 0, 2))
	audio := testfile.MPCAudio(64)
	stream := testfile.MPCStream(44100, 0, 2, audio, audio, testfile.MPCTitled(0, "x"), testfile.MPCEnd())
	so := testfile.MPCPacket("SO", testfile.MPCVarlen(4+uint64(len(audio))))
	seek := testfile.MPCStream(44100, 0, 2, so, audio, testfile.MPCPacket("ST", []byte{0, 0}), testfile.MPCEnd())
	ctx := context.Background()
	reaching := func(at int64) func(off, n int64) bool { return func(off, n int64) bool { return off+n > at } }
	startingAt := func(at int64) func(off, n int64) bool { return func(off, _ int64) bool { return off == at } }
	for _, c := range []struct {
		name  string
		data  []byte
		fails func(off, n int64) bool
	}{
		{"a packet header", stream, reaching(int64(head + len(audio) + 1))},
		{"the seek-table pointer", seek, startingAt(int64(head + 3))},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := &testfile.Faulty{Src: core.BytesSource(c.data), Fails: c.fails}
			if w, err := walkPackets(ctx, src, 0, int64(len(c.data)), 1<<20, 0); !errors.Is(err, syscall.EIO) || errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("walk: %+v, %v; want the read's own failure", w, err)
			}
		})
	}
	if w, err := walkPackets(ctx, core.BytesSource(stream[:head+len(audio)+2]), 0, int64(len(stream)), 1<<20, 0); w.end != -1 || w.chapters != -1 || err != nil {
		t.Errorf("a header past the end of the file: %+v, %v; want an unknown end, no chapters and no error", w, err)
	}
	if w, err := walkPackets(ctx, core.BytesSource(stream), 0, int64(len(stream)), 1<<20, 0); w.end != int64(len(stream)) ||
		w.chapters != int64(head+2*len(audio)) || err != nil {
		t.Errorf("the whole stream: %+v, %v; want its end and its chapter run", w, err)
	}
}

// reads counts the reads a source serves.
type reads struct {
	core.ReaderAtSized
	n int
}

func (r *reads) ReadAt(p []byte, off int64) (int, error) {
	r.n++
	return r.ReaderAtSized.ReadAt(p, off)
}

// One walk of the packet headers finds where a stream with no SO packet ends and where its chapters
// start, so a parse reads each header once.
func TestPacketHeadersReadOnce(t *testing.T) {
	packets := make([][]byte, 0, 1002)
	for range 1000 {
		packets = append(packets, testfile.MPCAudio(64))
	}
	packets = append(packets, testfile.MPCTitled(0, "x"), testfile.MPCEnd())
	src := &reads{ReaderAtSized: core.BytesSource(testfile.MPCStream(44100, 0, 2, packets...))}
	m, err := parse(context.Background(), src, core.ParseOptions{Limits: bits.DefaultLimits})
	if err != nil || len(m.Chapters) != 1 {
		t.Fatalf("chapters %+v, err %v; want the one", m, err)
	}
	if src.n > 1050 {
		t.Errorf("%d reads for 1002 packets, want each header read once", src.n)
	}
}

// The chapter packets lie inside the file, so a read of a chapter's header or payload that fails is
// an I/O fault the read returns, not a chapter reported malformed; a payload past the end of the file
// is a truncated chapter, reported.
func TestReadChaptersFailedRead(t *testing.T) {
	ct := testfile.MPCTitled(0, "x")
	stream := testfile.MPCStream(44100, 0, 2, ct, testfile.MPCEnd())
	at := int64(len(testfile.MPCStream(44100, 0, 2)))
	for _, c := range []struct {
		name  string
		fails func(off, n int64) bool
	}{
		{"a chapter's header", func(off, _ int64) bool { return off == at }},
		{"a chapter's payload", func(off, _ int64) bool { return off == at+3 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, err := readChapters(&testfile.Faulty{Src: core.BytesSource(stream), Fails: c.fails}, at, int64(len(stream)), 44100, 1<<20, 0)
			if !errors.Is(err, syscall.EIO) {
				t.Errorf("err = %v, want the read's own failure", err)
			}
		})
	}
	// The file ends right after the 11 bytes the header read takes.
	chs, _, ws, err := readChapters(core.BytesSource(stream[:at+11]), at, int64(len(stream)), 44100, 1<<20, 0)
	if err != nil || len(chs) != 0 || len(ws) != 1 || ws[0].Code != core.WarnMalformedTagEntry {
		t.Errorf("a payload past the end of the file: chapters %+v, warnings %v, err %v; want the truncated report", chs, ws, err)
	}
}

// The packet walk runs on to the tags at the end of the file, past a tag found before them, and a
// packet cap it meets inside that tag's bytes, past the stream, gives no warning of chapters going
// unread.
func TestPacketCapPastTheStreamIsNoWarning(t *testing.T) {
	tag, err := ape.Render([]ape.Item{{Key: "Title", Value: "x"}}, 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	stream := testfile.MPCStream(44100, 0, 2, testfile.MPCAudio(64), testfile.MPCAudio(64))
	data := slices.Concat(stream, tag, bytes.Repeat([]byte{1}, 100))
	walked, err := walkPackets(context.Background(), core.BytesSource(data), 0, int64(len(data)), 1<<20, 4)
	if err != nil || walked.capAt < int64(len(stream)) || walked.capAt >= int64(len(stream)+len(tag)) {
		t.Fatalf("walk %+v, %v; want the cap met inside the tag", walked, err)
	}
	limits := bits.DefaultLimits
	limits.MaxElements = 4
	m, err := parse(context.Background(), core.BytesSource(data), core.ParseOptions{Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range m.Warnings {
		if w.Code == core.WarnElementCap {
			t.Errorf("warning %v, want no packet cap for a three-packet stream", w)
		}
	}
}
