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
// each: a 4CC id and a zero size, no value, no pad). It is the cheap amplification
// probe for the element cap - a flood of empty items that a missing cap would
// still append one struct per header.
func wavWithNZeroInfo(n int) []byte {
	items := make([][2]string, n)
	for i := range items {
		items[i] = [2]string{"IART", ""}
	}
	return wavWithInfo(items...)
}

// TestInfoElementCapRejectsFlood is the release-gate regression: a LIST/INFO of
// MaxElements+1 items must fail with ErrSizeTooLarge rather than allocate one item
// per 8-byte header, so WithLimits actually bounds a crafted LIST. A tiny cap keeps
// it cheap and precise.
func TestInfoElementCapRejectsFlood(t *testing.T) {
	opts := core.DefaultParseOptions()
	opts.Limits.MaxElements = 100
	src := wavWithNZeroInfo(101)
	if _, err := parse(context.Background(), core.BytesSource(src), opts); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("parse of 101 INFO items under MaxElements=100: err = %v, want ErrSizeTooLarge", err)
	}
}

// TestInfoElementCapDefaultLimit confirms the default 100000-item cap is enforced
// too (not only an explicit WithLimits). The body is sized to just exceed the
// default (~0.8 MB), not the report's 24 MB in-process reproduction.
func TestInfoElementCapDefaultLimit(t *testing.T) {
	src := wavWithNZeroInfo(bits.DefaultLimits.MaxElements + 1)
	if _, err := parse(context.Background(), core.BytesSource(src), core.DefaultParseOptions()); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("parse of DefaultLimits.MaxElements+1 INFO items: err = %v, want ErrSizeTooLarge", err)
	}
}

// TestInfoTruncatedListStillParses guards the preserved tolerance: a LIST/INFO
// whose trailing item declares more bytes than are present stops at that item and
// keeps the well-formed ones, with no error. Only a genuine cap breach is fatal.
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
