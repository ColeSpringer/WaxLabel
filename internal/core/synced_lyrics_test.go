package core

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestParseLRCBasics: skip metadata tags, split multi-timestamp lines, sort, preserve clear markers.
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

// TestParseLRCReportTruncation: read-path line cap; truncated=true when over MaxSyncedLines.
func TestParseLRCReportTruncation(t *testing.T) {
	var b strings.Builder
	const over = MaxSyncedLines + 5
	for i := 0; i < over; i++ {
		fmt.Fprintf(&b, "[%d:00.000]x\n", i)
	}
	lines, truncated := ParseLRCReport(b.String())
	if !truncated {
		t.Errorf("a >cap LRC document must report truncated=true")
	}
	if len(lines) != MaxSyncedLines {
		t.Errorf("truncated line count = %d, want the cap %d", len(lines), MaxSyncedLines)
	}
	if _, tr := ParseLRCReport("[00:01.00]a\n[00:02.00]b"); tr {
		t.Errorf("a within-cap document wrongly reported truncated=true")
	}
}

// TestParseLRCOffset: effective = timestamp - offset, clamped at zero.
func TestParseLRCOffset(t *testing.T) {
	got := ParseLRC("[offset:500]\n[00:01.000]A\n[00:00.200]B")
	if len(got) != 2 {
		t.Fatalf("got %d lines", len(got))
	}
	if got[0].Time != 0 || got[0].Text != "B" {
		t.Errorf("line0 = %+v, want {0 B}", got[0])
	}
	if got[1].Time != 500*time.Millisecond || got[1].Text != "A" {
		t.Errorf("line1 = %+v, want {500ms A}", got[1])
	}
	g2 := ParseLRC("[offset:-250]\n[00:01.000]A")
	if g2[0].Time != 1250*time.Millisecond {
		t.Errorf("negative offset: %v, want 1.25s", g2[0].Time)
	}
}

// TestParseLRCBOM: strip leading UTF-8 BOM.
func TestParseLRCBOM(t *testing.T) {
	got := ParseLRC("\ufeff[00:01.000]One\n[00:12.000]Two")
	if len(got) != 2 || got[0].Text != "One" {
		t.Errorf("BOM-prefixed document = %+v, want both lines starting with One", got)
	}
}

// TestParseLRCInlineOffset: inline [offset:N] before a timestamp on the same line.
func TestParseLRCInlineOffset(t *testing.T) {
	got := ParseLRC("[offset:500][00:01.000]Hello")
	if len(got) != 1 || got[0].Text != "Hello" || got[0].Time != 500*time.Millisecond {
		t.Errorf("inline offset = %+v, want one {500ms Hello}", got)
	}
	if g := ParseLRC("[offset:500]\n[00:01.000]A"); len(g) != 1 || g[0].Time != 500*time.Millisecond {
		t.Errorf("standalone offset = %+v, want {500ms A}", g)
	}
}

// TestLRCFractionScaling: fractional seconds scale by digit count.
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

// TestLRCTimestampForms: HH:MM:SS form and whitespace inside brackets.
func TestLRCTimestampForms(t *testing.T) {
	cases := map[string]time.Duration{
		"[01:02:03.500]x": time.Hour + 2*time.Minute + 3*time.Second + 500*time.Millisecond,
		"[2:00:00]x":      2 * time.Hour,
		"[ 00:12.00 ]x":   12 * time.Second,
		"[120:00.00]x":    120 * time.Minute,
	}
	for in, want := range cases {
		got := ParseLRC(in)
		if len(got) != 1 || got[0].Time != want {
			t.Errorf("ParseLRC(%q) = %+v, want one line at %v", in, got, want)
		}
	}
	if got := ParseLRC("[ offset:500 ]\n[00:01.000]A"); len(got) != 1 || got[0].Time != 500*time.Millisecond {
		t.Errorf("spaced offset: %+v, want one line at 500ms", got)
	}
}

// TestParseLRCCarriageReturns: CR and CRLF split like LF.
func TestParseLRCCarriageReturns(t *testing.T) {
	for _, sep := range []string{"\r", "\r\n", "\n"} {
		got := ParseLRC("[00:01.00]A" + sep + "[00:02.00]B")
		if len(got) != 2 || got[0].Text != "A" || got[1].Text != "B" {
			t.Errorf("ParseLRC with %q separator = %+v, want two lines A,B", sep, got)
		}
	}
}

