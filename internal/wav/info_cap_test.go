package wav

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// wavWithNZeroInfo builds a WAV whose LIST/INFO holds n zero-length items (8 bytes
// each: a 4CC id and a zero size, no value, no pad), the cheapest flood for the
// element cap.
func wavWithNZeroInfo(n int) []byte {
	items := make([][2]string, n)
	for i := range items {
		items[i] = [2]string{"IART", ""}
	}
	return wavWithInfo(items...)
}

// TestInfoElementCapRejectsFlood checks that a LIST/INFO of MaxElements+1 items fails
// with ErrSizeTooLarge rather than allocating one item per 8-byte header. A tiny cap
// keeps it cheap.
func TestInfoElementCapRejectsFlood(t *testing.T) {
	opts := core.DefaultParseOptions()
	opts.Limits.MaxElements = 100
	src := wavWithNZeroInfo(101)
	if _, err := parse(context.Background(), core.BytesSource(src), opts); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("parse of 101 INFO items under MaxElements=100: err = %v, want ErrSizeTooLarge", err)
	}
}

// TestInfoElementCapDefaultLimit confirms the default 100000-item cap is enforced
// without an explicit WithLimits. The body just exceeds the default (~0.8 MB).
func TestInfoElementCapDefaultLimit(t *testing.T) {
	src := wavWithNZeroInfo(bits.DefaultLimits.MaxElements + 1)
	if _, err := parse(context.Background(), core.BytesSource(src), core.DefaultParseOptions()); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("parse of DefaultLimits.MaxElements+1 INFO items: err = %v, want ErrSizeTooLarge", err)
	}
}

// TestInfoTruncatedListStillParses checks that a LIST/INFO whose trailing item
// declares more bytes than are present stops at that item and keeps the well-formed
// ones, with no error. Only a cap breach is fatal.
func TestInfoTruncatedListStillParses(t *testing.T) {
	body := append([]byte("INFO"), infoItemBytes("INAM", "hello")...)
	body = append(body, "IART"...)
	body = append(body, le32(1000)...) // declares 1000 bytes with none present -> truncated
	chunks := slices.Concat(wavFmtChunk(), wavChunk("data", []byte{0, 0, 0, 0}), wavChunk("LIST", body))
	d := parseWAVDoc(t, riffWrap(chunks, nil, nil))
	if len(d.info) != 1 {
		t.Fatalf("truncated LIST/INFO: got %d items, want 1 (well-formed item kept, truncation tolerated)", len(d.info))
	}
}
