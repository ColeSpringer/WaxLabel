package waxlabel_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// An edit on input with adversarial content (an orphan key, a chapter or synced lyric past the time
// ceiling, a malformed picture, duplicate INFO titles, Musepack chapter packets, a truncated AIFF)
// is a real write the read-back accepts, not a no-op that drops the edit silently.
func TestAdversarialEditsWrite(t *testing.T) {
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
				e.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()})
			}},
		{"wav duplicate title + tag edit",
			testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Title A"}, [2]string{"INAM", "Title B"}), testfile.WAVData(400)),
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
		{"musepack chapter packets + tag edit", testfile.MPCChaptered(),
			func(e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }},
		{"musepack editor-written chapters + tag edit", read(chaptersMPC),
			func(e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }},
		// A truncated source: its count is the surviving bytes', and the write resolves the
		// truncation by declaring what it wrote, so the output is a well-formed file whose COMM
		// overstates.
		{"aiff truncated PCM + tag edit",
			testfile.AIFF("AIFF", stdCOMM(), testfile.TruncatedSSND(8+1000*4, 8+100*4)),
			func(e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }},
		{"aiff truncated ima4 + tag edit",
			testfile.AIFF("AIFC", testfile.COMMC(2, 1000, 16, 44100, "ima4"), testfile.TruncatedSSND(8+1000*68, 8+100*68)),
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
			if plan.IsNoOp() {
				t.Fatal("a real edit collapsed to a no-op (the edit was silently dropped)")
			}
			var w writerTo
			if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(c.src))); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})
	}
}

// assertSameProjection fails if the Document a write returns disagrees with a fresh re-parse of the
// written bytes on any projected surface, audio properties included: a write copies the audio, but
// the count a reader derives from it can depend on container state the write changes (a truncated
// AIFF's does).
func assertSameProjection(t *testing.T, want, got *wl.Document) {
	t.Helper()
	if diff := tag.Diff(want.Tags(), got.Tags()); len(diff) != 0 {
		t.Errorf("tags: returned doc and re-parse disagree: %v", diff)
	}
	if a, b := want.Properties(), got.Properties(); !reflect.DeepEqual(a, b) {
		t.Errorf("properties: returned doc and re-parse disagree:\n  returned=%+v\n  reparse=%+v", a, b)
	}
	if a, b := rtWarnKeys(want.Warnings()), rtWarnKeys(got.Warnings()); !slices.Equal(a, b) {
		t.Errorf("warnings: returned doc and re-parse disagree:\n  returned=%v\n  reparse=%v", a, b)
	}
	if !rtChaptersEqual(want.Chapters(), got.Chapters()) {
		t.Errorf("chapters: returned doc %v vs re-parse %v", want.Chapters(), got.Chapters())
	}
	if !rtSyncedEqual(want.SyncedLyrics(), got.SyncedLyrics()) {
		t.Errorf("synced lyrics: returned doc and re-parse disagree")
	}
	if !rtPicturesEqual(want.Pictures(), got.Pictures()) {
		t.Errorf("pictures: returned doc and re-parse disagree")
	}
	if a, b := want.Padding(), got.Padding(); a != b {
		t.Errorf("padding: returned doc %d, re-parse %d", a, b)
	}
	if a, b := rtLintKeys(want.Lint()), rtLintKeys(got.Lint()); !slices.Equal(a, b) {
		t.Errorf("lint: returned doc and re-parse disagree:\n  returned=%v\n  reparse=%v", a, b)
	}
}

