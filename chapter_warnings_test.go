package waxlabel

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// chap builds a Chapter with only Start set.
func chap(start time.Duration) core.Chapter { return core.Chapter{Start: start} }

// TestAppendChapterWarnings covers duration-0, duplicate-start dedup, and
// scoping to new chapters (public API fixtures miss these).
func TestAppendChapterWarnings(t *testing.T) {
	const dur = 10 * time.Second

	// New chapter past EOF flagged; in-bounds not.
	ws := appendChapterWarnings(nil, []core.Chapter{chap(time.Second), chap(dur + time.Hour)}, nil, dur)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 1 {
		t.Errorf("past-duration warnings = %d, want 1; got %v", n, ws)
	}

	// All in-bounds: no past-duration warning.
	ws = appendChapterWarnings(nil, []core.Chapter{chap(time.Second), chap(5 * time.Second)}, nil, dur)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 0 {
		t.Errorf("in-bounds chapters should not warn, got %d: %v", n, ws)
	}

	// Duration 0: skip past-duration check.
	ws = appendChapterWarnings(nil, []core.Chapter{chap(time.Second), chap(time.Hour)}, nil, 0)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 0 {
		t.Errorf("duration-0 should suppress past-duration warnings, got %d: %v", n, ws)
	}

	// Equal-start run warns once, not per adjacent pair.
	ws = appendChapterWarnings(nil, []core.Chapter{
		chap(time.Second), chap(time.Second), chap(time.Second), chap(2 * time.Second),
	}, nil, dur)
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 1 {
		t.Errorf("a run of equal starts should warn once, got %d: %v", n, ws)
	}

	// Two distinct collisions warn once each.
	ws = appendChapterWarnings(nil, []core.Chapter{
		chap(time.Second), chap(time.Second), chap(2 * time.Second), chap(2 * time.Second),
	}, nil, dur)
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 2 {
		t.Errorf("two distinct collisions should warn twice, got %d: %v", n, ws)
	}

	// Pre-existing past-duration / duplicate starts not flagged on --add-chapter.
	base := []core.Chapter{chap(dur + time.Hour), chap(2 * time.Second), chap(2 * time.Second)}
	merged := append(append([]core.Chapter{}, base...), chap(time.Second)) // + one valid new chapter
	ws = appendChapterWarnings(nil, merged, base, dur)
	if n := countWarn(ws, core.WarnChapterPastDuration); n != 0 {
		t.Errorf("a pre-existing past-duration chapter should not be flagged, got %d: %v", n, ws)
	}
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 0 {
		t.Errorf("a pre-existing duplicate-start pair should not be flagged, got %d: %v", n, ws)
	}

	// New chapter colliding with pre-existing start is flagged.
	base = []core.Chapter{{Start: 3 * time.Second, Title: "Old"}}
	merged = []core.Chapter{{Start: 3 * time.Second, Title: "Old"}, {Start: 3 * time.Second, Title: "New"}}
	ws = appendChapterWarnings(nil, merged, base, dur)
	if n := countWarn(ws, core.WarnDuplicateChapter); n != 1 {
		t.Errorf("a new chapter colliding with a pre-existing start should warn once, got %d: %v", n, ws)
	}
}

func countWarn(ws []core.Warning, code core.WarningCode) int {
	n := 0
	for _, w := range ws {
		if w.Code == code {
			n++
		}
	}
	return n
}

// A chapter an edit authors is refused when it starts before zero or ends before it starts. An
// end at the start is the open end.
func TestPrepareRefusesInvalidChapterTimes(t *testing.T) {
	const s = time.Second
	for _, fixture := range []string{"testdata/notags.mka", "testdata/notags.mp3", "testdata/notags.flac", "testdata/notags.m4a"} {
		doc, err := ParseFile(context.Background(), fixture)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name    string
			ch      Chapter
			invalid bool
		}{
			{"a start before zero", Chapter{Start: -5 * s, Title: "x"}, true},
			{"an end before the start", Chapter{Start: 2 * s, End: s, Title: "x"}, true},
			{"an end before zero", Chapter{Start: 0, End: -s, Title: "x"}, true},
			{"an end at the start", Chapter{Start: s, End: s, Title: "x"}, false},
			{"no end", Chapter{Start: s, Title: "x"}, false},
		} {
			_, err := doc.Edit().SetChapters(Chapter{Start: 0, Title: "first"}, tc.ch).Prepare()
			if tc.invalid != errors.Is(err, waxerr.ErrInvalidData) || (!tc.invalid && err != nil) {
				t.Errorf("%s, %s: Prepare = %v, want invalid %v", fixture, tc.name, err, tc.invalid)
			}
		}
	}
}

