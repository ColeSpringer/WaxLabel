package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestRoundTripInvariant is the direct encoding of WaxLabel's core promise - "the plan reports
// exactly what the write will do" - over a small corpus of adversarial inputs across several
// writable formats. For each case it edits, executes, and re-parses the output, then asserts
// the plan's post-write result Document equals a fresh re-parse of the bytes it wrote across
// every projection: tags, warnings, chapters, synced lyrics, and pictures. That single
// equality catches the whole class the pre-v1.0 pass targeted (an over-range value silently
// dropped, a preserved-but-invalid key double-counted, a malformed block lost on rewrite): if
// the write emits something the round-trip cannot reproduce, the result Document and the
// re-parse disagree here.
func TestRoundTripInvariant(t *testing.T) {
	read := func(path string) []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	overChapter := wl.Chapter{Start: maxChapterDuration + time.Second, Title: "Ghost"}
	overLyric := wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: maxLRCTime + time.Minute, Text: "x"}}}

	cases := []struct {
		name string
		src  []byte
		edit func(*wl.Editor)
	}{
		{"flac orphan key + tag edit", flacWithComments("TITLE=x", "=orphan"),
			func(e *wl.Editor) { e.Set(tag.Artist, "A") }},
		{"flac over-range chapter", read(notagsFLAC),
			func(e *wl.Editor) { e.SetChapters(overChapter) }},
		{"flac over-range synced lyric", read(notagsFLAC),
			func(e *wl.Editor) { e.SetSyncedLyrics(overLyric) }},
		{"flac malformed picture + add cover", flacWithMalformedPicture(),
			func(e *wl.Editor) {
				e.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()})
			}},
		{"wav duplicate title + tag edit",
			wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Title A"}, [2]string{"INAM", "Title B"}), wavData(400)),
			func(e *wl.Editor) { e.Set(tag.Artist, "New Artist") }},
		{"ogg over-range chapter", read(notagsOgg),
			func(e *wl.Editor) { e.SetChapters(overChapter) }},
		{"ogg over-range synced lyric", read(notagsOgg),
			func(e *wl.Editor) { e.SetSyncedLyrics(overLyric) }},
		{"flac clean tag edit", read(sampleFLAC),
			func(e *wl.Editor) { e.Set(tag.Artist, "RoundTrip Artist ZZ9") }},
		{"mp3 clean tag edit", read(sampleMP3),
			func(e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }},
		{"mp4 clean tag edit", read(notagsMP4),
			func(e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }},
		{"wav clean tag edit", read(notagsWAV),
			func(e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }},
		{"musepack chapter packets + tag edit", mpcChaptered(),
			func(e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }},
		{"musepack editor-written chapters + tag edit", read(chaptersMPC),
			func(e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }},
		// A truncated source: its count is the surviving bytes', and the write resolves the
		// truncation by declaring what it wrote, so the re-parse sees a well-formed file
		// whose COMM overstates. The result document must read the same as that file.
		{"aiff truncated PCM + tag edit",
			aiffFile("AIFF", stdCOMM(), truncatedSSND(8+1000*4, 8+100*4)),
			func(e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }},
		{"aiff truncated ima4 + tag edit",
			aiffFile("AIFC", aiffCOMMC(2, 1000, 16, 44100, "ima4"), truncatedSSND(8+1000*68, 8+100*68)),
			func(e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ed := mustParseBytes(t, c.src).Edit()
			c.edit(ed)
			plan, err := ed.Prepare()
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			// Every case is a real edit. A silent drop of the class this pass targets (an
			// over-range chapter/lyric written unreadably) collapses the edit to "No metadata
			// changes" - so a no-op here means the edit vanished before the write.
			if plan.IsNoOp() {
				t.Fatal("a real edit collapsed to a no-op (the edit was silently dropped)")
			}
			var w writerTo
			result, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(c.src)))
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			reparse := mustParseBytes(t, w.b)
			assertSameProjection(t, result, reparse)
		})
	}
}

