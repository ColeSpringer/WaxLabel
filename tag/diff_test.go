package tag

import (
	"slices"
	"testing"
)

// TestNumericValuesEqual pins the numeric-key equality: leading '+' and leading zeros do not make
// a numeric value different, including inside a slashed "n/total" pair, while a genuinely different
// number, a non-numeric token, or a mismatched count stays distinct. A non-numeric key falls back
// to exact slice equality.
func TestNumericValuesEqual(t *testing.T) {
	cases := []struct {
		name string
		key  Key
		a, b []string
		want bool
	}{
		{"leading zero equal", TrackNumber, []string{"03"}, []string{"3"}, true},
		{"sign equal", TrackNumber, []string{"+3"}, []string{"3"}, true},
		{"negative sign kept", TrackNumber, []string{"-03"}, []string{"-3"}, true},
		{"minus zero folds to zero", TrackNumber, []string{"-0"}, []string{"0"}, true},
		{"slashed pair equal", TrackNumber, []string{"3/012"}, []string{"3/12"}, true},
		// A value past the int range still canonicalizes its leading zero (string-level, no parse).
		{"huge leading zero equal", TrackNumber, []string{"0999999999999999999999"}, []string{"999999999999999999999"}, true},
		{"huge genuinely different", TrackNumber, []string{"999999999999999999998"}, []string{"999999999999999999999"}, false},
		{"different number", TrackNumber, []string{"3"}, []string{"4"}, false},
		{"negative vs positive", TrackNumber, []string{"-3"}, []string{"3"}, false},
		{"different total", TrackNumber, []string{"3/12"}, []string{"3/13"}, false},
		{"empty is not zero", TrackNumber, []string{""}, []string{"0"}, false},
		{"non-numeric token verbatim", TrackNumber, []string{"abc"}, []string{"abc"}, true},
		{"count mismatch", TrackNumber, []string{"3"}, []string{"3", "4"}, false},
		{"non-numeric key exact only", Title, []string{"03"}, []string{"3"}, false},
		{"non-numeric key equal", Title, []string{"x"}, []string{"x"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NumericValuesEqual(c.key, c.a, c.b); got != c.want {
				t.Errorf("NumericValuesEqual(%v, %v, %v) = %v, want %v", c.key, c.a, c.b, got, c.want)
			}
		})
	}
}

