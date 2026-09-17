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
