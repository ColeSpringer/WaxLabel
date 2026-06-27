package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseChapterTimestamp(t *testing.T) {
	ms := time.Millisecond
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"90", 90 * time.Second},          // bare seconds
		{"90.5", 90*time.Second + 500*ms}, // fractional bare seconds
		{"0", 0},                          // zero
		{"01:30", 90 * time.Second},       // MM:SS
		{"1:30", 90 * time.Second},        // MM:SS, no leading zero
		{"00:00.500", 500 * ms},           // fractional in MM:SS
		{"90:00", 90 * time.Minute},       // leading minutes unbounded
		{"0:01:30.000", 90 * time.Second}, // H:MM:SS.mmm (the dump format)
		{"1:02:03.5", time.Hour + 2*time.Minute + 3*time.Second + 500*ms},
		{"1:02:03.500", time.Hour + 2*time.Minute + 3*time.Second + 500*ms}, // 3 fractional digits
		{".5", 500 * ms},                 // bare fractional seconds, single digit
		{"2:00:00", 2 * time.Hour},       // hours
		{"1000:00:00", 1000 * time.Hour}, // large but representable (long audiobook)
		{"100000", 100000 * time.Second}, // large bare seconds, ~27.7 hours
	}
	for _, c := range cases {
		got, err := parseChapterTimestamp(c.in)
		if err != nil {
			t.Errorf("parseChapterTimestamp(%q) error = %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseChapterTimestamp(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseChapterTimestampRejects(t *testing.T) {
	// Every entry must be a usage error: negatives, out-of-range inner fields,
	// non-numeric input, the Inf/NaN ParseFloat accepts, too many components, and a
	// magnitude that would overflow int64-nanosecond time.Duration (which must be
	// rejected, not silently wrapped to a negative duration).
	bad := []string{
		"", "-90", "-01:00", "01:-30", // empty, negative
		"00:60", "0:00:60", // seconds must be < 60
		"0:90:00",             // inner minutes must be < 60 when hours present
		"1:2:3:4",             // too many components
		"ab", "1:xx", "1.2.3", // non-numeric
		"Inf", "NaN", // ParseFloat accepts these; we must not
		"0x1p4", "1e3", "1_000", "+90", "+1:30", // nonstandard numeric forms outside the decimal grammar
		"1:30.", "00:00:00.9999", ".9999", // dangling dot, and over-precise fractions (> 3 digits)
		"2562048:00:00",       // hours overflow int64 nanoseconds
		"153722868:00",        // leading minutes overflow
		"99999999999",         // bare seconds overflow
		"9999999999999:00:00", // gross hours (multi-wrap to a positive garbage value)
	}
	for _, s := range bad {
		if _, err := parseChapterTimestamp(s); err == nil {
			t.Errorf("parseChapterTimestamp(%q) = nil error, want a usage error", s)
		} else if !isUsageError(err) {
			t.Errorf("parseChapterTimestamp(%q) error is not a usage error: %v", s, err)
		}
	}
}

func TestSplitChapter(t *testing.T) {
	cases := []struct {
		in        string
		wantStart time.Duration
		wantTitle string
	}{
		{"1:30=Verse", 90 * time.Second, "Verse"},
		{"0:00=", 0, ""},           // empty title is allowed
		{"0:00=a=b=c", 0, "a=b=c"}, // title may contain '=' (split on the first only)
		{"90=Late", 90 * time.Second, "Late"},
	}
	for _, c := range cases {
		start, title, err := splitChapter(c.in)
		if err != nil {
			t.Errorf("splitChapter(%q) error = %v", c.in, err)
			continue
		}
		if start != c.wantStart || title != c.wantTitle {
			t.Errorf("splitChapter(%q) = (%v, %q), want (%v, %q)", c.in, start, title, c.wantStart, c.wantTitle)
		}
	}
	// A missing '=' is a usage error.
	if _, _, err := splitChapter("1:30"); err == nil || !isUsageError(err) {
		t.Errorf("splitChapter(%q) error = %v, want a usage error", "1:30", err)
	}
}

// essenceOf returns a file's audio-essence digest via the verify command, so a
// test can assert an edit left the audio untouched.
func essenceOf(t *testing.T, file string) string {
	t.Helper()
	out, _, code := runCLI(t, "--json", "verify", file)
	if code != 0 {
		t.Fatalf("verify %s exit = %d", file, code)
	}
	got := decodeJSONOne[jsonVerify](t, out).Essence
	// A non-empty digest is the precondition for the before==after invariant to mean
	// anything: an empty essence would let "" == "" pass the round-trip check trivially.
	if got == "" {
		t.Fatalf("verify %s produced an empty essence digest", file)
	}
	return got
}

func TestSetAddChapterRoundTrip(t *testing.T) {
	// Adding chapters to an MP4 appends to the existing list and round-trips through
	// dump; the canonical case (first chapter at 0:00) is the plan's example.
	file := copyFixture(t, sampleM4B)
	before := essenceOf(t, file)
	_, _, code := runCLI(t, "set", file, "--clear-chapters",
		"--add-chapter", "0:00=Intro", "--add-chapter", "1:30=Verse")
	if code != 0 {
		t.Fatalf("set --add-chapter exit = %d, want 0", code)
	}
	out, _, _ := runCLI(t, "dump", file)
	if !strings.Contains(out, "0:00:00.000  Intro") || !strings.Contains(out, "0:01:30.000  Verse") {
		t.Errorf("dump after add-chapter missing expected chapters:\n%s", out)
	}
	// The headline invariant: editing chapters never touches the audio essence.
	if after := essenceOf(t, file); after != before {
		t.Errorf("essence changed by chapter edit: %s -> %s", before, after)
	}
}

func TestSetAddChapterAppendsToExisting(t *testing.T) {
	// --add-chapter alone (no --clear-chapters) must APPEND to the file's existing
	// chapters, not replace them. sample_chapters.m4b ships three chapters; adding one
	// more must leave all four. This catches an append->replace regression that the
	// clear-then-add round-trip test would miss.
	file := copyFixture(t, sampleM4B)
	before, _, _ := runCLI(t, "dump", file)
	for _, existing := range []string{"Opening Credits", "Chapter One", "Chapter Two"} {
		if !strings.Contains(before, existing) {
			t.Fatalf("fixture precondition: expected chapter %q in:\n%s", existing, before)
		}
	}
	if _, _, code := runCLI(t, "set", file, "--add-chapter", "0:09=Appended"); code != 0 {
		t.Fatalf("set --add-chapter exit = %d, want 0", code)
	}
	out, _, _ := runCLI(t, "dump", file)
	if !strings.Contains(out, "chapters (4)") {
		t.Errorf("expected 4 chapters after an append; got:\n%s", out)
	}
	for _, want := range []string{"Opening Credits", "Chapter One", "Chapter Two", "Appended"} {
		if !strings.Contains(out, want) {
			t.Errorf("chapter %q missing after append (old chapters must survive):\n%s", want, out)
		}
	}
}

func TestSetAddChapterDedupsExactDuplicates(t *testing.T) {
	// A repeated --add-chapter (same Start/Title) must be written once, not twice,
	// while two distinct titles at the same timestamp are both kept.
	file := copyFixture(t, sampleM4B)
	if _, _, code := runCLI(t, "set", file, "--clear-chapters",
		"--add-chapter", "0:00=Intro",
		"--add-chapter", "1:00=Verse", "--add-chapter", "1:00=Verse", // exact duplicate -> one
		"--add-chapter", "1:00=Bridge"); code != 0 { // same start, different title -> kept
		t.Fatalf("set --add-chapter exit != 0")
	}
	out, _, _ := runCLI(t, "dump", file)
	if !strings.Contains(out, "chapters (3)") {
		t.Errorf("expected 3 chapters (the duplicate Verse deduped, Bridge kept); got:\n%s", out)
	}
	if strings.Count(out, "Verse") != 1 {
		t.Errorf("the duplicate Verse should appear once; got:\n%s", out)
	}
	if !strings.Contains(out, "Bridge") {
		t.Errorf("a distinct title at the same start must be kept; got:\n%s", out)
	}
}

func TestSetClearChapters(t *testing.T) {
	file := copyFixture(t, sampleM4B)
	if _, _, code := runCLI(t, "set", file, "--clear-chapters"); code != 0 {
		t.Fatalf("set --clear-chapters exit = %d, want 0", code)
	}
	out, _, _ := runCLI(t, "dump", file)
	if strings.Contains(out, "chapters (") {
		t.Errorf("chapters survived --clear-chapters:\n%s", out)
	}
}

func TestPlanAddChapterShowsOperation(t *testing.T) {
	// The plan preview surfaces the chapter operation (no write performed).
	out, _, code := runCLI(t, "plan", sampleM4B, "--add-chapter", "0:10=Extra")
	if code != 0 {
		t.Fatalf("plan --add-chapter exit = %d, want 0", code)
	}
	if !strings.Contains(out, "chapters") {
		t.Errorf("plan preview does not mention chapters:\n%s", out)
	}
}

func TestSetAddChapterOnFLACErrors(t *testing.T) {
	// A chapter-incapable format hard-fails rather than silently dropping the chapters.
	file := copyFixture(t, sampleFLAC)
	_, errOut, code := runCLI(t, "set", file, "--add-chapter", "0:00=Intro")
	if code != 3 {
		t.Fatalf("set --add-chapter on FLAC exit = %d, want 3", code)
	}
	if !strings.Contains(errOut, "chapters cannot be written") {
		t.Errorf("stderr = %q, want it to explain the chapter refusal", errOut)
	}
}

func TestSetBadChapterTimestampIsUsageError(t *testing.T) {
	// A malformed timestamp fails as a usage error (exit 2) before any file is
	// touched - the validation happens at compile time.
	file := copyFixture(t, sampleM4B)
	_, _, code := runCLI(t, "set", file, "--add-chapter", "1:2:3:4=Bad")
	if code != 2 {
		t.Fatalf("set with a bad timestamp exit = %d, want 2 (usage)", code)
	}
}