// An end at the start is written as no end.
func TestChapterEndingAtItsStartIsOpen(t *testing.T) {
	ctx := context.Background()
	doc, err := ParseFile(ctx, "testdata/notags.mka")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := doc.Edit().SetChapters(Chapter{Start: time.Second, End: time.Second, Title: "x"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "open.mka")
	if _, _, err := plan.Execute(ctx, SaveAsFile(path)); err != nil {
		t.Fatal(err)
	}
	got, err := ParseFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if chs := got.Chapters(); len(chs) != 1 || chs[0].Start != time.Second || chs[0].End != 0 {
		t.Errorf("chapters read back as %+v, want one open chapter at 1s", chs)
	}
}

// A chapter a file already holds is not judged: one whose CHAP ends before it starts stays through
// an edit that adds a chapter, and copies to another file.
func TestInvalidChapterTimesOfTheFileAreNotRefused(t *testing.T) {
	ctx := context.Background()
	audio, err := os.ReadFile("testdata/notags.mp3")
	if err != nil {
		t.Fatal(err)
	}
	chap := slices.Concat([]byte("ch0\x00"), testfile.BE32(2000), testfile.BE32(1000), bytes.Repeat([]byte{0xFF}, 8))
	src, err := Parse(ctx, BytesSource(slices.Concat(testfile.ID3v2(3, testfile.ID3Frame(3, "CHAP", chap)), audio)))
	if err != nil {
		t.Fatal(err)
	}
	held := src.Chapters()
	if len(held) != 1 || held[0].Start != 2*time.Second || held[0].End != time.Second {
		t.Fatalf("chapters = %+v, want the one that ends before it starts", held)
	}
	if _, err := src.Edit().SetChapters(append(held, Chapter{Start: 0, Title: "new"})...).Prepare(); err != nil {
		t.Errorf("adding a chapter beside it: %v", err)
	}
	dst, err := ParseFile(ctx, "testdata/notags.mka")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.PrepareTransfer(dst); err != nil {
		t.Errorf("copying it: %v", err)
	}
	if _, _, err := src.Transfer().SetChapters(held...).Prepare(dst); err != nil {
		t.Errorf("copying it as the chapters set on the transfer: %v", err)
	}
}

// Chapters set on a Transfer are authored as an edit's are, and judged the same way.
func TestTransferRefusesInvalidChapterTimes(t *testing.T) {
	ctx := context.Background()
	src, err := ParseFile(ctx, "testdata/notags.flac")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := ParseFile(ctx, "testdata/notags.mka")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		ch      Chapter
		invalid bool
	}{
		{"a start before zero", Chapter{Start: -5 * time.Second}, true},
		{"an end before the start", Chapter{Start: 2 * time.Second, End: time.Second}, true},
		{"an end at the start", Chapter{Start: time.Second, End: time.Second}, false},
	} {
		carry := src.Transfer().SetChapters(Chapter{Start: 0}, tc.ch)
		if _, _, err := carry.Prepare(dst); tc.invalid != errors.Is(err, waxerr.ErrInvalidData) || (!tc.invalid && err != nil) {
			t.Errorf("%s: Prepare = %v, want invalid %v", tc.name, err, tc.invalid)
		}
		if _, err := carry.Plan(FormatMatroska); tc.invalid != errors.Is(err, waxerr.ErrInvalidData) || (!tc.invalid && err != nil) {
			t.Errorf("%s: Plan = %v, want invalid %v", tc.name, err, tc.invalid)
		}
	}
}

// The times of a chapter list are judged where the list would be written: one a format cannot
// store is refused or dropped for that, and a transfer to a format nothing writes for that.
func TestChapterTimesAreJudgedWhereTheListIsWritten(t *testing.T) {
	ctx := context.Background()
	bad := Chapter{Start: -time.Second, Title: "x"}
	wv, err := ParseFile(ctx, "testdata/notags.wv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wv.Edit().SetChapters(bad).Prepare(); !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("a list WavPack cannot store: Prepare = %v, want unsupported-tag", err)
	}
	plan, err := wv.Edit().Set(tag.Title, "T").SetChapters(bad).Prepare(WithAllowUnsupportedDrop())
	if err != nil || countWarn(plan.Report().Warnings, core.WarnChaptersUnsupported) != 1 {
		t.Errorf("the same list under the drop option: %v, want the title planned and the list dropped with a warning", err)
	}
	src, err := ParseFile(ctx, "testdata/sample.flac")
	if err != nil {
		t.Fatal(err)
	}
	carry := src.Transfer().SetChapters(bad)
	if _, _, err := carry.Prepare(wv); err != nil {
		t.Errorf("a transfer to a file that stores no chapters: %v, want the list dropped", err)
	}
	if _, err := carry.Plan(FormatWavPack); err != nil {
		t.Errorf("a transfer planned for a format that stores no chapters: %v, want the list dropped", err)
	}
	if _, err := carry.Plan(Format(250)); !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("a transfer planned for no format: %v, want unsupported-format", err)
	}
}