// TestParseLRCRejectsOutOfRangeSeconds: seconds >= 60 rejected; large minutes OK in MM:SS form.
func TestParseLRCRejectsOutOfRangeSeconds(t *testing.T) {
	for _, in := range []string{
		"[00:99.00]x",
		"[01:99:00]x",
		"[01:60:00]x",
	} {
		if got := ParseLRC(in); got != nil {
			t.Errorf("ParseLRC(%q) = %+v, want no line (out-of-range field)", in, got)
		}
	}
	if got := ParseLRC("[120:00.00]x"); len(got) != 1 || got[0].Time != 120*time.Minute {
		t.Errorf("ParseLRC([120:00.00]) = %+v, want one line at 120m (long track, two-part form)", got)
	}
}

// TestFormatLRCRoundTrip: FormatLRC round-trips through ParseLRC.
func TestFormatLRCRoundTrip(t *testing.T) {
	lines := []SyncedLine{
		{Time: 0, Text: "start"},
		{Time: time.Hour, Text: ""},
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

// TestLRCBracketTextRoundTrip: bracketed lyric text (e.g. [Chorus]) round-trips.
func TestLRCBracketTextRoundTrip(t *testing.T) {
	for _, text := range []string{"[Chorus]", "[Verse 1] sing along", "[Bridge]", "[Intro]", "la [x] la", "plain"} {
		lines := []SyncedLine{{Time: time.Second, Text: text}}
		got := ParseLRC(FormatLRC(lines))
		if len(got) != 1 || got[0].Time != time.Second || got[0].Text != text {
			t.Errorf("round-trip %q = %+v (LRC %q), want one {1s %q}", text, got, FormatLRC(lines), text)
		}
	}
	if got := ParseLRC("[ar:Artist]\n[ti:Title]"); got != nil {
		t.Errorf("metadata-only document yielded lines: %+v", got)
	}
}

// TestLRCFieldOverflowRejected: absurd fields skipped; huge offset clamped, not overflowed.
func TestLRCFieldOverflowRejected(t *testing.T) {
	for _, in := range []string{"[153722868:00.00]x", "[400000000:00.00]y", "[999999999:00]z", "[2562048:00:00]h", "[2000000:00:00]m"} {
		if got := ParseLRC(in); len(got) != 0 {
			t.Errorf("ParseLRC(%q) = %+v, want no line (absurd field skipped)", in, got)
		}
	}
	got := ParseLRC("[offset:99999999999999999][00:10.00]x")
	if len(got) != 1 {
		t.Fatalf("huge inline offset: got %d lines, want 1 (the line must survive and exercise the clamp)", len(got))
	}
	if got[0].Time != 0 {
		t.Errorf("huge offset applied to 10s line = %v, want 0 (clamped offset shifts it back)", got[0].Time)
	}
	for _, ln := range ParseLRC("[offset:-99999999999999999][00:10.00]y") {
		if ln.Time < 0 || ln.Time > time.Duration(1<<21)*time.Minute {
			t.Errorf("huge negative offset produced out-of-range time %v", ln.Time)
		}
	}
}

// TestFormatLRCFlattensNewlines: embedded newlines flattened to space on write.
func TestFormatLRCFlattensNewlines(t *testing.T) {
	for _, in := range []string{"hello\nworld", "hello\r\nworld", "hello\rworld"} {
		got := ParseLRC(FormatLRC([]SyncedLine{{Time: time.Second, Text: in}}))
		if len(got) != 1 || got[0].Text != "hello world" {
			t.Errorf("FormatLRC/ParseLRC of %q = %+v, want one {1s \"hello world\"}", in, got)
		}
	}
}

// TestFormatLRCSpaceSeparator: one space before text; clear marker has no trailing space.
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

// TestLRCTimestampShapedTextRoundTrip: timestamp-shaped lyric text round-trips (space separator disambiguates).
func TestLRCTimestampShapedTextRoundTrip(t *testing.T) {
	for _, text := range []string{
		"[00:05.000]hi",
		"[00:05.000]",
		"[offset:500]x",
		"[00:01.000] [00:02.000]spaced",
		" hi",
		"  two leading",
	} {
		lines := []SyncedLine{{Time: 3 * time.Second, Text: text}}
		got := ParseLRC(FormatLRC(lines))
		if len(got) != 1 || got[0].Time != 3*time.Second || got[0].Text != text {
			t.Errorf("round-trip %q = %+v (LRC %q), want one {3s %q}", text, got, FormatLRC(lines), text)
		}
	}
}

// TestParseLRCTimestampTextSeparator: space after adjacent timestamps shares text; space between timestamps stops collection.
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

// FuzzLRCDoubleParse: ParseLRC(FormatLRC(ParseLRC(x))) == ParseLRC(x).
func FuzzLRCDoubleParse(f *testing.F) {
	for _, s := range []string{
		"[00:01.000]hi",
		"[00:03.000][00:05.000]hi",
		"[00:03.00] [00:05.00]hi",
		"[00:01.00][00:02.00] chorus",
		"[offset:500][00:01.000]A",
		"[ar:Artist]\n[00:02.000] world",
		"[00:30.000]",
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

// TestCloneSyncedLyricsDetaches: clone deep-copies Lines; nil in, nil out.
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

// TestSyncedLyricsLoseMetadata: language/descriptor and embedded line breaks are lossy under LRC store.
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
	for _, brk := range []string{"a\nb", "a\r\nb", "a\rb"} {
		set := []SyncedLyrics{{Lines: []SyncedLine{{Time: 0, Text: brk}}}}
		if !SyncedLyricsLoseMetadata(set, SyncedLyricsLossLanguage) {
			t.Errorf("a set with an embedded line break %q should be lossy under the LRC store", brk)
		}
	}
}

// FuzzParseLRC: no panic; no negative timestamps.
func FuzzParseLRC(f *testing.F) {
	f.Add("")
	f.Add("[00:12.00]Line")
	f.Add("[offset:-9999999999999][00:00.00]x")
	f.Add("[ti:title][99:99.99]weird")
	f.Add("[[[[::::....")
	f.Add("[00:00.000][00:00.000]dup\nplain")
	f.Add("[00:01.000][Chorus]")
	f.Add("[153722868:00.00]overflow")
	f.Add("[2562048:00:00]hours")
	f.Add("[2000000:00:00]minform")
	f.Add("[offset:99999999999999999][00:10.00]huge")
	f.Add("[ 01:02:03.50 ]spaced hours")
	f.Fuzz(func(t *testing.T, text string) {
		lines := ParseLRC(text)
		for _, ln := range lines {
			if ln.Time < 0 {
				t.Errorf("negative timestamp %v from %q", ln.Time, text)
			}
		}
		if got := ParseLRC(FormatLRC(lines)); len(got) != len(lines) {
			t.Errorf("re-parse changed line count: %d -> %d", len(lines), len(got))
		}
	})
}

// TestParseLRCReportFullDroppedLines: 1-based line numbers for dropped content; structure tags excluded.
func TestParseLRCReportFullDroppedLines(t *testing.T) {
	in := strings.Join([]string{
		"[ar:Artist]",
		"[00:01.00]good",
		"[9:99.99]bad stamp",
		"just some text",
		"[Chorus]",
		"[offset:+200]",
		"[length:03:45]",
		"",
		"   ",
		"[00:05.00]good two",
	}, "\n")
	lines, dropped := ParseLRCReportFull(in)
	if len(lines) != 2 {
		t.Fatalf("timed lines = %d, want 2: %+v", len(lines), lines)
	}
	want := []int{3, 4, 5}
	if len(dropped) != len(want) {
		t.Fatalf("dropped lines = %v, want %v", dropped, want)
	}
	for i := range want {
		if dropped[i] != want[i] {
			t.Errorf("dropped[%d] = %d, want %d", i, dropped[i], want[i])
		}
	}
}

// TestCountsAsDroppedLRCLine: structure tags not dropped; bad stamps and bare bracket groups are.
func TestCountsAsDroppedLRCLine(t *testing.T) {
	cases := []struct {
		line    string
		dropped bool
	}{
		{"", false},
		{"   ", false},
		{"[ar:Artist]", false},
		{"[ti:Title]", false},
		{"[al:Album]", false},
		{"[au:Author]", false},
		{"[by:Creator]", false},
		{"[re:Editor]", false},
		{"[ve:1.0]", false},
		{"[offset:+200]", false},
		{"[offset:-250]", false},
		{"[length:03:45]", false},
		{"[Chorus]", true},
		{"[Verse 1]", true},
		{"[nocolonhere]", true},
		{"[00.00.00]", true},
		{"just some text", true},
		{"[9:99.99]bad", true},
		{"[9:99.99]", true},
		{"[Note: whatever]", true},
		{"[Chorus] with text", true},
	}
	for _, c := range cases {
		if got := countsAsDroppedLRCLine(c.line); got != c.dropped {
			t.Errorf("countsAsDroppedLRCLine(%q) = %v, want %v", c.line, got, c.dropped)
		}
	}
}
