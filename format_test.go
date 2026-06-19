package waxlabel_test

import (
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// TestHumanBytes confirms the exported formatter is wired and promotes at a unit
// boundary; the exhaustive table lives in internal/bits.
func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1048575, "1.0 MiB"}, // 1 MiB - 1 byte promotes rather than reading "1024.0 KiB"
	}
	for _, c := range cases {
		if got := wl.HumanBytes(c.n); got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
