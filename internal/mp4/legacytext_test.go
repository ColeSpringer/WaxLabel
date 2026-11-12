package mp4

import "testing"

func TestTitleReader(t *testing.T) {
	for _, c := range []struct {
		page   *codePage
		raw    string
		want   string
		legacy bool
	}{
		{&macRoman, "", "", false},
		{&macRoman, "Caf\u00e9", "Caf\u00e9", false},
		{&macRoman, "Caf\x8e", "Caf\u00e9", true},
		{&macRoman, "\x80\x9f\xca\xd5\xdb\xf0\xff", "\u00c4\u00fc\u00a0\u2019\u20ac\uf8ff\u02c7", true},
		{&windows1252, "Caf\xe9", "Caf\u00e9", true},
		{&windows1252, "\x80\x81\x92\x9f\xa0\xff", "\u20ac\ufffd\u2019\u0178\u00a0\u00ff", true},
		// A title ends at its first NUL.
		{&macRoman, "Intro\x00\x00", "Intro", false},
		{&macRoman, "a\x8e\x00b", "a\u00e9", true},
		// UTF-16 behind a byte-order mark, in either order, ends at a NUL code unit. A lone last
		// byte is no code unit and half a surrogate pair reads as U+FFFD.
		{&macRoman, "\xfe\xff\x00I\x00n", "In", false},
		{&windows1252, "\xff\xfeI\x00n\x00", "In", false},
		{&macRoman, "\xfe\xff\x00C\x00a\x00f\x00\xe9", "Caf\u00e9", false},
		{&macRoman, "\xff\xfe\xe5\x65\x2c\x67", "\u65e5\u672c", false},
		{&macRoman, "\xfe\xff\xd8\x3d\xde\x00", "\U0001f600", false},
		{&macRoman, "\xfe\xff\x00A\x00\x00\x00B", "A", false},
		{&macRoman, "\xfe\xff\x00A\x00", "A", false},
		{&macRoman, "\xfe\xff\xd8\x3d\x00A", "\ufffdA", false},
		{&macRoman, "\xfe\xff", "", false},
	} {
		r := titleReader{page: c.page}
		if got := r.read([]byte(c.raw)); got != c.want || r.legacy != c.legacy {
			t.Errorf("%s: read(%q) = %q, legacy %v; want %q, %v", c.page.name, c.raw, got, r.legacy, c.want, c.legacy)
		}
	}
}
