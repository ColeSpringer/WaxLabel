package core

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestWarningCodeStrings pins the machine codes (JSON and lint surface them) and checks the codes
// with a String case are one unbroken run, each with its own string.
func TestWarningCodeStrings(t *testing.T) {
	for code, want := range map[WarningCode]string{
		WarnTrailingLyrics3:   "trailing-lyrics3",
		WarnLeadingBytes:      "leading-bytes",
		WarnPageGap:           "page-gap",
		WarnMultiplexedStream: "multiplexed-stream",
	} {
		if got := code.String(); got != want {
			t.Errorf("WarningCode(%d).String() = %q, want %q", code, got, want)
		}
	}
	seen := map[string]WarningCode{}
	last := WarnUnknown
	for c := WarnUnknown + 1; c != WarnUnknown; c++ { // every value of the uint8, wrapping to 0
		s := c.String()
		if s == "unknown" {
			continue
		}
		if c != last+1 {
			t.Errorf("WarningCode(%d) has no String case but WarningCode(%d) does", last+1, c)
		}
		if prev, dup := seen[s]; dup {
			t.Errorf("WarningCode(%d) and WarningCode(%d) both render %q", prev, c, s)
		}
		seen[s], last = c, c
	}
}

// A tally lists kinds in the order they first appear and sums what each kind is counted again.
func TestTally(t *testing.T) {
	var tl Tally
	if tl.Counts() != nil {
		t.Errorf("an empty tally lists %v, want nil", tl.Counts())
	}
	tl.Add("b", 1)
	tl.Add("a", 2)
	tl.Add("b", 3)
	if got, want := tl.Counts(), []ContentCount{{Kind: "b", N: 4}, {Kind: "a", N: 2}}; !slices.Equal(got, want) {
		t.Errorf("counts %v, want %v", got, want)
	}
}

// A dropped-duplicate warning sums each kind of lost content across the duplicates, does not name a
// value the parsed set or the written one still holds, and lists at most a dozen losses, counting
// the rest, while Keys keeps every key.
func TestAppendDuplicateBlockDropped(t *testing.T) {
	before, written := tag.NewTagSet(), tag.NewTagSet()
	before.Set(tag.Title, "Old")
	written.Set(tag.Title, "New")
	dupTags := tag.NewTagSet()
	dupTags.Set(tag.Title, "Old")
	dupTags.Set(tag.Album, "Only Here")
	popm := ContentCount{Kind: `"POPM" frame(s)`, N: 1}
	ws := AppendDuplicateBlockDropped(nil, "ID3v2 tag", before, written, []DuplicateContent{
		{Tags: dupTags, Other: []ContentCount{popm}}, {Other: []ContentCount{popm}},
	})
	if len(ws) != 1 || !slices.Equal(ws[0].Keys, []tag.Key{tag.Album}) || !strings.Contains(ws[0].Message, `(ALBUM, 2 "POPM" frame(s))`) {
		t.Errorf("warnings %v, want ALBUM and two POPM frames named, the title's old value not", ws)
	}
	var many []ContentCount
	for i := range 20 {
		many = append(many, ContentCount{Kind: fmt.Sprintf("kind %d", i), N: 1})
	}
	ws = AppendDuplicateBlockDropped(nil, "ID3v2 tag", before, written, []DuplicateContent{{Other: many}})
	if len(ws) != 1 || strings.Contains(ws[0].Message, "kind 12") || !strings.Contains(ws[0].Message, "kind 11, 8 more)") {
		t.Errorf("warnings %v, want twelve kinds listed and the other eight counted", ws)
	}
}
