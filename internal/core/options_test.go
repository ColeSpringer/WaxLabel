package core

import "testing"

// ReuseOrTarget reuses a region's leftover bytes as padding, capped at Max as every padding policy
// is, and falls back to the clamped target when the content does not fit or leaves less than Min.
func TestReuseOrTargetHonorsMax(t *testing.T) {
	const kib, mib = 1 << 10, 1 << 20
	for _, c := range []struct {
		name          string
		pol           PaddingPolicy
		orig, content int64
		want          int64
	}{
		{"leftover over Max", DefaultPadding, 3 * mib, kib, mib},
		{"leftover under Max", DefaultPadding, 10 * kib, kib, 9 * kib},
		{"no cap", PaddingPolicy{Target: 8192, ReuseInPlace: true}, 3 * mib, kib, 3*mib - kib},
		{"content past the region", DefaultPadding, kib, 2 * kib, 8192},
		{"leftover under Min", PaddingPolicy{Target: 100, Min: 600, Max: mib, ReuseInPlace: true}, kib, 512, 600},
		{"no reuse", PaddingPolicy{Target: 100, Max: mib}, 3 * mib, kib, 100},
	} {
		if got := c.pol.ReuseOrTarget(c.orig, c.content); got != c.want {
			t.Errorf("%s: ReuseOrTarget(%d, %d) = %d, want %d", c.name, c.orig, c.content, got, c.want)
		}
	}
}
