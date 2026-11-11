package vorbis

import (
	"fmt"
	"slices"
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
		{Name: "CHAPTER001", Value: "00:01:30.500"},
		{Name: "CHAPTER001NAME", Value: "Named"},
		{Name: "CHAPTER002", Value: "00:00:00.000"},
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

// TestVorbisChapterThousandFitsThreeDigits checks the exactly-1000 case: keys number
// from 0 (CHAPTER000..CHAPTER999) so ffmpeg's fixed 3-digit CHAPTER%03d parser reads all
// 1000, and 999 chapters keep the 1-based CHAPTER001 form.
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
		{Name: "CHAPTER0", Value: "00:00:00.000"}, // 0-based, 1 digit
		{Name: "CHAPTER0NAME", Value: "Zero"},
		{Name: "CHAPTER00001", Value: "00:00:05.000"}, // 5 digits
		{Name: "CHAPTER00001NAME", Value: "One"},
	}
	got := ProjectChapters(comments)
	if len(got) != 2 || got[0].Title != "Zero" || got[1].Title != "One" || got[1].Start != 5*time.Second {
		t.Fatalf("got %+v, want Zero@0 then One@5s", got)
	}
}

// TestVorbisChapterSortsByStart checks that chapters whose CHAPTERxxx index order
// disagrees with their start times project in start order, so a load->store round-trip
// is a no-op; equal-start chapters break ties by index.
func TestVorbisChapterSortsByStart(t *testing.T) {
	comments := []Comment{
		{Name: "CHAPTER001", Value: "00:00:10.000"}, // lower index, later time
		{Name: "CHAPTER001NAME", Value: "Late"},
		{Name: "CHAPTER002", Value: "00:00:05.000"}, // higher index, earlier time
		{Name: "CHAPTER002NAME", Value: "Early"},
		{Name: "CHAPTER003", Value: "00:00:05.000"}, // equal start to CHAPTER002: ties break by index
		{Name: "CHAPTER003NAME", Value: "EarlyTie"},
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
		{Name: "TITLE", Value: "Song"},
		{Name: "CHAPTER001", Value: "00:00:00.000"},
		{Name: "CHAPTER001NAME", Value: "Intro"},
	}
	ts, _ := Project(comments)
	for _, k := range ts.Keys() {
		if string(k) == "CHAPTER001" || string(k) == "CHAPTER001NAME" {
			t.Errorf("chapter comment %q leaked into the tag view", k)
		}
	}
}

// TestVorbisChapterMalformedNotAChapter checks that a CHAPTERxxx with an unparseable
// timestamp, or a stray CHAPTERxxxNAME with no timestamp, contributes no chapter but is
// still owned (see TestRebuildOwnsChapters).
func TestVorbisChapterMalformedNotAChapter(t *testing.T) {
	comments := []Comment{
		{Name: "CHAPTER001", Value: "not-a-time"},
		{Name: "CHAPTER002NAME", Value: "orphan title"},
	}
	if got := ProjectChapters(comments); len(got) != 0 {
		t.Errorf("malformed chapter comments yielded %+v, want none", got)
	}
}

