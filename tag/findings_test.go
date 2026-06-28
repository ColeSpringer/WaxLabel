package tag

import "testing"

// TestParseKeyTrimsWhitespace pins L1: ParseKey ignores surrounding whitespace
// (so a CLI "KEY = VALUE" split yields the bare key) while preserving interior
// spaces, and an all-whitespace input is still the empty-key error.
func TestParseKeyTrimsWhitespace(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		want Key
	}{
		{"  TITLE  ", Title},
		{"\tartist\n", Artist},
		{"  with space  ", Key("WITH SPACE")}, // interior space preserved
	} {
		got, err := ParseKey(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseKey(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := ParseKey("   "); err == nil {
		t.Error("ParseKey(all-whitespace) should error (empty after trim)")
	}
}

// TestClosestKey pins U2: a near-miss key resolves to the intended canonical key,
// while an unrelated string draws no suggestion.
func TestClosestKey(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		want Key
	}{
		{"TITEL", Title},              // transposition, distance 2
		{"TRACK NUMBER", TrackNumber}, // an extra separator, distance 1
		{"ARTIS", Artist},             // one missing letter
		{"titel", Title},              // case-insensitive
		{"DISC", DiscNumber},          // alias, 6 edits from DISCNUMBER (would suggest ISRC)
		{"TRACK", TrackNumber},        // alias, past the distance cap
		{"album_artist", AlbumArtist}, // alias, case-insensitive
		{"DATE", RecordingDate},       // alias resolves to the canonical date key
	} {
		got, ok := ClosestKey(c.in)
		if !ok || got != c.want {
			t.Errorf("ClosestKey(%q) = %q, %v; want %q, true", c.in, got, ok, c.want)
		}
	}
	// A recognized alias resolves to its canonical key before the distance fallback.
	if got, _ := ClosestKey("DISC"); got == ISRC {
		t.Error("ClosestKey(DISC) = ISRC; expected the alias to win over the distance fallback")
	}
	for _, in := range []string{"", "ZZZZZZZZZZ", "X"} {
		if got, ok := ClosestKey(in); ok {
			t.Errorf("ClosestKey(%q) = %q, true; want no suggestion", in, got)
		}
	}
}

// TestBooleanValueHelpers pins V1: IsBooleanKey identifies the boolean keys and
// ValidBooleanValue accepts both polarities (case-insensitive, trimmed) while
// rejecting anything else; a non-boolean key is always reported valid.
func TestBooleanValueHelpers(t *testing.T) {
	t.Parallel()
	if !IsBooleanKey(Compilation) || IsBooleanKey(Title) {
		t.Error("IsBooleanKey should be true only for Compilation among these")
	}
	for _, v := range []string{"1", "0", "true", "FALSE", "Yes", " no "} {
		if !ValidBooleanValue(Compilation, v) {
			t.Errorf("ValidBooleanValue(Compilation, %q) = false, want true", v)
		}
	}
	if ValidBooleanValue(Compilation, "maybe") {
		t.Error(`ValidBooleanValue(Compilation, "maybe") = true, want false`)
	}
	if !ValidBooleanValue(Title, "anything") {
		t.Error("a non-boolean key should report any value valid")
	}
}

// TestNegativeNumericValue pins V2: a negative component is detected for the plain
// numeric keys and for either side of a "n/total" pair key, while a well-formed
// non-negative value (and a non-numeric key) is not flagged.
func TestNegativeNumericValue(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		k    Key
		v    string
		want bool
	}{
		{TrackNumber, "-3", true},
		{PlayCount, "-1", true},
		{TrackNumber, "-3/10", true}, // numerator negative
		{TrackNumber, "3/-10", true}, // total negative (mirrors ParseNumPair split)
		{TrackNumber, "3/10", false},
		{TrackNumber, "3", false},
		{TrackNumber, "abc", false}, // malformed, not negative (ValidNumericValue judges that)
		{Title, "-3", false},        // not a numeric key
	} {
		if got := NegativeNumericValue(c.k, c.v); got != c.want {
			t.Errorf("NegativeNumericValue(%s, %q) = %v, want %v", c.k, c.v, got, c.want)
		}
	}
}
