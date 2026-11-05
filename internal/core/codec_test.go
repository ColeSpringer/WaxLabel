package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/waxerr"
)

// TestCheckPlanResult: a planned result holding a different number of chapters, pictures or synced
// lyric sets than the edit asked for is refused unless the report names a discard; a count match
// passes whatever the contents.
func TestCheckPlanResult(t *testing.T) {
	two := []Chapter{{Title: "A"}, {Start: 1, Title: "B"}}
	pic := []Picture{{Type: PicFrontCover, Data: []byte{1}}}
	lyrics := []SyncedLyrics{{Lines: []SyncedLine{{Text: "x"}}}}
	discard := WriteReport{Warnings: []Warning{{Code: WarnValueDropped}, {Code: WarnChaptersUnsupported}}}
	note := WriteReport{Warnings: []Warning{{Code: WarnChapterTitleTruncated}}}

	for _, tc := range []struct {
		name           string
		edited, result *Media
		report         WriteReport
		refused        string // the item the refusal names; empty when the plan passes
	}{
		{"chapter lost", &Media{Chapters: two}, &Media{Chapters: two[:1]}, WriteReport{}, "chapters"},
		{"chapter lost under a note", &Media{Chapters: two}, &Media{Chapters: two[:1]}, note, "chapters"},
		{"chapter lost under a discard", &Media{Chapters: two}, &Media{Chapters: two[:1]}, discard, ""},
		{"picture gained", &Media{}, &Media{Pictures: pic}, WriteReport{}, "pictures"},
		{"synced lyrics lost", &Media{SyncedLyrics: lyrics}, &Media{}, WriteReport{}, "synced lyrics"},
		{"counts kept, contents changed", &Media{Chapters: two, Pictures: pic},
			&Media{Chapters: []Chapter{{Title: "X"}, {Title: "Y"}}, Pictures: []Picture{{}}}, WriteReport{}, ""},
		{"no result", &Media{Chapters: two}, nil, WriteReport{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckPlanResult(tc.edited, tc.result, tc.report)
			if tc.refused == "" {
				if err != nil {
					t.Fatalf("err = %v, want the plan accepted", err)
				}
				return
			}
			if !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "requested "+tc.refused) {
				t.Fatalf("err = %v, want ErrInvalidData naming the %s", err, tc.refused)
			}
		})
	}
}
