package tag

import (
	"slices"
	"testing"
)

// TestParseNumPairOfConvention pins the worded pair: an integer on either side of "of", in
// any case and with or without spaces, reads as number and total, as "n/total" does. A value
// that only contains the word is no pair and reads 0.
func TestParseNumPairOfConvention(t *testing.T) {
	for _, c := range []struct {
		num, total string
		n, tot     int
	}{
		{"3 of 12", "", 3, 12},
		{"3 OF 12", "", 3, 12},
		{"3 Of 12", "", 3, 12},
		{"3of12", "", 3, 12},
		{" 03 of 012 ", "", 3, 12},
		{"3\tof\t12", "", 3, 12},
		{"3 of 12", "20", 3, 20}, // an explicit total wins
		{"3 of", "", 3, 0},
		{"of 12", "", 0, 12},
		{"of", "", 0, 0},
		{"1 of 2 of 3", "", 0, 0},
		{"Part 3 of 12", "", 0, 0},
		{"3 of twelve", "", 0, 0},
		{"3 off 12", "", 0, 0},
		{"3 of x", "", 0, 0},                    // the word separates only two integers
		{"3/x", "", 3, 0},                       // a slash cuts whatever is around it
		{"99999999999999999999 of 5", "", 0, 0}, // an overflowing side is no integer
	} {
		if n, tot := ParseNumPair(c.num, c.total); n != c.n || tot != c.tot {
			t.Errorf("ParseNumPair(%q, %q) = %d, %d, want %d, %d", c.num, c.total, n, tot, c.n, c.tot)
		}
	}
}

// TestProjectReadsOfPair: a number the read path left unsplit still projects as the pair.
func TestProjectReadsOfPair(t *testing.T) {
	var ts TagSet
	ts.Set(TrackNumber, "3 of 12")
	ts.Set(DiscNumber, "1 of 2")
	got := Project(ts)
	if got.TrackNumber != 3 || got.TrackTotal != 12 || got.DiscNumber != 1 || got.DiscTotal != 2 {
		t.Errorf("Project = track %d/%d disc %d/%d, want 3/12 and 1/2",
			got.TrackNumber, got.TrackTotal, got.DiscNumber, got.DiscTotal)
	}
}

// TestSplitNumberTotalOf: the split that takes no key knows the slash alone, so the words
// stay whole there. NumberTotalSplit reads them, under a key that holds a pair.
func TestSplitNumberTotalOf(t *testing.T) {
	for _, c := range []struct{ in, num, total string }{
		{"3 of 12", "3 of 12", ""},
		{"of 12", "of 12", ""},
		{"lots of beats", "lots of beats", ""},
		{"3 of 12/20", "3 of 12", "20"},
	} {
		if num, total := SplitNumberTotal(c.in); num != c.num || total != c.total {
			t.Errorf("SplitNumberTotal(%q) = %q,%q, want %q,%q", c.in, num, total, c.num, c.total)
		}
	}
}

// TestNumberTotalSplitOf pins the read-path split of the worded pair, gated to the track and
// disc numbers like the slashed one.
func TestNumberTotalSplitOf(t *testing.T) {
	for _, c := range []struct {
		key        Key
		in         string
		num, total string
		split      bool
	}{
		{TrackNumber, "3 of 12", "3", "12", true},
		{TrackNumber, "03 of 09", "03", "09", true},
		{TrackNumber, "3 OF 12", "3", "12", true},
		{TrackNumber, "3of12", "3", "12", true},
		{TrackNumber, "0 of 12", "0", "12", true},
		{TrackNumber, "-3 of 10", "-3", "10", true},
		{TrackNumber, "of 2", "", "2", true},
		{TrackNumber, "3 of", "3", "", true},
		{TrackNumber, "of", "of", "", false},
		{TrackNumber, "1 of 2 of 3", "1 of 2 of 3", "", false},
		{TrackNumber, "x of 3", "x of 3", "", false},
		{TrackNumber, "3 of 12/20", "3 of 12/20", "", false},
		{TrackNumber, "3/12 of 20", "3/12 of 20", "", false},
		{DiscNumber, "1 of 2", "1", "2", true},
		{TrackTotal, "3 of 12", "3 of 12", "", false},
		{Artist, "3 of 12", "3 of 12", "", false},
	} {
		num, total, split := NumberTotalSplit(c.key, c.in)
		if num != c.num || total != c.total || split != c.split {
			t.Errorf("NumberTotalSplit(%s, %q) = %q,%q,%v want %q,%q,%v",
				c.key, c.in, num, total, split, c.num, c.total, c.split)
		}
	}
}

