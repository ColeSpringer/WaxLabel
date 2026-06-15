package bits

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/colespringer/waxlabel/waxerr"
)

func TestDepthGuard(t *testing.T) {
	d := NewDepth(3)
	for i := 0; i < 3; i++ {
		if err := d.Enter(); err != nil {
			t.Fatalf("level %d: unexpected err %v", i, err)
		}
	}
	if err := d.Enter(); !errors.Is(err, waxerr.ErrTooDeep) {
		t.Errorf("4th Enter err = %v, want ErrTooDeep", err)
	}
	d.Leave()
	if err := d.Enter(); err != nil {
		t.Errorf("after Leave, Enter err = %v, want nil", err)
	}
}

func TestReadSliceBounds(t *testing.T) {
	r := bytes.NewReader([]byte("0123456789"))

	got, err := ReadSlice(r, 2, 3, 1<<20)
	if err != nil || string(got) != "234" {
		t.Errorf("ReadSlice = %q, %v", got, err)
	}

	if _, err := ReadSlice(r, 0, 5, 4); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("over-limit err = %v, want ErrSizeTooLarge", err)
	}
	if _, err := ReadSlice(r, 8, 5, 1<<20); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("past-EOF err = %v, want ErrInvalidData", err)
	}
}

func TestHasherTapMatchesDirectHash(t *testing.T) {
	src := bytes.NewReader([]byte("AAAABBBBCCCCDDDD"))
	cfg := []byte("config")

	// Hash config + the [4,12) region directly.
	want := sha256.New()
	want.Write(cfg)
	want.Write([]byte("BBBBCCCC"))

	h := NewHasher([][2]int64{{4, 12}})
	h.Mix(cfg)
	segs := []Segment{Copy(0, 16)}
	if _, err := Write(context.Background(), discardWriter{}, src, segs, h); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(h.Sum(), want.Sum(nil)) {
		t.Error("tapped hash does not match the direct hash of the claimed region")
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestWriteCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := bytes.NewReader(make([]byte, 1<<18)) // larger than one chunk
	_, err := Write(ctx, discardWriter{}, src, []Segment{Copy(0, 1<<18)}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Write with cancelled ctx err = %v, want context.Canceled", err)
	}
}

func TestSHA256Concatenation(t *testing.T) {
	a := SHA256([]byte("foo"), []byte("bar"))
	b := SHA256([]byte("foobar"))
	if a != b {
		t.Error("SHA256 over chunks should equal SHA256 of the concatenation")
	}
}
