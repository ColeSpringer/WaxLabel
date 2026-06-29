package core

import (
	"testing"
	"time"
)

// TestParseLRCBasics checks the core LRC behaviors: leading metadata tags are skipped, a
// multi-timestamp line yields one line per stamp, timestamps sort, and an empty-text clear
// marker is preserved.
func TestParseLRCBasics(t *testing.T) {
	in := "[ar:Artist]\n[ti:Title]\n[al:Album]\n[length:03:00]\n" +
		"[00:12.00]Line A\n[00:45.10][00:21.10]Chorus\n[00:30.000]\nplain line with no stamp"
	got := ParseLRC(in)
	want := []SyncedLine{
		{Time: 12 * time.Second, Text: "Line A"},
		{Time: 21100 * time.Millisecond, Text: "Chorus"},
		{Time: 30 * time.Second, Text: ""}, // clear marker
		{Time: 45100 * time.Millisecond, Text: "Chorus"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestParseLRCOffset checks the foobar2000 offset rule (effective = timestamp - offset),
// clamped at zero, and that the sign is applied as documented.
func TestParseLRCOffset(t *testing.T) {
	// offset 500 shifts every line 500 ms earlier.
	got := ParseLRC("[offset:500]\n[00:01.000]A\n[00:00.200]B")
	if len(got) != 2 {
		t.Fatalf("got %d lines", len(got))
	}
	// B: 200ms - 500ms clamps to 0; A: 1000ms - 500ms = 500ms. Sorted: B(0) then A(500).
	if got[0].Time != 0 || got[0].Text != "B" {
		t.Errorf("line0 = %+v, want {0 B}", got[0])
	}
	if got[1].Time != 500*time.Millisecond || got[1].Text != "A" {
		t.Errorf("line1 = %+v, want {500ms A}", got[1])
	}
	// A negative offset shifts later.
	g2 := ParseLRC("[offset:-250]\n[00:01.000]A")
	if g2[0].Time != 1250*time.Millisecond {
		t.Errorf("negative offset: %v, want 1.25s", g2[0].Time)
	}
}

// TestParseLRCBOM checks a leading UTF-8 BOM (common in Windows-saved LRC files) is stripped
// so the first timed line is not lost.
func TestParseLRCBOM(t *testing.T) {
	got := ParseLRC("\ufeff[00:01.000]One\n[00:12.000]Two")
	if len(got) != 2 || got[0].Text != "One" {
		t.Errorf("BOM-prefixed document = %+v, want both lines starting with One", got)
	}
}

// TestParseLRCInlineOffset checks an [offset:N] tag co-located before a timestamp on one
// line is applied without losing that line's lyric (the offset and the stamp are both read
// in the single leading-tag pass).
func TestParseLRCInlineOffset(t *testing.T) {
	got := ParseLRC("[offset:500][00:01.000]Hello")
	if len(got) != 1 || got[0].Text != "Hello" || got[0].Time != 500*time.Millisecond {
		t.Errorf("inline offset = %+v, want one {500ms Hello}", got)
	}
	// An offset on its own line still applies to a following line.
	if g := ParseLRC("[offset:500]\n[00:01.000]A"); len(g) != 1 || g[0].Time != 500*time.Millisecond {
		t.Errorf("standalone offset = %+v, want {500ms A}", g)
	}
}

// TestLRCFractionScaling checks the fractional second scales by digit count: ".5" is
// 500ms, ".05" is 50ms, ".050" is 50ms, and ".345" is 345ms. Both the centisecond LRC
// convention and the millisecond form WaxLabel emits should parse.
func TestLRCFractionScaling(t *testing.T) {
	cases := map[string]time.Duration{
		"[00:00.5]x":   500 * time.Millisecond,
		"[00:00.05]x":  50 * time.Millisecond,
		"[00:00.050]x": 50 * time.Millisecond,
		"[00:00.345]x": 345 * time.Millisecond,
		"[00:00]x":     0,
		"[01:00.000]x": time.Minute,
	}
	for in, want := range cases {
		got := ParseLRC(in)
		if len(got) != 1 || got[0].Time != want {
			t.Errorf("ParseLRC(%q) = %+v, want time %v", in, got, want)
		}
	}
}

// TestLRCTimestampForms checks the lenient timestamp forms the parser accepts beyond the
// canonical [mm:ss.mmm]: an optional three-part hours form ([hh:mm:ss.fff], used by some
// long files) and surrounding whitespace inside the brackets.
func TestLRCTimestampForms(t *testing.T) {
	cases := map[string]time.Duration{
		"[01:02:03.500]x": time.Hour + 2*time.Minute + 3*time.Second + 500*time.Millisecond,
		"[2:00:00]x":      2 * time.Hour,
		"[ 00:12.00 ]x":   12 * time.Second,  // edge whitespace inside the bracket
		"[120:00.00]x":    120 * time.Minute, // a large minute count (the standard long form)
	}
	for in, want := range cases {
		got := ParseLRC(in)
		if len(got) != 1 || got[0].Time != want {
			t.Errorf("ParseLRC(%q) = %+v, want one line at %v", in, got, want)
		}
	}
	// A spaced offset tag still applies.
	if got := ParseLRC("[ offset:500 ]\n[00:01.000]A"); len(got) != 1 || got[0].Time != 500*time.Millisecond {
		t.Errorf("spaced offset: %+v, want one line at 500ms", got)
	}
}

// TestFormatLRCRoundTrip checks FormatLRC emits [mm:ss.mmm] and round-trips losslessly
// through ParseLRC, including a long (>99 minute) timestamp and an empty clear marker.
func TestFormatLRCRoundTrip(t *testing.T) {
	// Already sorted by time, since ParseLRC returns lines sorted (the round-trip must not
	// reorder them).
	lines := []SyncedLine{
		{Time: 0, Text: "start"},
		{Time: time.Hour, Text: ""}, // clear marker at 60:00
		{Time: 90*time.Minute + 12*time.Second + 345*time.Millisecond, Text: "long"},
	}
	out := FormatLRC(lines)
	got := ParseLRC(out)
	if len(got) != len(lines) {
		t.Fatalf("round-trip got %d lines from %q", len(got), out)
	}
	for i := range lines {
		if got[i] != lines[i] {
			t.Errorf("round-trip line %d = %+v, want %+v (LRC %q)", i, got[i], lines[i], out)
		}
	}
}

// TestLRCBracketTextRoundTrip checks a lyric line whose text begins with a non-timestamp
// bracket group (a section marker like "[Chorus]") round-trips: ParseLRC must stop
// collecting timestamp tags at the first non-timestamp group rather than swallowing the
// marker as a tag.
func TestLRCBracketTextRoundTrip(t *testing.T) {
	for _, text := range []string{"[Chorus]", "[Verse 1] sing along", "[Bridge]", "[Intro]", "la [x] la", "plain"} {
		lines := []SyncedLine{{Time: time.Second, Text: text}}
		got := ParseLRC(FormatLRC(lines))
		if len(got) != 1 || got[0].Time != time.Second || got[0].Text != text {
			t.Errorf("round-trip %q = %+v (LRC %q), want one {1s %q}", text, got, FormatLRC(lines), text)
		}
	}
	// A bare metadata line still contributes no timed line (no leading timestamp).
	if got := ParseLRC("[ar:Artist]\n[ti:Title]"); got != nil {
		t.Errorf("metadata-only document yielded lines: %+v", got)
	}
}

// TestLRCFieldOverflowRejected checks an absurd minute field, including one that would
// overflow a time.Duration, is skipped rather than wrapped to an invalid negative value.
// A huge [offset:] is clamped rather than overflowed.
func TestLRCFieldOverflowRejected(t *testing.T) {
	// Minute values past maxLRCField, an hours field large enough to overflow when
	// multiplied by time.Hour, and an hours value that is valid per-field but whose
	// minute-normalized form (what FormatLRC emits) exceeds maxLRCField and would not
	// re-parse, must all be skipped rather than wrapped or made non-round-trippable.
	for _, in := range []string{"[153722868:00.00]x", "[400000000:00.00]y", "[999999999:00]z", "[2562048:00:00]h", "[2000000:00:00]m"} {
		if got := ParseLRC(in); len(got) != 0 {
			t.Errorf("ParseLRC(%q) = %+v, want no line (absurd field skipped)", in, got)
		}
	}
	// A gigantic positive offset is clamped (not overflowed) and applied to the real 10s
	// line, shifting it back to 0 - the line must be present (exercising the clamp), not
	// dropped, and non-negative.
	got := ParseLRC("[offset:99999999999999999][00:10.00]x")
	if len(got) != 1 {
		t.Fatalf("huge inline offset: got %d lines, want 1 (the line must survive and exercise the clamp)", len(got))
	}
	if got[0].Time != 0 {
		t.Errorf("huge offset applied to 10s line = %v, want 0 (clamped offset shifts it back)", got[0].Time)
	}
	// A gigantic negative offset shifts forward but is capped at the re-emittable maximum,
	// staying non-negative and bounded.
	for _, ln := range ParseLRC("[offset:-99999999999999999][00:10.00]y") {
		if ln.Time < 0 || ln.Time > time.Duration(1<<21)*time.Minute {
			t.Errorf("huge negative offset produced out-of-range time %v", ln.Time)
		}
	}
}

// TestFormatLRCFlattensNewlines checks an embedded newline in a line's text is flattened to
// a space rather than written as a literal record separator (which ParseLRC would read as a
// line break, dropping everything after it).
func TestFormatLRCFlattensNewlines(t *testing.T) {
	for _, in := range []string{"hello\nworld", "hello\r\nworld", "hello\rworld"} {
		got := ParseLRC(FormatLRC([]SyncedLine{{Time: time.Second, Text: in}}))
		if len(got) != 1 || got[0].Text != "hello world" {
			t.Errorf("FormatLRC/ParseLRC of %q = %+v, want one {1s \"hello world\"}", in, got)
		}
	}
}

// TestEqualSyncedLyrics checks element-wise equality across language, descriptor, and lines.
func TestEqualSyncedLyrics(t *testing.T) {
	a := []SyncedLyrics{{Language: "eng", Description: "d", Lines: []SyncedLine{{Time: time.Second, Text: "x"}}}}
	b := []SyncedLyrics{{Language: "eng", Description: "d", Lines: []SyncedLine{{Time: time.Second, Text: "x"}}}}
	if !EqualSyncedLyrics(a, b) {
		t.Error("identical sets not equal")
	}
	for _, diff := range []func(*SyncedLyrics){
		func(s *SyncedLyrics) { s.Language = "spa" },
		func(s *SyncedLyrics) { s.Description = "other" },
		func(s *SyncedLyrics) { s.Lines = append(s.Lines, SyncedLine{Time: 2 * time.Second, Text: "y"}) },
		func(s *SyncedLyrics) { s.Lines[0].Text = "z" },
	} {
		c := CloneSyncedLyrics(b)
		diff(&c[0])
		if EqualSyncedLyrics(a, c) {
			t.Errorf("expected inequality after mutation, got equal: %+v", c)
		}
	}
}

// TestCloneSyncedLyricsDetaches checks the clone deep-copies Lines so a mutation cannot
// reach back into the source, and preserves nil.
func TestCloneSyncedLyricsDetaches(t *testing.T) {
	if CloneSyncedLyrics(nil) != nil {
		t.Error("clone of nil should be nil")
	}
	src := []SyncedLyrics{{Lines: []SyncedLine{{Time: time.Second, Text: "x"}}}}
	c := CloneSyncedLyrics(src)
	c[0].Lines[0].Text = "mutated"
	if src[0].Lines[0].Text != "x" {
		t.Error("clone shares the Lines backing array with the source")
	}
}

// TestSyncedLyricsLoseMetadata checks the language-loss predicate fires only when a set
// carries a per-set language or descriptor.
func TestSyncedLyricsLoseMetadata(t *testing.T) {
	plain := []SyncedLyrics{{Lines: []SyncedLine{{Time: 0, Text: "x"}}}}
	if SyncedLyricsLoseMetadata(plain, SyncedLyricsLossLanguage) {
		t.Error("a plain timed-text set should not be lossy")
	}
	if SyncedLyricsLoseMetadata(plain, SyncedLyricsLossNone) {
		t.Error("SyncedLyricsLossNone should never be lossy")
	}
	withLang := []SyncedLyrics{{Language: "eng", Lines: plain[0].Lines}}
	if !SyncedLyricsLoseMetadata(withLang, SyncedLyricsLossLanguage) {
		t.Error("a set with a language should be lossy under the LRC store")
	}
	withDesc := []SyncedLyrics{{Description: "d", Lines: plain[0].Lines}}
	if !SyncedLyricsLoseMetadata(withDesc, SyncedLyricsLossLanguage) {
		t.Error("a set with a descriptor should be lossy under the LRC store")
	}
}

// FuzzParseLRC asserts the LRC parser never panics and never yields a negative timestamp.
func FuzzParseLRC(f *testing.F) {
	f.Add("")
	f.Add("[00:12.00]Line")
	f.Add("[offset:-9999999999999][00:00.00]x")
	f.Add("[ti:title][99:99.99]weird")
	f.Add("[[[[::::....")
	f.Add("[00:00.000][00:00.000]dup\nplain")
	f.Add("[00:01.000][Chorus]")                      // bracket-leading text (regression)
	f.Add("[153722868:00.00]overflow")                // minute field overflow (regression)
	f.Add("[2562048:00:00]hours")                     // hours field overflow (regression)
	f.Add("[2000000:00:00]minform")                   // hours valid per-field, minute form too big (regression)
	f.Add("[offset:99999999999999999][00:10.00]huge") // offset overflow (regression)
	f.Add("[ 01:02:03.50 ]spaced hours")              // whitespace + hours form
	f.Fuzz(func(t *testing.T, text string) {
		lines := ParseLRC(text)
		for _, ln := range lines {
			if ln.Time < 0 {
				t.Errorf("negative timestamp %v from %q", ln.Time, text)
			}
		}
		// FormatLRC of the result must re-parse to the same lines (idempotent projection).
		if got := ParseLRC(FormatLRC(lines)); len(got) != len(lines) {
			t.Errorf("re-parse changed line count: %d -> %d", len(lines), len(got))
		}
	})
}