// assertSameIdentity fails unless the returned Document's identity is the written file's, field by
// field: a re-edit of it saves against that identity.
func assertSameIdentity(t *testing.T, want, got wl.Identity) {
	t.Helper()
	if want.Path != got.Path || want.Size != got.Size || want.ModTimeUnixNano != got.ModTimeUnixNano ||
		want.ChangeTimeUnixNano != got.ChangeTimeUnixNano || want.INode != got.INode || want.Device != got.Device ||
		want.Fingerprint != got.Fingerprint || want.HasFinger != got.HasFinger {
		t.Errorf("identity: returned doc and re-parse disagree:\n  returned=%+v\n  reparse=%+v", want, got)
	}
	if ok, why := want.Matches(got); !ok {
		t.Errorf("identity: returned doc does not match the written file: %s", why)
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

func rtLintKeys(fs []wl.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.String())
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

// flacWithStrayTagsAndTwoVC is a FLAC behind a leading ID3v2, with two VORBIS_COMMENT blocks and a
// trailing ID3v1: every container a rewrite may drop or merge.
func flacWithStrayTagsAndTwoVC() []byte {
	return slices.Concat(
		testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Stray Title")),
		flacWithTwoVC(),
		testfile.ID3v1("Trailing Title", "", "", "", "", 255),
	)
}

// The Document a write returns reads the same as a fresh parse of the written bytes, for every
// writable fixture and edit kind: SaveAsFile against ParseFile of the output (identity included),
// WriteTo against Parse of the streamed bytes. Every write verifies its essence too, and the sweep
// must reach every writable format, so a format the adversarial table above misses is still held
// to the promise.
func TestExecuteDocumentMatchesFreshParse(t *testing.T) {
	ctx := context.Background()
	paths, err := filepath.Glob("../testdata/*")
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob testdata: %v (%d files)", err, len(paths))
	}
	paths = append(paths, writeTempFile(t, "stray-tags-two-vc.flac", flacWithStrayTagsAndTwoVC()))

	edits := []struct {
		name string
		edit func(*wl.Document, *wl.Editor)
		opts []wl.WriteOption
	}{
		{"title", func(_ *wl.Document, e *wl.Editor) { e.Set(tag.Title, "RoundTrip Title ZZ9") }, nil},
		{"minimal", func(*wl.Document, *wl.Editor) {}, []wl.WriteOption{wl.Minimal}},
		{"compatible", func(*wl.Document, *wl.Editor) {}, []wl.WriteOption{wl.Compatible}},
		{"add chapter", func(d *wl.Document, e *wl.Editor) {
			e.SetChapters(append(d.Chapters(), wl.Chapter{Start: 500 * time.Millisecond, Title: "Added"})...)
		}, []wl.WriteOption{wl.WithAllowUnsupportedDrop()}},
		{"clear chapters", func(_ *wl.Document, e *wl.Editor) { e.ClearChapters() }, []wl.WriteOption{wl.WithAllowUnsupportedDrop()}},
		{"add cover", func(_ *wl.Document, e *wl.Editor) {
			e.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()})
		}, []wl.WriteOption{wl.WithAllowUnsupportedDrop()}},
	}
	covered := map[wl.Format]bool{}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			doc := mustParseFile(t, path)
			if !doc.Format().Writable() {
				t.Skip("read-only format")
			}
			if hasWarning(doc, wl.WarnNoAudioFrames) {
				t.Skip("no audio essence: the editor refuses the write")
			}
			covered[doc.Format()] = true
			for _, ed := range edits {
				t.Run(ed.name, func(t *testing.T) {
					e := doc.Edit()
					ed.edit(doc, e)
					plan, err := e.Prepare(append([]wl.WriteOption{wl.WithVerifyEssence()}, ed.opts...)...)
					if err != nil {
						t.Fatalf("prepare: %v", err)
					}
					out := filepath.Join(t.TempDir(), "out"+filepath.Ext(path))
					saved, res, err := plan.Execute(ctx, wl.SaveAsFile(out))
					if err != nil || !res.Committed {
						t.Fatalf("SaveAsFile: err=%v committed=%v", err, res.Committed)
					}
					fresh := mustParseFile(t, out)
					assertSameProjection(t, saved, fresh)
					assertSameIdentity(t, saved.Identity(), fresh.Identity())

					var w writerTo
					streamed, _, err := plan.Execute(ctx, wl.WriteTo(&w, nil))
					if err != nil {
						t.Fatalf("WriteTo: %v", err)
					}
					assertSameProjection(t, streamed, mustParseBytes(t, w.b))
				})
			}
		})
	}
	for f := wl.Format(1); f.Implemented(); f++ {
		if f.Writable() && !covered[f] {
			t.Errorf("no fixture in testdata exercises %s, a writable format", f)
		}
	}
}
