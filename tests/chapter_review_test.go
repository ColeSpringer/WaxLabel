package waxlabel_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// execChapters applies an edit to src and returns a fresh parse of the written bytes.
func execChapters(t *testing.T, src []byte, edit func(*wl.Editor) *wl.Editor) *wl.Document {
	t.Helper()
	plan, err := edit(mustParseBytes(t, src).Edit()).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	return mustParseBytes(t, applyToBytes(t, src, plan))
}

func chapterWarn(doc *wl.Document, code wl.WarningCode) bool {
	for _, w := range doc.Warnings() {
		if w.Code == code {
			return true
		}
	}
	return false
}

// A chapter edit on a file with a QuickTime chapter track rebuilds the track, so the edit reads
// back with the chpl and the track in agreement.
func TestMP4ChapterEditRebuildsAgreeingQTTrack(t *testing.T) {
	data := testfile.MP4QTFile([]int{0, 3000, 6000}, []string{"A", "B", "C"})
	re := execChapters(t, data, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(wl.Chapter{Start: 0, Title: "NEW1"}, wl.Chapter{Start: 4 * time.Second, Title: "NEW2"})
	})
	if chs := re.Chapters(); len(chs) != 2 || chs[0].Title != "NEW1" || chs[1].Title != "NEW2" || chs[1].Start != 4*time.Second {
		t.Errorf("chapters = %+v, want NEW1 at 0 and NEW2 at 4s", chs)
	}
	if chapterWarn(re, wl.WarnChapterSourceConflict) {
		t.Error("a rebuilt QuickTime track agrees with the chpl, so there must be no source conflict")
	}
}

func TestMP4ChapterEndFilledFromNextStart(t *testing.T) {
	// chpl carries no End; a read fills it from the next start.
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
	re := execChapters(t, data, func(e *wl.Editor) *wl.Editor {
		return e.SetChapters(wl.Chapter{Start: 0, Title: "A"}, wl.Chapter{Start: 4 * time.Second, Title: "B"})
	})
	if got := re.Chapters()[0].End; got != 4*time.Second {
		t.Errorf("End = %v, want 4s", got)
	}
}

func TestMP4BrandPreservedAcrossEdit(t *testing.T) {
	src := readFixture(t, sampleM4B)
	re := execChapters(t, src, func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "X") })
	brand := func(d *wl.Document) string {
		for _, e := range d.Native().Describe() {
			if e.Kind == "ftyp" {
				return e.Note
			}
		}
		return ""
	}
	if got, want := brand(re), brand(mustParseBytes(t, src)); got != want || got == "file type" {
		t.Errorf("ftyp brand after an edit = %q, want the source's %q", got, want)
	}
}

func TestMP4ClearChaptersRemovesUdtaCleanly(t *testing.T) {
	// Clearing the only child of a udta (a chpl) must drop the whole udta, not leave an empty one, so a
	// later edit does not create a second udta box.
	chpl := mp4Chpl(1, []time.Duration{0}, []string{"Solo"})
	data := testfile.MP4AssembleUdta(chpl)
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

func TestMP4MediaTypeWideValueDropped(t *testing.T) {
	// A stik value past the single byte the atom stores (the iTunes media kinds are 0-14) is
	// dropped and warned, not widened into a 2- or 4-byte atom. A value that fits (2) still stores.
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
	p, err := mustParseBytes(t, data).Edit().Set(tag.MediaType, "256").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	dropped := false
	for _, w := range p.Report().Warnings {
		if w.Code == wl.WarnValueDropped {
			dropped = true
		}
	}
	if !dropped {
		t.Errorf("MEDIATYPE=256 must warn value-dropped; got %v", p.Report().Warnings)
	}
	out := applyToBytes(t, data, p)
	if v, ok := mustParseBytes(t, out).Get(tag.MediaType); ok {
		t.Errorf("MEDIATYPE=256 must be absent from the written file (not a widened atom), got %v", v)
	}

	// A value inside the byte range stores and round-trips.
	p2, err := mustParseBytes(t, data).Edit().Set(tag.MediaType, "2").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, data, p2)
	if v, ok := mustParseBytes(t, out2).Get(tag.MediaType); !ok || len(v) != 1 || v[0] != "2" {
		t.Errorf("MediaType=2 round-trip = %v (ok=%v), want [2]", v, ok)
	}
}

func TestMP4EmptyChplNoSpuriousConflict(t *testing.T) {
	// A count-0 chpl alongside a real QuickTime track is not a conflicting source.
	emptyChpl := mp4Chpl(1, nil, nil)
	data := testfile.MP4QTFile([]int{0, 5000}, []string{"A", "B"}, emptyChpl)
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
	data := testfile.MP4QTFileTS(0, []int{0, 5000}, []string{"A", "B"})
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 0 {
		t.Errorf("zero-timescale QuickTime track yielded %d chapters, want 0", len(chs))
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