// TestRebuildOwnsChapters checks that a chapter edit drops the source CHAPTERxxx comments
// and re-emits the edited set, while an unrelated edit preserves them verbatim, including
// a malformed one.
func TestRebuildOwnsChapters(t *testing.T) {
	orig := []Comment{
		{Name: "TITLE", Value: "Old"},
		{Name: "CHAPTER001", Value: "00:00:00.000"},
		{Name: "CHAPTER001NAME", Value: "Intro"},
		{Name: "CHAPTER002", Value: "garbage"}, // malformed: preserved on unrelated edits
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
// negative duration. The "2002000000000" seed is a regression for a bare-seconds value
// that overflowed time.Duration.
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

// TestOwnedCommentWarnings: an owned comment that no projector reads is reported, whichever way it
// goes unread; a well-formed set is not.
func TestOwnedCommentWarnings(t *testing.T) {
	for name, tc := range map[string]struct {
		comments []Comment
		mentions string
	}{
		"untimed synced lyrics": {[]Comment{{Name: "SYNCEDLYRICS", Value: "just words"}}, "SYNCEDLYRICS"},
		"a second synced lyrics comment": {[]Comment{
			{Name: "SYNCEDLYRICS", Value: "[00:01.00]a"}, {Name: "syncedlyrics", Value: "[00:02.00]b"},
		}, "SYNCEDLYRICS"},
		"a time that does not parse": {[]Comment{
			{Name: "CHAPTER001", Value: "bogus"}, {Name: "CHAPTER001NAME", Value: "Intro"},
		}, "CHAPTER001"},
		"a repeated chapter": {[]Comment{
			{Name: "CHAPTER001", Value: "00:00:00.000"}, {Name: "CHAPTER001", Value: "00:00:05.000"},
		}, "CHAPTER001"},
		"a title with no time": {[]Comment{{Name: "CHAPTER002NAME", Value: "Stray"}}, "CHAPTER002NAME"},
	} {
		ws := ProjectOwned(tc.comments).Warnings
		if len(ws) != 1 || ws[0].Code != core.WarnMalformedTagEntry || !strings.Contains(strings.ToUpper(ws[0].Message), tc.mentions) {
			t.Errorf("%s: warnings = %+v, want one malformed-tag-entry naming %s", name, ws, tc.mentions)
		}
	}
	if ws := ProjectOwned([]Comment{
		{Name: "TITLE", Value: "x"}, {Name: "CHAPTER001", Value: "00:00:00.000"}, {Name: "CHAPTER001NAME", Value: "Intro"},
		{Name: "SYNCEDLYRICS", Value: "[00:01.00]a"}, {Value: "CHAPTER009", Unseparated: true},
	}).Warnings; len(ws) != 0 {
		t.Errorf("a well-formed list warned: %+v", ws)
	}
}

// TestPlanUnread: an explicit clear of the chapters, synced lyrics or pictures takes the unread
// comments of that kind with it and says so, a cleared name takes those under that name and no
// other, a replacement warns, and nothing else touches them.
func TestPlanUnread(t *testing.T) {
	orig := []Comment{
		{Name: "TITLE", Value: "x"},
		{Name: "CHAPTER001", Value: "bogus"}, {Name: "CHAPTER001NAME", Value: "Intro"},
		{Name: "chapter1", Value: "also bogus"},
		{Name: "CHAPTER002", Value: "00:00:05.000"},
		{Name: "SYNCEDLYRICS", Value: "just words"},
		{Name: "metadata_block_picture", Value: "not~base64"}, // a cover that decodes is not in the list
	}
	without := func(names ...string) []Comment {
		var out []Comment
		for _, cm := range orig {
			if !slices.Contains(names, cm.Name) {
				out = append(out, cm)
			}
		}
		return out
	}
	tags, _ := Project(orig)
	touched := func(keys ...tag.Key) core.WriteOptions {
		opts := core.WriteOptions{Touched: map[tag.Key]bool{}}
		for _, k := range keys {
			opts.Touched[k] = true
		}
		return opts
	}
	withLyricsKey := tags.Clone()
	withLyricsKey.Set("SYNCEDLYRICS", "v")

	for name, tc := range map[string]struct {
		opts              core.WriteOptions
		edited            tag.TagSet
		blocks            int  // PICTURE blocks that did not decode
		chapters, lyrics  bool // changed, as the caller found them
		kept              []Comment
		wantChapters      bool
		wantLyrics        bool
		wantBlocks        bool
		ops, droppedWarns int
	}{
		"an unrelated edit":                 {kept: orig, edited: tags, blocks: 1},
		"the chapters cleared":              {opts: core.WriteOptions{ChaptersCleared: true}, edited: tags, kept: orig, wantChapters: true, ops: 1},
		"the synced lyrics cleared":         {opts: core.WriteOptions{SyncedLyricsCleared: true}, edited: tags, kept: orig, wantLyrics: true, ops: 1},
		"the pictures cleared":              {opts: core.WriteOptions{PicturesCleared: true}, edited: tags, kept: without("metadata_block_picture"), ops: 1},
		"the pictures cleared, with blocks": {opts: core.WriteOptions{PicturesCleared: true}, edited: tags, blocks: 2, kept: without("metadata_block_picture"), wantBlocks: true, ops: 2},
		"a chapter's name cleared":          {opts: touched("CHAPTER001"), edited: tags, kept: without("CHAPTER001"), ops: 1},
		"the name in another case":          {opts: touched("CHAPTER1"), edited: tags, kept: without("chapter1"), ops: 1},
		"the number spelled another way":    {opts: touched("CHAPTER0001", "CHAPTER01NAME"), edited: tags, kept: orig},
		"a chapter title's name cleared":    {opts: touched("CHAPTER001NAME"), edited: tags, kept: without("CHAPTER001NAME"), ops: 1},
		"a readable chapter's name cleared": {opts: touched("CHAPTER002"), edited: tags, kept: orig},
		"the lyrics name cleared":           {opts: touched("SYNCEDLYRICS"), edited: tags, kept: without("SYNCEDLYRICS"), ops: 1},
		"the lyrics name set":               {opts: touched("SYNCEDLYRICS"), edited: withLyricsKey, kept: orig},
		"the picture name cleared":          {opts: touched("METADATA_BLOCK_PICTURE"), edited: tags, blocks: 1, kept: without("metadata_block_picture"), ops: 1},
		"the chapters replaced":             {edited: tags, chapters: true, kept: orig, wantChapters: true, droppedWarns: 1},
		"the synced lyrics replaced":        {edited: tags, lyrics: true, kept: orig, wantLyrics: true, droppedWarns: 1},
	} {
		got := PlanUnread(orig, tc.blocks, tc.edited, tc.opts, tc.chapters, tc.lyrics)
		if !slices.Equal(got.Comments, tc.kept) {
			t.Errorf("%s: kept %v, want %v", name, got.Comments, tc.kept)
		}
		if got.ChaptersChanged != tc.wantChapters || got.LyricsChanged != tc.wantLyrics || got.DropBlocks != tc.wantBlocks {
			t.Errorf("%s: chapters changed %v, lyrics changed %v, blocks dropped %v; want %v, %v, %v", name,
				got.ChaptersChanged, got.LyricsChanged, got.DropBlocks, tc.wantChapters, tc.wantLyrics, tc.wantBlocks)
		}
		ws := got.Warnings
		if len(got.Ops) != tc.ops || len(ws) != tc.droppedWarns || len(ws) == 1 && ws[0].Code != core.WarnMalformedTagEntryDropped {
			t.Errorf("%s: ops %q, warnings %+v; want %d op(s), %d malformed-tag-entry-dropped", name, got.Ops, ws, tc.ops, tc.droppedWarns)
		}
		if got.Removes() != (tc.ops+tc.droppedWarns > 0) {
			t.Errorf("%s: Removes() = %v with %d op(s) and %d warning(s)", name, got.Removes(), tc.ops, tc.droppedWarns)
		}
	}
}

// TestUnreadFollowsTheProjectors: the comments reported as unread are exactly the owned comments
// that the chapters and synced lyrics read back do not come from, so removing them changes
// neither.
func TestUnreadFollowsTheProjectors(t *testing.T) {
	comments := []Comment{
		{Name: "CHAPTER01", Value: "00:00:01.000"}, {Name: "chapter1", Value: "00:00:02.000"}, {Name: "CHAPTER1NAME", Value: "old"},
		{Name: "Chapter001Name", Value: "kept"}, {Name: "CHAPTER2", Value: "nope"}, {Name: "CHAPTER2", Value: "00:00:09.000"},
		{Name: "CHAPTER3NAME", Value: "stray"}, {Name: "CHAPTER4", Value: "00:00:04.000"}, {Name: "CHAPTER4", Value: "later, unreadable"},
		{Name: "SYNCEDLYRICS", Value: "no time"}, {Name: "SyncedLyrics", Value: "[00:01.00]one"}, {Name: "SYNCEDLYRICS", Value: "[00:02.00]two"},
		{Name: "TITLE", Value: "x"}, {Value: "CHAPTER9", Unseparated: true},
	}
	r := readOwned(comments)
	var read []Comment
	for i, cm := range comments {
		if r.unread[i] == unreadNone {
			read = append(read, cm)
		}
	}
	if r.n[unreadChapter] != 5 || r.n[unreadLyrics] != 2 || len(read) != len(comments)-7 {
		t.Errorf("unread: %d chapter and %d lyrics comments of %d; want 5 and 2", r.n[unreadChapter], r.n[unreadLyrics], len(comments))
	}
	if !slices.Equal(ProjectChapters(read), ProjectChapters(comments)) || len(ProjectChapters(read)) != 3 {
		t.Errorf("chapters without the unread comments = %+v, with them %+v", ProjectChapters(read), ProjectChapters(comments))
	}
	if a, b := ProjectSyncedLyrics(read), ProjectSyncedLyrics(comments); len(a) != 1 || len(b) != 1 || !slices.Equal(a[0].Lines, b[0].Lines) {
		t.Errorf("synced lyrics without the unread comments = %+v, with them %+v", a, b)
	}
}

// TestRebuildKeepsEachChapterItsOwnTitleBytes: titles that read alike are told apart by the
// chapter they title, whatever order the file holds them in; a title the reader passed over lends
// its bytes to nothing; chapters moved together keep theirs in order.
func TestRebuildKeepsEachChapterItsOwnTitleBytes(t *testing.T) {
	titles := func(cs []Comment) (out []string) {
		for _, cm := range cs {
			if _, isTitle, _ := parseChapterName(cm.Name); isTitle {
				out = append(out, cm.Value)
			}
		}
		return out
	}
	added := core.Chapter{Start: 2 * time.Minute, Title: "New"}

	outOfOrder := []Comment{
		{Name: "CHAPTER001", Value: "00:01:00.000"}, {Name: "CHAPTER001NAME", Value: "Caf\xe9"},
		{Name: "CHAPTER002", Value: "00:00:00.000"}, {Name: "CHAPTER002NAME", Value: "Caf\xff"},
	}
	got, _ := Rebuild(outOfOrder, tag.NewTagSet(), nil, append(ProjectChapters(outOfOrder), added), true, nil, false)
	if want := []string{"Caf\xff", "Caf\xe9", "New"}; !slices.Equal(titles(got), want) {
		t.Errorf("stored out of time order: titles %q, want %q", titles(got), want)
	}

	repeated := []Comment{
		{Name: "CHAPTER001", Value: "00:00:00.000"}, {Name: "CHAPTER001NAME", Value: "Caf\xe8"}, {Name: "CHAPTER001NAME", Value: "Caf\xe9"},
	}
	got, _ = Rebuild(repeated, tag.NewTagSet(), nil, append(ProjectChapters(repeated), added), true, nil, false)
	if want := []string{"Caf\xe9", "New"}; !slices.Equal(titles(got), want) {
		t.Errorf("a title passed over: titles %q, want %q", titles(got), want)
	}

	moved := ProjectChapters(outOfOrder)
	for i := range moved {
		moved[i].Start += 3 * time.Second
	}
	got, _ = Rebuild(outOfOrder, tag.NewTagSet(), nil, moved, true, nil, false)
	if want := []string{"Caf\xff", "Caf\xe9"}; !slices.Equal(titles(got), want) {
		t.Errorf("chapters moved together: titles %q, want %q", titles(got), want)
	}
}

// TestRebuildKeepsInvalidUTF8ChapterTitle: a chapter edit that keeps a title read with U+FFFD in
// place of invalid bytes writes the title back as stored; a retitled chapter takes the new title.
func TestRebuildKeepsInvalidUTF8ChapterTitle(t *testing.T) {
	orig := []Comment{
		{Name: "CHAPTER001", Value: "00:00:00.000"}, {Name: "CHAPTER001NAME", Value: "Intr\xf6"},
		{Name: "CHAPTER002", Value: "00:00:05.000"}, {Name: "CHAPTER002NAME", Value: "Vers\xe9"},
	}
	chs := ProjectChapters(orig)
	if len(chs) != 2 || chs[0].Title != "Intr�" {
		t.Fatalf("setup: chapters = %+v", chs)
	}
	chs[1].Title = "Verse"
	chs = append(chs, core.Chapter{Start: 9 * time.Second, Title: "Outro"})
	got, _ := Rebuild(orig, tag.NewTagSet(), nil, chs, true, nil, false)
	want := []Comment{
		{Name: "CHAPTER001", Value: "00:00:00.000"}, {Name: "CHAPTER001NAME", Value: "Intr\xf6"},
		{Name: "CHAPTER002", Value: "00:00:05.000"}, {Name: "CHAPTER002NAME", Value: "Verse"},
		{Name: "CHAPTER003", Value: "00:00:09.000"}, {Name: "CHAPTER003NAME", Value: "Outro"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("rebuild = %q, want %q", fmt.Sprint(got), fmt.Sprint(want))
	}
}