func TestDiff(t *testing.T) {
	var base, edited TagSet
	base.Set(Title, "Old")
	base.Set(Artist, "A")
	base.Set(Encoder, "Lavf") // removed in edited
	edited.Set(Title, "New")  // changed
	edited.Set(Artist, "A")   // unchanged: no Change
	edited.Set(Album, "Alb")  // added

	got := Diff(base, edited)

	// Removed/changed come first in base order, then added in edited order; an
	// unchanged key yields nothing.
	want := []Change{
		{Key: Title, Kind: ChangeChanged, Old: []string{"Old"}, New: []string{"New"}},
		{Key: Encoder, Kind: ChangeRemoved, Old: []string{"Lavf"}},
		{Key: Album, Kind: ChangeAdded, New: []string{"Alb"}},
	}
	if len(got) != len(want) {
		t.Fatalf("Diff() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Key != want[i].Key || got[i].Kind != want[i].Kind ||
			!slices.Equal(got[i].Old, want[i].Old) || !slices.Equal(got[i].New, want[i].New) {
			t.Errorf("Diff()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDiffNoOp(t *testing.T) {
	var base TagSet
	base.Set(Title, "X")
	base.Add(Artist, "A", "B")
	if got := Diff(base, base.Clone()); len(got) != 0 {
		t.Errorf("Diff of identical sets = %v, want none", got)
	}
}

// TestDiffMultiValueOrderSignificant: a reordered multi-value field is a change
// (the diff uses the same order-significant equality a codec uses to detect an
// edit).
func TestDiffMultiValueOrderSignificant(t *testing.T) {
	var base, edited TagSet
	base.Add(Artist, "A", "B")
	edited.Add(Artist, "B", "A")
	got := Diff(base, edited)
	if len(got) != 1 || got[0].Kind != ChangeChanged {
		t.Fatalf("Diff() = %v, want one changed", got)
	}
}

func TestChangeKindString(t *testing.T) {
	for kind, want := range map[ChangeKind]string{
		ChangeAdded: "added", ChangeRemoved: "removed", ChangeChanged: "changed",
	} {
		if got := kind.String(); got != want {
			t.Errorf("ChangeKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

// TestChangeZeroValue: the zero value is the explicit ChangeUnknown sentinel, so
// a never-set kind does not masquerade as a real one.
func TestChangeZeroValue(t *testing.T) {
	var c Change
	if c.Kind != ChangeUnknown {
		t.Errorf("zero Change.Kind = %v, want ChangeUnknown", c.Kind)
	}
	if got := ChangeUnknown.String(); got != "unknown" {
		t.Errorf("ChangeUnknown.String() = %q, want %q", got, "unknown")
	}
}

func TestChangeString(t *testing.T) {
	cases := []struct {
		c    Change
		want string
	}{
		{Change{Key: Title, Kind: ChangeAdded, New: []string{"New"}}, "+ TITLE: New"},
		{Change{Key: Encoder, Kind: ChangeRemoved, Old: []string{"Lavf"}}, "- ENCODER: Lavf"},
		{Change{Key: Title, Kind: ChangeChanged, Old: []string{"Old"}, New: []string{"New"}}, "~ TITLE: Old -> New"},
		// Multiple values join with " | "; an empty value list reads as "(present, no value)".
		{Change{Key: Artist, Kind: ChangeAdded, New: []string{"A", "B"}}, "+ ARTIST: A | B"},
		{Change{Key: Comment, Kind: ChangeAdded, New: nil}, "+ COMMENT: (present, no value)"},
		// Control bytes in a value are escaped (the same sanitizer the dump path uses).
		{Change{Key: Title, Kind: ChangeChanged, Old: []string{"a\x1bb"}, New: []string{"c"}}, `~ TITLE: a\x1bb -> c`},
		// A control byte in the KEY is escaped too: a custom Vorbis/MP4 field name
		// bypasses key validation on parse, so it can carry control bytes.
		{Change{Key: Key("BAD\x1bKEY"), Kind: ChangeAdded, New: []string{"v"}}, `+ BAD\x1bKEY: v`},
		// A newline in a value is escaped: the change row is single-line, so a
		// multi-line value (lyrics) or hostile input cannot forge a second row.
		{Change{Key: Title, Kind: ChangeAdded, New: []string{"a\nb"}}, `+ TITLE: a\x0ab`},
		{Change{}, ""}, // the zero (unknown) kind renders nothing
	}
	for _, c := range cases {
		if got := c.c.String(); got != c.want {
			t.Errorf("Change.String() = %q, want %q", got, c.want)
		}
	}
}

func TestSanitizeText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"clean", "clean"},
		{"a\x1b[31mb", `a\x1b[31mb`},       // ESC (the ANSI CSI introducer)
		{"bell\x07", `bell\x07`},           // BEL
		{"a\rb", `a\x0db`},                 // a mid-string carriage return
		{"back\x08space", `back\x08space`}, // backspace
		{"\x7f", `\x7f`},                   // DEL
		{"\u009b", `\x9b`},                 // a C1 control (CSI), validly UTF-8 encoded
		{"keep\ttab", "keep\ttab"},         // tab preserved
		{"keep\nnewline", "keep\nnewline"}, // newline preserved (the value renderer owns it)
	}
	for _, c := range cases {
		if got := SanitizeText(c.in); got != c.want {
			t.Errorf("SanitizeText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeTextPreservesUnicode is the UTF-8 regression guard: a naive
// byte-level C1 (0x80-0x9F) check would corrupt multi-byte text, whose
// continuation bytes live in that range. Decoding to runes first keeps it intact.
func TestSanitizeTextPreservesUnicode(t *testing.T) {
	for _, s := range []string{"café", "naïve", "日本語", "emoji 🎵🎶", "Þórr"} {
		if got := SanitizeText(s); got != s {
			t.Errorf("SanitizeText(%q) = %q, want it unchanged", s, got)
		}
	}
}

// TestSanitizeTextInvalidUTF8: an invalid UTF-8 byte is escaped on its own rather
// than emitting a replacement character.
func TestSanitizeTextInvalidUTF8(t *testing.T) {
	if got := SanitizeText("a\xffb"); got != `a\xffb` {
		t.Errorf("SanitizeText(invalid byte) = %q, want %q", got, `a\xffb`)
	}
}

// TestSanitizeLine is SanitizeText's bar plus the tab and newline: a single-line
// field (a tag key, a chapter title, a change-line value) must occupy exactly one
// line, so both are escaped - unlike SanitizeText, which keeps them for the
// multi-line value renderer. Everything else escapes identically, and multi-byte
// text still survives.
func TestSanitizeLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"clean", "clean"},
		{"a\x1b[31mb", `a\x1b[31mb`},     // ESC still escaped (same as SanitizeText)
		{"keep\ttab", `keep\x09tab`},     // tab now escaped, not preserved
		{"line\nbreak", `line\x0abreak`}, // newline now escaped, not preserved
		{"café 🎵", "café 🎵"},             // multi-byte text survives intact
		{"a\xffb", `a\xffb`},             // invalid UTF-8 still escaped per byte
	}
	for _, c := range cases {
		if got := SanitizeLine(c.in); got != c.want {
			t.Errorf("SanitizeLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