// TestNormalizeNumberPairsOf: the read-path pass splits a worded pair and keeps an explicit total.
func TestNormalizeNumberPairsOf(t *testing.T) {
	var ts TagSet
	ts.Set(TrackNumber, "03 of 12")
	ts.Set(DiscNumber, "1 of 2")
	ts.Set(DiscTotal, "5")
	NormalizeNumberPairs(&ts)
	for _, c := range []struct {
		key  Key
		want string
	}{{TrackNumber, "03"}, {TrackTotal, "12"}, {DiscNumber, "1"}, {DiscTotal, "5"}} {
		if got, _ := ts.Get(c.key); !slices.Equal(got, []string{c.want}) {
			t.Errorf("%s = %v, want [%s]", c.key, got, c.want)
		}
	}
}

// TestValidNumericValueOf: the worded pair is valid exactly where the slashed one is.
func TestValidNumericValueOf(t *testing.T) {
	for _, c := range []struct {
		k    Key
		v    string
		want bool
	}{
		{TrackNumber, "3 of 12", true},
		{DiscNumber, "1 OF 2", true},
		{TrackNumber, "3of12", true},
		{TrackNumber, "3 of", true},
		{DiscNumber, "of 2", true},
		{TrackNumber, "of", false},
		{TrackNumber, "3 of x", false},
		{TrackNumber, "1 of 2 of 3", false},
		{TrackNumber, "3 of 4/5", false},
		{TrackNumber, "Part 3 of 12", false},
		{TrackTotal, "3 of 4", false},
		{PlayCount, "3 of 4", false},
	} {
		if got := ValidNumericValue(c.k, c.v); got != c.want {
			t.Errorf("ValidNumericValue(%s, %q) = %v, want %v", c.k, c.v, got, c.want)
		}
	}
}

func TestNegativeNumericValueOf(t *testing.T) {
	for _, c := range []struct {
		k    Key
		v    string
		want bool
	}{
		{TrackNumber, "-3 of 10", true},
		{TrackNumber, "3 of -10", true},
		{TrackNumber, "3 of 10", false},
		{TrackTotal, "3 of -10", false}, // a total takes no pair
	} {
		if got := NegativeNumericValue(c.k, c.v); got != c.want {
			t.Errorf("NegativeNumericValue(%s, %q) = %v, want %v", c.k, c.v, got, c.want)
		}
	}
}

func TestEmptyNumberWithTotalOf(t *testing.T) {
	for _, c := range []struct {
		k    Key
		v    string
		want bool
	}{
		{TrackNumber, "of 5", true},
		{DiscNumber, "OF 5", true},
		{TrackNumber, "3 of 5", false},
		{TrackNumber, "of", false},
		{TrackNumber, "of abc", false},
		{TrackTotal, "of 5", false},
	} {
		if got := EmptyNumberWithTotal(c.k, c.v); got != c.want {
			t.Errorf("EmptyNumberWithTotal(%s, %q) = %v, want %v", c.k, c.v, got, c.want)
		}
	}
}

