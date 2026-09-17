package main

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestParseByteSize: --max-size human-size parser (binary/decimal units, zero unlimited, rejects).
func TestParseByteSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"2GiB", 2 << 30, false},
		{"500MB", 500 * 1000 * 1000, false},
		{"2147483648", 2147483648, false},
		{"0", 0, false},
		{"1MiB", 1 << 20, false},
		{"500 MB", 500 * 1000 * 1000, false}, // an optional space is allowed before the unit
		{"4k", 4096, false},                  // a bare unit letter is binary
		{"4kB", 4000, false},                 // a trailing B without an i is decimal
		{"1.5KiB", 1536, false},              // a fractional value is allowed
		{"7TiB", 7 << 40, false},             // large but well within int64
		{"+5MB", 5 * 1000 * 1000, false},     // a redundant leading '+' is accepted
		{"", 0, true},
		{"abc", 0, true},
		{"12zz", 0, true},
		{"-5", 0, true},                  // a negative bare count is rejected
		{"-5MB", 0, true},                // a negative value with a unit is rejected
		{"9223372036854775808", 0, true}, // 2^63: int64(total) would wrap to a negative "unlimited"
		{"8388608TiB", 0, true},          // 8388608 * 2^40 = 2^63: same overflow via a unit
	}
	for _, tc := range cases {
		got, err := parseByteSize(tc.in)
		switch {
		case tc.wantErr && err == nil:
			t.Errorf("parseByteSize(%q) = %d, want error", tc.in, got)
		case !tc.wantErr && err != nil:
			t.Errorf("parseByteSize(%q) unexpected error: %v", tc.in, err)
		case !tc.wantErr && got != tc.want:
			t.Errorf("parseByteSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestMaxSizeStdinBoundary: at cap dumps; one byte under exits 7 (input-too-large); 0 disables cap.
func TestMaxSizeStdinBoundary(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	size := strconv.Itoa(len(data))
	under := strconv.Itoa(len(data) - 1)

	if _, errb, code := runCLIStdin(t, string(data), "dump", "--max-size", size, "-"); code != 0 {
		t.Errorf("dump - at the limit exit = %d, want 0; stderr=%q", code, errb)
	}

	_, errb, code := runCLIStdin(t, string(data), "dump", "--max-size", under, "-")
	if code != 7 {
		t.Errorf("dump - over the limit exit = %d, want 7; stderr=%q", code, errb)
	}
	if !strings.Contains(errb, "exceeds") {
		t.Errorf("over-limit stderr should explain the size cap, got %q", errb)
	}
	if strings.Contains(errb, "declared size") {
		t.Errorf("over-limit stderr must not use the corruption 'declared size' framing, got %q", errb)
	}

	if _, errb, code := runCLIStdin(t, string(data), "dump", "--max-size", "0", "-"); code != 0 {
		t.Errorf("dump - with --max-size 0 exit = %d, want 0; stderr=%q", code, errb)
	}
}

// endlessReader: unbounded stream with no EOF.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	return len(p), nil
}

// TestMaxSizeStopsEndlessStdin: bounded dump - on endless stdin exits 7 without hanging.
func TestMaxSizeStopsEndlessStdin(t *testing.T) {
	t.Parallel()
	type result struct {
		errb string
		code int
	}
	done := make(chan result, 1)
	go func() {
		var out, errb bytes.Buffer
		c := dispatch(context.Background(), []string{"dump", "--max-size", "1MB", "-"}, endlessReader{}, &out, &errb)
		done <- result{errb.String(), c}
	}()
	select {
	case r := <-done:
		if r.code != 7 {
			t.Errorf("endless stdin exit = %d, want 7 (bounded, input-too-large); stderr=%q", r.code, r.errb)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bounded dump - hung on an endless stream")
	}
}
