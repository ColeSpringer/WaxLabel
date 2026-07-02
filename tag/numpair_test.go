package tag

import (
	"slices"
	"testing"
)

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

// TestNumberTotalSplit pins the shared read-path split decision used by the id3, matroska, and
// vorbis/wav read paths, so all of them split a slashed value the same way. A well-formed pair
// splits (leading zeros and a literal 0 preserved); a malformed pair, a bare slash, a slashless
// value, or a non-pair key comes back whole with split=false, so the value stays verbatim on the
// number key exactly as the editor leaves it.
func TestNumberTotalSplit(t *testing.T) {
	for _, c := range []struct {
		key        Key
		in         string
		num, total string
		split      bool
	}{
		{TrackNumber, "4/9", "4", "9", true},
		{TrackNumber, "04/09", "04", "09", true}, // leading zeros preserved
		{TrackNumber, "0/12", "0", "12", true},   // literal 0 preserved
		{TrackNumber, "/2", "", "2", true},
		{TrackNumber, "3/", "3", "", true},
		{TrackNumber, " 4 / 9 ", "4", "9", true}, // each side trimmed
		{TrackNumber, "-3/10", "-3", "10", true}, // a signed side still round-trips through Atoi
		{TrackNumber, "abc/1", "abc/1", "", false},
		{TrackNumber, "1/2/3", "1/2/3", "", false},
		{TrackNumber, "/", "/", "", false}, // bare slash: malformed, kept verbatim
		{TrackNumber, "5", "5", "", false}, // no slash
		{TrackNumber, "", "", "", false},
		{DiscNumber, "1/2", "1", "2", true},
		{Artist, "4/9", "4/9", "", false}, // not a pair key: never split
	} {
		num, total, split := NumberTotalSplit(c.key, c.in)
		if num != c.num || total != c.total || split != c.split {
			t.Errorf("NumberTotalSplit(%s, %q) = %q,%q,%v want %q,%q,%v",
				c.key, c.in, num, total, split, c.num, c.total, c.split)
		}
	}
}

// TestNormalizeNumberPairs pins the FLAC/Ogg and WAV read-path post-pass: it splits a single
// slashed track/disc number into number + derived total (leading zeros and a literal 0 intact),
// leaves a malformed or slashless value verbatim, drops the number for a leading "/total", and
// never overwrites an already-present (including present-empty) or explicit total. A multi-valued
// key is left untouched so no value is lost.
func TestNormalizeNumberPairs(t *testing.T) {
	type slot struct {
		present bool
		vals    []string
	}
	present := func(ts TagSet, k Key) slot {
		v, ok := ts.Get(k)
		return slot{ok, v}
	}
	eq := func(a, b slot) bool { return a.present == b.present && slices.Equal(a.vals, b.vals) }

	for _, c := range []struct {
		name           string
		setup          func(*TagSet)
		numK, totK     Key
		wantNum, wantT slot
	}{
		{"well-formed", func(ts *TagSet) { ts.Set(TrackNumber, "4/9") }, TrackNumber, TrackTotal,
			slot{true, []string{"4"}}, slot{true, []string{"9"}}},
		{"leading zeros", func(ts *TagSet) { ts.Set(TrackNumber, "04/09") }, TrackNumber, TrackTotal,
			slot{true, []string{"04"}}, slot{true, []string{"09"}}},
		{"literal zero number", func(ts *TagSet) { ts.Set(TrackNumber, "0/12") }, TrackNumber, TrackTotal,
			slot{true, []string{"0"}}, slot{true, []string{"12"}}},
		{"empty number side", func(ts *TagSet) { ts.Set(TrackNumber, "/2") }, TrackNumber, TrackTotal,
			slot{false, nil}, slot{true, []string{"2"}}}, // number deleted, only the total survives
		{"empty total side", func(ts *TagSet) { ts.Set(TrackNumber, "3/") }, TrackNumber, TrackTotal,
			slot{true, []string{"3"}}, slot{false, nil}},
		{"malformed triple", func(ts *TagSet) { ts.Set(TrackNumber, "1/2/3") }, TrackNumber, TrackTotal,
			slot{true, []string{"1/2/3"}}, slot{false, nil}}, // kept verbatim, no total composed
		{"malformed non-numeric", func(ts *TagSet) { ts.Set(TrackNumber, "abc/1") }, TrackNumber, TrackTotal,
			slot{true, []string{"abc/1"}}, slot{false, nil}},
		{"no slash", func(ts *TagSet) { ts.Set(TrackNumber, "5") }, TrackNumber, TrackTotal,
			slot{true, []string{"5"}}, slot{false, nil}},
		{"explicit total wins", func(ts *TagSet) { ts.Set(TrackNumber, "4/9"); ts.Set(TrackTotal, "20") },
			TrackNumber, TrackTotal, slot{true, []string{"4"}}, slot{true, []string{"20"}}}, // slash total does not override
		{"present-empty total preserved", func(ts *TagSet) { ts.Set(TrackNumber, "4/9"); ts.Set(TrackTotal) },
			TrackNumber, TrackTotal, slot{true, []string{"4"}}, slot{true, nil}}, // present-but-empty kept
		{"multi-valued left alone", func(ts *TagSet) { ts.Set(TrackNumber, "4/9", "5") }, TrackNumber, TrackTotal,
			slot{true, []string{"4/9", "5"}}, slot{false, nil}}, // never collapse a multi-value
		{"disc pair", func(ts *TagSet) { ts.Set(DiscNumber, "1/2") }, DiscNumber, DiscTotal,
			slot{true, []string{"1"}}, slot{true, []string{"2"}}},
	} {
		var ts TagSet
		c.setup(&ts)
		NormalizeNumberPairs(&ts)
		if n := present(ts, c.numK); !eq(n, c.wantNum) {
			t.Errorf("%s: %s = %+v, want %+v", c.name, c.numK, n, c.wantNum)
		}
		if tt := present(ts, c.totK); !eq(tt, c.wantT) {
			t.Errorf("%s: %s = %+v, want %+v", c.name, c.totK, tt, c.wantT)
		}
	}
}