// assertSameProjection fails if the plan's result document disagrees with a fresh re-parse of
// the written bytes on any projected surface, the audio properties included: a write
// copies the audio, but the count a reader derives from it can depend on the container
// state the write changes, as a truncated AIFF's once did.
func assertSameProjection(t *testing.T, want, got *wl.Document) {
	t.Helper()
	if diff := tag.Diff(want.Tags(), got.Tags()); len(diff) != 0 {
		t.Errorf("tags: result doc and re-parse disagree: %v", diff)
	}
	if a, b := want.Properties(), got.Properties(); !reflect.DeepEqual(a, b) {
		t.Errorf("properties: result doc and re-parse disagree:\n  result=%+v\n  reparse=%+v", a, b)
	}
	if a, b := rtWarnKeys(want.Warnings()), rtWarnKeys(got.Warnings()); !slices.Equal(a, b) {
		t.Errorf("warnings: result doc and re-parse disagree:\n  result=%v\n  reparse=%v", a, b)
	}
	if !rtChaptersEqual(want.Chapters(), got.Chapters()) {
		t.Errorf("chapters: result doc %v vs re-parse %v", want.Chapters(), got.Chapters())
	}
	if !rtSyncedEqual(want.SyncedLyrics(), got.SyncedLyrics()) {
		t.Errorf("synced lyrics: result doc and re-parse disagree")
	}
	if !rtPicturesEqual(want.Pictures(), got.Pictures()) {
		t.Errorf("pictures: result doc and re-parse disagree")
	}
}

func rtWarnKeys(ws []wl.Warning) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Code.String()+"|"+w.Message)
	}
	slices.Sort(out)
	return out
}

func rtChaptersEqual(a, b []wl.Chapter) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Start != b[i].Start || a[i].End != b[i].End || a[i].Title != b[i].Title {
			return false
		}
	}
	return true
}

func rtSyncedEqual(a, b []wl.SyncedLyrics) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Language != b[i].Language || a[i].Description != b[i].Description {
			return false
		}
		if !slices.Equal(a[i].Lines, b[i].Lines) {
			return false
		}
	}
	return true
}

func rtPicturesEqual(a, b []wl.Picture) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].MIME != b[i].MIME || a[i].Description != b[i].Description {
			return false
		}
		if !bytes.Equal(a[i].Data, b[i].Data) {
			return false
		}
	}
	return true
}

// TestRoundTripInvariantFixtures runs the same result-equals-re-parse check over every
// fixture in testdata, and requires that sweep to have exercised every writable format,
// so a format the adversarial table above does not reach is still held to the promise.
func TestRoundTripInvariantFixtures(t *testing.T) {
	paths, err := filepath.Glob("../testdata/*")
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob testdata: %v (%d files)", err, len(paths))
	}
	covered := map[wl.Format]bool{}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			doc := mustParseBytes(t, src)
			if !doc.Format().Writable() {
				t.Skip("read-only format")
			}
			covered[doc.Format()] = true
			plan, err := doc.Edit().Set(tag.Title, "RoundTrip Title ZZ9").Prepare()
			if err != nil {
				// A header-only fixture has no audio essence, which Editor.Prepare refuses
				// to write metadata to by design; there is no output to hold to the promise.
				if errors.Is(err, waxerr.ErrInvalidData) && hasWarning(doc, wl.WarnNoAudioFrames) {
					t.Skip("no audio essence: the editor refuses the write")
				}
				t.Fatalf("prepare: %v", err)
			}
			var w writerTo
			result, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(src)))
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			assertSameProjection(t, result, mustParseBytes(t, w.b))
		})
	}
	for f := wl.Format(1); f.Implemented(); f++ {
		if f.Writable() && !covered[f] {
			t.Errorf("no fixture in testdata exercises %s, a writable format", f)
		}
	}
}
