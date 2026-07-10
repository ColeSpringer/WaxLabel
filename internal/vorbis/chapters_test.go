package vorbis

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestVorbisChapterRoundTrip checks decode(encode(x)) preserves start and title (the only
// fields CHAPTERxxx stores).
func TestVorbisChapterRoundTrip(t *testing.T) {
	in := []core.Chapter{
		{Start: 0, Title: "Intro"},
		{Start: 1500 * time.Millisecond, Title: "Verse"},
		{Start: 3*time.Minute + 7500*time.Millisecond, Title: "Bridge"},
	}
	cc, _ := chapterComments(in)
	got := ProjectChapters(cc)
	if len(got) != len(in) {
		t.Fatalf("got %d chapters, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i].Start != in[i].Start || got[i].Title != in[i].Title {
			t.Errorf("chapter %d = {%v %q}, want {%v %q}", i, got[i].Start, got[i].Title, in[i].Start, in[i].Title)
		}
	}
}

// TestVorbisChapterEmitsCommonForm checks the writer emits 1-based, 3-digit numbers and a
// HH:MM:SS.mmm timestamp, with no CHAPTERxxxNAME for a titleless chapter.
func TestVorbisChapterEmitsCommonForm(t *testing.T) {
	got, _ := chapterComments([]core.Chapter{
		{Start: 90500 * time.Millisecond, Title: "Named"},
		{Start: 0}, // titleless: no CHAPTER002NAME
	})
	want := []Comment{
		{"CHAPTER001", "00:01:30.500"},
		{"CHAPTER001NAME", "Named"},
		{"CHAPTER002", "00:00:00.000"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("comment %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestVorbisChapterThousandFitsThreeDigits checks the exactly-1000 case: ffmpeg/ffprobe parse
// CHAPTERxxx with a fixed 3-digit key (CHAPTER%03d), so a 1-based CHAPTER1000 (4 digits) would be
// unreadable and drop chapters there. Numbering from 0 keeps every key 3-digit
// (CHAPTER000..CHAPTER999), which ffmpeg reads in full, while WaxLabel still round-trips all 1000.
// The boundary is pinned too: 999 chapters keep the common 1-based CHAPTER001 form.
func TestVorbisChapterThousandFitsThreeDigits(t *testing.T) {
	// Boundary: 999 chapters stay 1-based (CHAPTER001..), the common foobar2000 form.
	if cc999, _ := chapterComments(make([]core.Chapter, 999)); cc999[0].Name != "CHAPTER001" {
		t.Errorf("999 chapters first key = %q, want the 1-based CHAPTER001 form", cc999[0].Name)
	}

	const n = 1000
	in := make([]core.Chapter, n)
	for i := range in {
		in[i] = core.Chapter{Start: time.Duration(i) * time.Millisecond, Title: fmt.Sprintf("C%d", i+1)}
	}
	cc, overflow := chapterComments(in)
	if overflow {
		t.Fatalf("unexpected overflow authoring %d chapters", n)
	}
	// Every CHAPTERxxx start key is CHAPTER + exactly 3 digits, numbered 0-based so ffmpeg's
	// fixed 3-digit parser reads all 1000.
	const wantLen = len(chapterNamePrefix) + 3
	seen := map[string]bool{}
	for _, c := range cc {
		if strings.HasSuffix(c.Name, "NAME") {
			continue
		}
		if len(c.Name) != wantLen {
			t.Errorf("chapter key %q width = %d, want %d (CHAPTER + 3 digits)", c.Name, len(c.Name), wantLen)
		}
		seen[c.Name] = true
	}
	if !seen["CHAPTER000"] || !seen["CHAPTER999"] {
		t.Errorf("want 0-based keys CHAPTER000..CHAPTER999; present 000=%v 999=%v", seen["CHAPTER000"], seen["CHAPTER999"])
	}
	if got := ProjectChapters(cc); len(got) != n {
		t.Fatalf("round-trip projected %d chapters, want %d", len(got), n)
	}
}

// TestVorbisChapterAcceptsAnyDigitsAndBase checks the reader accepts arbitrary digit counts
// and 0- or 1-based numbering, ordering by the numeric index.
func TestVorbisChapterAcceptsAnyDigitsAndBase(t *testing.T) {
	comments := []Comment{
		{"CHAPTER0", "00:00:00.000"}, // 0-based, 1 digit
		{"CHAPTER0NAME", "Zero"},
		{"CHAPTER00001", "00:00:05.000"}, // 5 digits
		{"CHAPTER00001NAME", "One"},
	}
	got := ProjectChapters(comments)
	if len(got) != 2 || got[0].Title != "Zero" || got[1].Title != "One" || got[1].Start != 5*time.Second {
		t.Fatalf("got %+v, want Zero@0 then One@5s", got)
	}
}

// TestVorbisChapterSortsByStart checks that chapters whose CHAPTERxxx index order disagrees
// with their start times project in start-time order, so a load->store round-trip is a no-op
// even for an out-of-order source. The prior sort.Ints(order) kept index order (the 10s
// chapter ahead of the 5s one). Equal-start chapters break ties by index (stable sort).
func TestVorbisChapterSortsByStart(t *testing.T) {
	comments := []Comment{
		{"CHAPTER001", "00:00:10.000"}, // lower index, later time
		{"CHAPTER001NAME", "Late"},
		{"CHAPTER002", "00:00:05.000"}, // higher index, earlier time
		{"CHAPTER002NAME", "Early"},
		{"CHAPTER003", "00:00:05.000"}, // equal start to CHAPTER002: ties break by index
		{"CHAPTER003NAME", "EarlyTie"},
	}
	got := ProjectChapters(comments)
	if len(got) != 3 {
		t.Fatalf("got %d chapters, want 3", len(got))
	}
	want := []core.Chapter{
		{Start: 5 * time.Second, Title: "Early"},
		{Start: 5 * time.Second, Title: "EarlyTie"},
		{Start: 10 * time.Second, Title: "Late"},
	}
	for i := range want {
		if got[i].Start != want[i].Start || got[i].Title != want[i].Title {
			t.Errorf("chapter %d = {%v %q}, want {%v %q}", i, got[i].Start, got[i].Title, want[i].Start, want[i].Title)
		}
	}
}

// TestVorbisChapterFractionScaling checks the fractional second scales by its digit count
// (".5" == 500 ms, ".05" == 50 ms) rather than being read as a literal millisecond count.
func TestVorbisChapterFractionScaling(t *testing.T) {
	cases := map[string]time.Duration{
		"00:00:01.5":   1500 * time.Millisecond,
		"00:00:01.05":  1050 * time.Millisecond,
		"00:00:01.005": 1005 * time.Millisecond,
		"0:30":         30 * time.Second, // MM:SS, no hour, no fraction
		"90":           90 * time.Second, // bare seconds
	}
	for s, want := range cases {
		got, ok := parseChapterTime(s)
		if !ok || got != want {
			t.Errorf("parseChapterTime(%q) = %v, %v; want %v", s, got, ok, want)
		}
	}
}

// TestVorbisChapterOwnership checks owned CHAPTERxxx comments do not leak into the generic
// tag projection (they are chapters, not custom tag fields).
func TestVorbisChapterOwnership(t *testing.T) {
	comments := []Comment{
		{"TITLE", "Song"},
		{"CHAPTER001", "00:00:00.000"},
		{"CHAPTER001NAME", "Intro"},
	}
	ts, _ := Project(comments)
	for _, k := range ts.Keys() {
		if string(k) == "CHAPTER001" || string(k) == "CHAPTER001NAME" {
			t.Errorf("chapter comment %q leaked into the tag view", k)
		}
	}
}

// TestVorbisChapterMalformedNotAChapter checks a CHAPTERxxx with an unparseable timestamp,
// or a stray CHAPTERxxxNAME with no timestamp, contributes no chapter (but is still owned -
// see TestRebuildPreservesUnrelatedChapter).
func TestVorbisChapterMalformedNotAChapter(t *testing.T) {
	comments := []Comment{
		{"CHAPTER001", "not-a-time"},
		{"CHAPTER002NAME", "orphan title"},
	}
	if got := ProjectChapters(comments); len(got) != 0 {
		t.Errorf("malformed chapter comments yielded %+v, want none", got)
	}
}

// TestRebuildOwnsChapters checks Rebuild's ownership: on a chapter edit the source
// CHAPTERxxx are dropped and the edited set re-emitted; on an unrelated edit they are
// preserved verbatim, including a malformed one.
func TestRebuildOwnsChapters(t *testing.T) {
	orig := []Comment{
		{"TITLE", "Old"},
		{"CHAPTER001", "00:00:00.000"},
		{"CHAPTER001NAME", "Intro"},
		{"CHAPTER002", "garbage"}, // malformed: preserved on unrelated edits
	}

	// Unrelated (title-only) edit: every CHAPTERxxx comment is preserved verbatim.
	base := mkTagSet("TITLE", "Old")
	edited := mkTagSet("TITLE", "New")
	got, _ := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
	if !hasComment(got, "CHAPTER001", "00:00:00.000") ||
		!hasComment(got, "CHAPTER001NAME", "Intro") ||
		!hasComment(got, "CHAPTER002", "garbage") {
		t.Errorf("unrelated edit did not preserve chapter comments verbatim: %v", got)
	}

	// Chapter edit: old CHAPTERxxx dropped, the new single chapter re-emitted.
	got, _ = Rebuild(orig, base, DiffKeys(base, base), []core.Chapter{{Start: time.Second, Title: "Only"}}, true, nil, false)
	if hasComment(got, "CHAPTER002", "garbage") {
		t.Error("a chapter edit must drop the source CHAPTERxxx comments, including the malformed one")
	}
	chs := ProjectChapters(got)
	if len(chs) != 1 || chs[0].Title != "Only" || chs[0].Start != time.Second {
		t.Errorf("re-emitted chapters = %+v, want one Only@1s", chs)
	}
}

func mkTagSet(name, value string) tag.TagSet {
	ts := tag.NewTagSet()
	ts.Set(tag.Key(name), value)
	return ts
}

func hasComment(cs []Comment, name, value string) bool {
	for _, c := range cs {
		if c.Name == name && c.Value == value {
			return true
		}
	}
	return false
}

// FuzzParseChapterTime asserts the CHAPTERxxx timestamp parser never panics or returns a
// negative duration. The "2002000000000" seed is a regression: a huge bare-seconds value
// once overflowed time.Duration into a negative span (now rejected as not-a-chapter).
func FuzzParseChapterTime(f *testing.F) {
	for _, s := range []string{"", ":", "::", "00:00:00.000", "1:2:3:4", "....", "99:99:99.99999", "-1", "00:00:01.5", "  12:34  ", "2002000000000"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if d, ok := parseChapterTime(s); ok && d < 0 {
			t.Errorf("parseChapterTime(%q) = negative %v", s, d)
		}
	})
}

// FuzzVorbisProjectChapters asserts the CHAPTERxxx projection never panics and never yields
// an invalid-UTF-8 title.
func FuzzVorbisProjectChapters(f *testing.F) {
	f.Add("CHAPTER001", "00:00:00.000")
	f.Add("CHAPTER1NAME", "title")
	f.Add("CHAPTERZ", "garbage")
	f.Add("chapter007name", "lower")
	f.Fuzz(func(t *testing.T, name, value string) {
		for _, c := range ProjectChapters([]Comment{{Name: name, Value: value}, {Name: name + "NAME", Value: value}}) {
			if !utf8.ValidString(c.Title) {
				t.Errorf("chapter title not valid UTF-8: %q", c.Title)
			}
		}
	})
}
