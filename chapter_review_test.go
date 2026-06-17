package waxlabel_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// execChapters runs an edit to bytes and returns the in-memory result Document
// plus a fresh parse of the written bytes, the pair that must agree.
func execChapters(t *testing.T, src []byte, edit func(*wl.Editor) *wl.Editor) (result, reparse *wl.Document) {
	t.Helper()
	plan, err := edit(mustParseBytes(t, src).Edit()).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	res, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(src)))
	if err != nil {
		t.Fatal(err)
	}
	return res, mustParseBytes(t, w.b)
}

func chapterWarn(doc *wl.Document, code wl.WarningCode) bool {
	for _, w := range doc.Warnings() {
		if w.Code == code {
			return true
		}
	}
	return false
}

// TestMP4ChapterEditResultMatchesReparse is the regression for the headline
// finding: the in-memory result of a chapter edit must equal a fresh parse of its
// own bytes — same chapters (a preserved QuickTime track still wins), same
// source-conflict warning, and same ftyp brand in the native view.
func TestMP4ChapterEditResultMatchesReparse(t *testing.T) {
	data := mp4QTFile([]int{0, 3000, 6000}, []string{"A", "B", "C"})
	res, re := execChapters(t, data, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(wl.Chapter{Start: 0, Title: "NEW1"}, wl.Chapter{Start: 4 * time.Second, Title: "NEW2"})
	})
	if !equalChapterLists(res.Chapters(), re.Chapters()) {
		t.Errorf("result chapters %+v != reparse %+v", res.Chapters(), re.Chapters())
	}
	if chapterWarn(res, wl.WarnChapterSourceConflict) != chapterWarn(re, wl.WarnChapterSourceConflict) {
		t.Errorf("conflict warning differs: result=%v reparse=%v", res.Warnings(), re.Warnings())
	}
	if chapterWarn(re, wl.WarnChapterSourceConflict) {
		t.Error("a rebuilt QuickTime track agrees with the chpl, so there must be no source conflict")
	}
}

func TestMP4ChapterEndFilledInResult(t *testing.T) {
	// chpl carries no End; the result must fill it from the next start exactly as a
	// reparse does (no End=0 where a reparse shows a real end).
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	res, re := execChapters(t, data, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(wl.Chapter{Start: 0, Title: "A"}, wl.Chapter{Start: 4 * time.Second, Title: "B"})
	})
	if res.Chapters()[0].End != re.Chapters()[0].End || res.Chapters()[0].End != 4*time.Second {
		t.Errorf("result End=%v reparse End=%v, want 4s", res.Chapters()[0].End, re.Chapters()[0].End)
	}
}

func TestMP4BrandPreservedAcrossEdit(t *testing.T) {
	src := readFixture(t, sampleM4B)
	res, re := execChapters(t, src, func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "X") })
	brand := func(d *wl.Document) string {
		for _, e := range d.Native().Describe() {
			if e.Kind == "ftyp" {
				return e.Note
			}
		}
		return ""
	}
	if brand(res) != brand(re) || brand(res) == "file type" {
		t.Errorf("ftyp brand lost on edit: result=%q reparse=%q", brand(res), brand(re))
	}
}

func TestMP4ClearChaptersRemovesUdtaCleanly(t *testing.T) {
	// Clearing the only child of a udta (a chpl) must drop the whole udta, not leave
	// an empty one — so a later edit does not create a second udta box.
	chpl := mp4Chpl(1, []time.Duration{0}, []string{"Solo"})
	data := mp4AssembleUdta(chpl)
	plan, err := mustParseBytes(t, data).Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	res, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	// A follow-up tag edit on the returned document must yield exactly one udta.
	plan2, err := res.Edit().Set(tag.Title, "Added").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, w.b, plan2)
	if n := bytes.Count(out2, []byte("udta")); n != 1 {
		t.Errorf("udta atom count = %d, want 1 (duplicate udta after clear+edit)", n)
	}
	if mustParseBytes(t, out2).Fields().Title != "Added" {
		t.Error("tag edit after ClearChapters did not apply")
	}
}

func TestMP4MediaTypeWideValueRoundTrips(t *testing.T) {
	// A stik value above one byte must not be dropped on write.
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	out := applyToBytes(t, data, func() *wl.Plan {
		p, err := mustParseBytes(t, data).Edit().Set(tag.MediaType, "256").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}())
	if v, ok := mustParseBytes(t, out).Get(tag.MediaType); !ok || len(v) != 1 || v[0] != "256" {
		t.Errorf("MediaType round-trip = %v (ok=%v), want [256]", v, ok)
	}
}

func TestMP4EmptyChplNoSpuriousConflict(t *testing.T) {
	// A count-0 chpl alongside a real QuickTime track is not a conflicting source.
	emptyChpl := mp4Chpl(1, nil, nil)
	data := mp4QTFile([]int{0, 5000}, []string{"A", "B"}, emptyChpl)
	doc := mustParseBytes(t, data)
	if hasWarning(doc, wl.WarnChapterSourceConflict) {
		t.Errorf("an empty chpl should not conflict with the QuickTime track; warnings=%v", doc.Warnings())
	}
	if len(doc.Chapters()) != 2 {
		t.Errorf("chapters = %d, want 2 (from the QuickTime track)", len(doc.Chapters()))
	}
}

func TestMP4QTTimescaleZeroIgnored(t *testing.T) {
	// A zero media timescale is invalid; the QuickTime track must be rejected rather
	// than collapsing every chapter to time zero.
	data := mp4QTFileTS(0, []int{0, 5000}, []string{"A", "B"})
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 0 {
		t.Errorf("zero-timescale QuickTime track yielded %d chapters, want 0", len(chs))
	}
}

func TestMP4ChplInvalidUTF8TitleEmptied(t *testing.T) {
	// An invalid-UTF-8 chpl title decodes to empty (matching the QuickTime path),
	// not raw bytes a JSON dump would mangle.
	bad := string([]byte{0xff, 0xfe, 0x41})
	chpl := mp4Chpl(1, []time.Duration{0, 5 * time.Second}, []string{bad, "Good"})
	data := mp4AssembleUdta(mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))), chpl)
	chs := mustParseBytes(t, data).Chapters()
	if len(chs) != 2 || chs[0].Title != "" || chs[1].Title != "Good" {
		t.Errorf("chapters = %+v, want [{Title:\"\"}, {Title:\"Good\"}]", chs)
	}
}

// equalChapterLists compares two chapter slices field-by-field.
func equalChapterLists(a, b []wl.Chapter) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