// TestNumericValuesEqualOf: a worded pair equals the slashed pair of the same numbers. Text
// that is no pair compares verbatim, the word's case included.
func TestNumericValuesEqualOf(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"3 of 12", "3/12", true},
		{"03 OF 012", "3 of 12", true},
		{"3 of 12", "3 of 13", false},
		{"3 of 12", "3", false},
		{"lots of beats", "lots OF beats", false},
		{"of", "/", false},
	} {
		if got := NumericValuesEqual(TrackNumber, []string{c.a, "5"}, []string{c.b, "5"}); got != c.want {
			t.Errorf("NumericValuesEqual(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// FuzzNumberPair holds the pair helpers to each other on any text: what splits is valid and
// projects as its two halves, what does not split comes back whole and is valid only as a
// plain integer, and the halves joined by a slash split back the same, which the writers
// that compose "n/total" rely on.
func FuzzNumberPair(f *testing.F) {
	for _, s := range []string{
		"3/12", "3 of 12", "03 OF 09", "3of12", "of 2", "3 of", "of", "/", "1 of 2 of 3",
		"3 of 12/20", "3/12 of 20", "Part 3 of 12", "-3 of +10", " 4 / 9 ", "lots of beats",
		"99999999999999999999 of 5", "3" + string(rune(0xA0)) + "of 12", "7", "",
	} {
		f.Add(s)
	}
	atoi := func(s string) int { n, _ := parseIntField(s); return n }
	f.Fuzz(func(t *testing.T, v string) {
		for _, k := range []Key{TrackNumber, DiscNumber} {
			num, total, split := NumberTotalSplit(k, v)
			valid := ValidNumericValue(k, v)
			if !split {
				if num != v || total != "" {
					t.Fatalf("%q: unsplit gave %q, %q", v, num, total)
				}
				if valid != validInt(v) {
					t.Fatalf("%q: valid = %v without a pair, validInt = %v", v, valid, validInt(v))
				}
				continue
			}
			if !valid {
				t.Fatalf("%q splits but is not valid", v)
			}
			if num == "" && total == "" {
				t.Fatalf("%q splits into nothing", v)
			}
			if (num != "" && !validInt(num)) || (total != "" && !validInt(total)) {
				t.Fatalf("%q splits into %q, %q, not integers", v, num, total)
			}
			if n, tot := ParseNumPair(v, ""); n != atoi(num) || tot != atoi(total) {
				t.Fatalf("%q projects %d, %d; its halves are %q, %q", v, n, tot, num, total)
			}
			if n, tot, ok := NumberTotalSplit(k, num+"/"+total); !ok || n != num || tot != total {
				t.Fatalf("%q: %q/%q rejoined splits %q, %q, %v", v, num, total, n, tot, ok)
			}
		}
		if num, total, split := NumberTotalSplit(TrackTotal, v); split || num != v || total != "" {
			t.Fatalf("%q split on a key that takes no pair", v)
		}
	})
}

// TestNumericValuesEqualOfTakesNoPairElsewhere: only a track or disc number holds a worded
// pair, so under any other numeric key the words are text and compare verbatim. A slash
// still splits under any key.
func TestNumericValuesEqualOfTakesNoPairElsewhere(t *testing.T) {
	for _, c := range []struct {
		k    Key
		a, b string
		want bool
	}{
		{TrackTotal, "3 of 12", "3/12", false},
		{TrackTotal, "3 of 12", "3 OF 12", false},
		{TrackTotal, "3 of 12", "3 of 12", true},
		{BPM, "3 of 12", "3/12", false},
		{Movement, "3 of 12", "03 of 12", false},
		{PlayCount, "3 of 12", "3/12", false},
		{TrackTotal, "3/012", "3 / 12", true},
		{Movement, "03/12", "3/12", true},
		{DiscNumber, "1 of 2", "01/2", true},
	} {
		if got := NumericValuesEqual(c.k, []string{c.a}, []string{c.b}); got != c.want {
			t.Errorf("NumericValuesEqual(%s, %q, %q) = %v, want %v", c.k, c.a, c.b, got, c.want)
		}
	}
}
