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
		// A title ends at its first NUL, and UTF-16 behind a byte-order mark reads empty.
		{&macRoman, "Intro\x00\x00", "Intro", false},
		{&macRoman, "a\x8e\x00b", "a\u00e9", true},
		{&macRoman, "\xfe\xff\x00I\x00n", "", false},
		{&windows1252, "\xff\xfeI\x00", "", false},
	} {
		r := titleReader{page: c.page}
		if got := r.read([]byte(c.raw)); got != c.want || r.legacy != c.legacy {
			t.Errorf("%s: read(%q) = %q, legacy %v; want %q, %v", c.page.name, c.raw, got, r.legacy, c.want, c.legacy)
		}
	}
}
