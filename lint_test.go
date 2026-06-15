package waxlabel_test

import (
	"context"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

func findingCodes(fs []wl.Finding) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Code] = true
	}
	return m
}

func TestLintEncoderNoise(t *testing.T) {
	// sample.flac carries ffmpeg's "encoder=Lavf..." stamp.
	codes := findingCodes(mustParseFile(t, sampleFLAC).Lint())
	if !codes["encoder-noise"] {
		t.Errorf("expected encoder-noise finding; got %v", codes)
	}
}

func TestLintMalformedDate(t *testing.T) {
	doc := mustParseBytes(t, writeBack(t, "testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.RecordingDate, "not-a-date")
	}))
	codes := findingCodes(doc.Lint())
	if !codes["malformed-date"] {
		t.Errorf("expected malformed-date finding; got %v", codes)
	}
}

func TestLintAcceptsValidDates(t *testing.T) {
	for _, d := range []string{"2021", "2021-06", "2021-06-15"} {
		doc := mustParseBytes(t, writeBack(t, "testdata/notags.flac", func(e *wl.Editor) {
			e.Set(tag.RecordingDate, d)
		}))
		for _, f := range doc.Lint() {
			if f.Code == "malformed-date" {
				t.Errorf("valid date %q flagged as malformed", d)
			}
		}
	}
}

func TestLintCalendarDates(t *testing.T) {
	lintDate := func(d string) map[string]bool {
		doc := mustParseBytes(t, writeBack(t, "testdata/notags.flac", func(e *wl.Editor) {
			e.Set(tag.RecordingDate, d)
		}))
		return findingCodes(doc.Lint())
	}

	// Calendar-invalid dates are flagged, including non-leap Feb 29 and
	// non-zero-padded forms.
	for _, d := range []string{"2021-02-31", "2021-13-01", "2021-00-10", "2021-06-00", "2021-2-3", "99999"} {
		if !lintDate(d)["malformed-date"] {
			t.Errorf("date %q should be flagged as malformed", d)
		}
	}
	// Real calendar dates, including a leap-day, are accepted.
	for _, d := range []string{"2020-02-29", "2021-12-31", "2021-06", "2021"} {
		if lintDate(d)["malformed-date"] {
			t.Errorf("valid date %q wrongly flagged", d)
		}
	}
	// Feb 29 in a non-leap year is invalid.
	if !lintDate("2021-02-29")["malformed-date"] {
		t.Error("2021-02-29 (non-leap) should be flagged")
	}
}

func TestLintDuplicatePicture(t *testing.T) {
	png := tinyPNG()
	doc := mustParseBytes(t, writeBack(t, "testdata/notags.flac", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: png})
		e.AddPicture(wl.Picture{Type: wl.PicBackCover, Data: png}) // same bytes, different role
	}))
	codes := findingCodes(doc.Lint())
	if !codes["duplicate-picture"] {
		t.Errorf("expected duplicate-picture finding; got %v", codes)
	}
}

func TestLintClean(t *testing.T) {
	// A freshly written file with one good date and no legacy noise should be
	// clean.
	doc := mustParseBytes(t, writeBack(t, "testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Title, "Clean").Set(tag.RecordingDate, "2022-01-01")
	}))
	if fs := doc.Lint(); len(fs) != 0 {
		t.Errorf("expected no findings, got %v", fs)
	}
}

// writeBack applies edits to a fixture in memory and returns the written bytes.
func writeBack(t *testing.T, fixture string, edit func(*wl.Editor)) []byte {
	t.Helper()
	src := readFixture(t, fixture)
	doc, err := wl.Parse(context.Background(), wl.BytesSource(src))
	if err != nil {
		t.Fatal(err)
	}
	ed := doc.Edit()
	edit(ed)
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var buf writerTo
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(src))); err != nil {
		t.Fatal(err)
	}
	return buf.b
}

type writerTo struct{ b []byte }

func (w *writerTo) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
