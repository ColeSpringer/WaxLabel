package aac

import (
	"bytes"
	"context"
	"errors"
	"syscall"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
)

// readBytes counts the bytes a source serves.
type readBytes struct {
	core.ReaderAtSized
	n int64
}

func (r *readBytes) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.ReaderAtSized.ReadAt(p, off)
	r.n += int64(n)
	return n, err
}

// Frames that abstain from the SBR vote, as AAC Main frames do, still count toward the frames the
// probe examines, so a stream of them costs one window, however long it is.
func TestDetectSBRExaminesAtMostSixteenFrames(t *testing.T) {
	frame := make([]byte, 207)
	copy(frame, buildADTS(0, 4, 2, len(frame))) // profile 0: AAC Main, which the probe does not parse
	stream := bytes.Repeat(frame, 20000)
	h, _ := decodeADTS(stream)
	src := &readBytes{ReaderAtSized: core.BytesSource(stream)}
	sbr, ps, err := detectSBR(context.Background(), src, 0, int64(len(stream)), h, 1<<20)
	if err != nil || sbr || ps {
		t.Fatalf("detectSBR = %v, %v, %v; want no SBR, no PS, no error", sbr, ps, err)
	}
	if src.n > probeWindow {
		t.Errorf("the probe read %d bytes of a %d-byte stream, want at most one %d-byte window", src.n, len(stream), probeWindow)
	}
}

// The probe reads only inside the file, so a window it reads that fails is an I/O fault it returns,
// not an answer of no SBR; a window past the end of the file ends the probe with no error.
func TestDetectSBRFailedRead(t *testing.T) {
	frame := make([]byte, 207)
	copy(frame, buildADTS(1, 4, 2, len(frame)))
	stream := bytes.Repeat(frame, 200)
	h, _ := decodeADTS(stream)
	if _, _, err := detectSBR(context.Background(), &testfile.Faulty{Src: core.BytesSource(stream), Fails: testfile.From(100)}, 0, int64(len(stream)), h, 1<<20); !errors.Is(err, syscall.EIO) {
		t.Errorf("a failing window: err = %v, want the read's own failure", err)
	}
	if sbr, ps, err := detectSBR(context.Background(), core.BytesSource(stream[:500]), 0, int64(len(stream)), h, 1<<20); err != nil || sbr || ps {
		t.Errorf("a window past the end of the file: %v, %v, %v; want no SBR and no error", sbr, ps, err)
	}
}
