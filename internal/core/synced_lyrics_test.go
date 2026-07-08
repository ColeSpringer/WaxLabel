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

// TestParseLRCCarriageReturns is a regression guard: classic-Mac pure-CR line endings (and CRLF)
// must be split like LF, not read as one concatenated line.
func TestParseLRCCarriageReturns(t *testing.T) {
	for _, sep := range []string{"\r", "\r\n", "\n"} {
		got := ParseLRC("[00:01.00]A" + sep + "[00:02.00]B")
		if len(got) != 2 || got[0].Text != "A" || got[1].Text != "B" {
			t.Errorf("ParseLRC with %q separator = %+v, want two lines A,B", sep, got)
		}
	}
}

// TestParseLRCRejectsOutOfRangeSeconds is a regression guard: a seconds field >= 60 is malformed
// in every form, and minutes >= 60 are rejected only in the three-part HH:MM:SS form; the
// two-part MM:SS form keeps a large minute count for a long track ("[120:00.00]").
func TestParseLRCRejectsOutOfRangeSeconds(t *testing.T) {
	for _, in := range []string{
		"[00:99.00]x", // 99 seconds, MM:SS
		"[01:99:00]x", // 99 seconds, HH:MM:SS
		"[01:60:00]x", // 60 minutes, HH:MM:SS
	} {
		if got := ParseLRC(in); got != nil {
			t.Errorf("ParseLRC(%q) = %+v, want no line (out-of-range field)", in, got)
		}
	}
	if got := ParseLRC("[120:00.00]x"); len(got) != 1 || got[0].Time != 120*time.Minute {
		t.Errorf("ParseLRC([120:00.00]) = %+v, want one line at 120m (long track, two-part form)", got)
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

// TestFormatLRCSpaceSeparator pins the convention: FormatLRC separates a timestamp from
// non-empty text with exactly one space, while an empty-text clear marker stays a bare timestamp
// with no trailing space.
func TestFormatLRCSpaceSeparator(t *testing.T) {
	got := FormatLRC([]SyncedLine{
		{Time: time.Second, Text: "hi"},
		{Time: 2 * time.Second, Text: ""},
	})
	want := "[00:01.000] hi\n[00:02.000]"
	if got != want {
		t.Errorf("FormatLRC = %q, want %q", got, want)
	}
}

// TestLRCTimestampShapedTextRoundTrip is the corruption repro: a lyric whose text is
// itself a literal [mm:ss.xx]-shaped string used to corrupt on FLAC/Ogg - FormatLRC wrote
// "[00:03.000][00:05.000]hi" with no separator, which ParseLRC read back as two phantom lines. The
// space separator disambiguates it, so it now round-trips as one line whose text keeps the
// bracketed prefix - including a lyric that legitimately begins with its own space.
func TestLRCTimestampShapedTextRoundTrip(t *testing.T) {
	for _, text := range []string{
		"[00:05.000]hi",                 // text looks like a second timestamp
		"[00:05.000]",                   // text is exactly a timestamp string
		"[offset:500]x",                 // text looks like an offset directive
		"[00:01.000] [00:02.000]spaced", // text is several timestamp-shaped groups
		" hi",                           // legitimate leading space (written as two, stripped to one)
		"  two leading",
	} {
		lines := []SyncedLine{{Time: 3 * time.Second, Text: text}}
		got := ParseLRC(FormatLRC(lines))
		if len(got) != 1 || got[0].Time != 3*time.Second || got[0].Text != text {
			t.Errorf("round-trip %q = %+v (LRC %q), want one {3s %q}", text, got, FormatLRC(lines), text)
		}
	}
}

// TestParseLRCTimestampTextSeparator covers how the read side treats the space between a timestamp
// group and text, including externally-authored input: a space after a run of adjacent timestamps
// separates shared text, a space between two timestamps stops collection at the first, and a
// no-space external line is unaffected (there is no separator to strip).
func TestParseLRCTimestampTextSeparator(t *testing.T) {
	if got := ParseLRC("[00:01.00][00:02.00] chorus"); len(got) != 2 || got[0].Text != "chorus" || got[1].Text != "chorus" {
		t.Errorf("adjacent-then-space = %+v, want two lines 'chorus'", got)
	}
	if g := ParseLRC("[00:01.00] [00:02.00]text"); len(g) != 1 || g[0].Time != time.Second || g[0].Text != "[00:02.00]text" {
		t.Errorf("space-between = %+v, want one {1s \"[00:02.00]text\"}", g)
	}
	if g := ParseLRC("[00:03.00]hi"); len(g) != 1 || g[0].Text != "hi" {
		t.Errorf("external no-space = %+v, want one {_ \"hi\"}", g)
	}
}

// FuzzLRCDoubleParse asserts double-parse idempotency: ParseLRC(FormatLRC(ParseLRC(x)))
// equals ParseLRC(x) line for line. It holds for arbitrary input because ParseLRC's output never
// contains an embedded newline (it splits on them), so FormatLRC's one non-inverse - flattening an
// embedded newline to a space - never fires on already-parsed lines. Constructed-line equality is
// deliberately not asserted (see the round-trip test above for the specific pinned cases).
func FuzzLRCDoubleParse(f *testing.F) {
	for _, s := range []string{
		"[00:01.000]hi",
		"[00:03.000][00:05.000]hi",       // adjacent timestamps
		"[00:03.00] [00:05.00]hi",        // space between: text is a timestamp string
		"[00:01.00][00:02.00] chorus",    // shared text after a run
		"[offset:500][00:01.000]A",       // offset directive
		"[ar:Artist]\n[00:02.000] world", // metadata + a spaced line
		"[00:30.000]",                    // bare clear marker
		"plain text no stamp",
		"\ufeff[00:01.000]bom",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, x string) {
		once := ParseLRC(x)
		twice := ParseLRC(FormatLRC(once))
		if len(once) != len(twice) {
			t.Fatalf("line count changed: %d -> %d (x=%q, LRC=%q)", len(once), len(twice), x, FormatLRC(once))
		}
		for i := range once {
			if once[i] != twice[i] {
				t.Errorf("line %d changed: %+v -> %+v (x=%q)", i, once[i], twice[i], x)
			}
		}
	})
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
	// An embedded line break in a line's text is flattened to a space by the LRC store,
	// so a set carrying one is a lossy carry even with no language or descriptor.
	for _, brk := range []string{"a\nb", "a\r\nb", "a\rb"} {
		set := []SyncedLyrics{{Lines: []SyncedLine{{Time: 0, Text: brk}}}}
		if !SyncedLyricsLoseMetadata(set, SyncedLyricsLossLanguage) {
			t.Errorf("a set with an embedded line break %q should be lossy under the LRC store", brk)
		}
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
