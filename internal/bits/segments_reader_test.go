package bits

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"

	"github.com/colespringer/waxlabel/waxerr"
)

// TestSegmentsReaderMatchesWrite: every read returns the bytes Write would emit at that offset,
// across literal, copy and empty segments, with io.EOF only where the output ends.
func TestSegmentsReaderMatchesWrite(t *testing.T) {
	src := bytes.NewReader([]byte("0123456789"))
	segs := []Segment{
		Lit([]byte("HEAD")),
		Copy(2, 3),
		Lit([]byte{}),
		Copy(5, 0),
		Lit([]byte("--")),
		Copy(8, 2),
		Copy(0, 1),
	}
	var out bytes.Buffer
	if _, err := Write(context.Background(), &out, src, segs, nil); err != nil {
		t.Fatal(err)
	}
	want := out.Bytes()
	r := NewSegmentsReader(src, segs)
	if r.Size() != OutputLen(segs) {
		t.Fatalf("Size = %d, want %d", r.Size(), OutputLen(segs))
	}
	for off := range len(want) + 1 {
		for n := range len(want) - off + 2 {
			p := make([]byte, n)
			k, err := r.ReadAt(p, int64(off))
			end := min(off+n, len(want))
			if k != end-off || !bytes.Equal(p[:k], want[off:end]) {
				t.Fatalf("ReadAt(%d bytes at %d) = %q, want %q", n, off, p[:k], want[off:end])
			}
			var wantErr error
			if off+n > len(want) {
				wantErr = io.EOF
			}
			if err != wantErr {
				t.Fatalf("ReadAt(%d bytes at %d) err = %v, want %v", n, off, err, wantErr)
			}
		}
	}
}

// TestSegmentsReaderReadErrors: a source shorter than a copy segment is invalid data as in Write,
// a failing source read keeps its cause, and a negative length or offset fails.
func TestSegmentsReaderReadErrors(t *testing.T) {
	p := make([]byte, 8)
	if _, err := NewSegmentsReader(endsAt{4}, []Segment{Copy(0, 8)}).ReadAt(p, 0); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("short source: err = %v, want ErrInvalidData", err)
	}
	_, err := NewSegmentsReader(eioReader{}, []Segment{Copy(0, 8)}).ReadAt(p, 0)
	if _, ok := errors.AsType[*fs.PathError](err); !ok || errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("EIO read: err = %v, want the *fs.PathError and not ErrInvalidData", err)
	}
	if k, err := NewSegmentsReader(eofAtEnd{8}, []Segment{Copy(0, 8)}).ReadAt(p, 0); err != nil || k != 8 {
		t.Errorf("full read with EOF: (%d, %v), want (8, nil)", k, err)
	}
	if _, err := NewSegmentsReader(endsAt{8}, []Segment{Copy(0, -1)}).ReadAt(p, 0); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("negative copy length: err = %v, want ErrInvalidData", err)
	}
	if _, err := NewSegmentsReader(endsAt{8}, []Segment{Copy(0, 8)}).ReadAt(p, -1); err == nil {
		t.Error("negative offset: err = nil, want an error")
	}
}
