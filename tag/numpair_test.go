package tag

import "testing"

// TestSplitNumberTotal pins the substring-preserving split shared by the ID3 read
// path and the edit-time pair normalization - distinct from ParseNumPair, which
// renumbers to ints and so drops leading zeros.
func TestSplitNumberTotal(t *testing.T) {
	for _, c := range []struct{ in, num, total string }{
		{"3/12", "3", "12"},
		{"03/09", "03", "09"}, // leading zeros preserved (ParseNumPair would collapse to 3/9)
		{"3/", "3", ""},
		{"/12", "", "12"},
		{"3", "3", ""},
		{"", "", ""},
		{" 3 / 12 ", "3", "12"}, // each side trimmed
		{"1/2/3", "1", "2/3"},   // split on the first '/' only
	} {
		if num, total := SplitNumberTotal(c.in); num != c.num || total != c.total {
			t.Errorf("SplitNumberTotal(%q) = %q,%q, want %q,%q", c.in, num, total, c.num, c.total)
		}
	}
}

// TestTotalKey pins the number->total companion mapping shared by the codecs and the
// edit-time split; a non-numbering key is returned unchanged.
func TestTotalKey(t *testing.T) {
	for _, c := range []struct{ in, want Key }{
		{TrackNumber, TrackTotal},
		{DiscNumber, DiscTotal},
		{Artist, Artist},
	} {
		if got := TotalKey(c.in); got != c.want {
			t.Errorf("TotalKey(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}
