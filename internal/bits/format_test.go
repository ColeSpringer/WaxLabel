package bits

import "testing"

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{16777215, "16.0 MiB"},
		{1073741824, "1.0 GiB"},
		// Promote to next unit at boundary instead of "1024.0 KiB".
		{1048575, "1.0 MiB"},
		{1073741823, "1.0 GiB"},
		{1048524, "1023.9 KiB"},
	}
	for _, c := range cases {
		if got := HumanBytes(c.n); got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestSizeOverLimit: a size and the limit it exceeds print in units, and with their byte counts
// when the units would print the two alike.
func TestSizeOverLimit(t *testing.T) {
	const limit = 1<<24 - 1
	for _, c := range []struct {
		n, limit int64
		got, max string
	}{
		{20 << 20, limit, "20.0 MiB", "16.0 MiB"},
		{limit + 1, limit, "16.0 MiB (16777216 bytes)", "16.0 MiB (16777215 bytes)"},
		{limit + 43, limit, "16.0 MiB (16777258 bytes)", "16.0 MiB (16777215 bytes)"},
		{501, 500, "501 B", "500 B"},
	} {
		if got, max := SizeOverLimit(c.n, c.limit); got != c.got || max != c.max {
			t.Errorf("SizeOverLimit(%d, %d) = %q, %q; want %q, %q", c.n, c.limit, got, max, c.got, c.max)
		}
	}
}

func TestGroupThousands(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0", 7: "7", 500: "500", 1000: "1,000", 100000: "100,000", 1234567: "1,234,567", -1: "-1", -1000: "-1,000", -123456: "-123,456",
	} {
		if got := GroupThousands(n); got != want {
			t.Errorf("GroupThousands(%d) = %q, want %q", n, got, want)
		}
	}
}
