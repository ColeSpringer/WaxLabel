package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestOpenSourceMaxSourceBytesBoundary pins the exact ingest boundary: a stream of
// exactly the limit still parses (the read buffers limit+1 and compares, so len == limit
// passes), while one byte past the limit fails with ErrSizeTooLarge rather than being
// silently truncated and misparsed. The input is a fixed valid FLAC and the limit is
// varied around its size, so all three boundary positions are exercised with one fixture.
func TestOpenSourceMaxSourceBytesBoundary(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	size := int64(len(src))
	ctx := context.Background()

	cases := []struct {
		name    string
		limit   int64
		wantErr bool
	}{
		{"stream under limit", size + 1, false},
		{"stream at limit", size, false},
		{"stream over limit", size - 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := wl.OpenSource(ctx, bytes.NewReader(src), wl.WithMaxSourceBytes(tc.limit))
			if tc.wantErr {
				if !errors.Is(err, waxerr.ErrSizeTooLarge) {
					t.Fatalf("OpenSource(limit=%d) err = %v, want ErrSizeTooLarge", tc.limit, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenSource(limit=%d) unexpected err: %v", tc.limit, err)
			}
			defer s.Close()
			if got := s.Document().Fields().Title; got != "Original Title" {
				t.Errorf("Title = %q, want Original Title", got)
			}
		})
	}
}

// TestOpenSourceMaxSourceBytesUnlimited: a non-positive limit disables the cap, so a
// stream larger than a small positive limit would reject still parses. This is the
// documented WithMaxSourceBytes(0) escape hatch.
func TestOpenSourceMaxSourceBytesUnlimited(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	s, err := wl.OpenSource(context.Background(), bytes.NewReader(src), wl.WithMaxSourceBytes(0))
	if err != nil {
		t.Fatalf("WithMaxSourceBytes(0) should disable the cap, got: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestOpenSourceMaxSourceBytesCeiling: a limit at the int64 ceiling must behave as
// unbounded, not overflow the limit+1 probe to a negative that io.LimitReader reads as
// "nothing" and so misparse every input as an empty (unidentifiable) file.
func TestOpenSourceMaxSourceBytesCeiling(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	s, err := wl.OpenSource(context.Background(), bytes.NewReader(src), wl.WithMaxSourceBytes(math.MaxInt64))
	if err != nil {
		t.Fatalf("WithMaxSourceBytes(MaxInt64) should parse normally, got: %v", err)
	}
	defer s.Close()
	if got := s.Document().Fields().Title; got != "Original Title" {
		t.Errorf("Title = %q, want Original Title (ceiling limit must not misparse to empty)", got)
	}
}
