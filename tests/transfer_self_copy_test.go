package waxlabel_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// A copy of a file onto itself loses nothing and writes nothing, for every fixture: the formats
// no write reaches (WMA), a file with no audio, chapters only the reader models (Musepack), and
// the layouts an explicit same-value edit would rewrite (AIFF text chunks, a Matroska last end).
func TestTransferOntoItselfIsANoOp(t *testing.T) {
	fixtures, err := filepath.Glob("../testdata/*")
	if err != nil || len(fixtures) < 40 {
		t.Fatalf("fixtures: %d found, err %v", len(fixtures), err)
	}
	ctx := context.Background()
	for _, fixture := range fixtures {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			t.Parallel()
			path := copyToTemp(t, fixture)
			// A real save sets a new modification time, so an old one shows a rewrite.
			old := time.Now().Add(-time.Hour).Truncate(time.Second)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			src, dst := mustParseFile(t, path), mustParseFile(t, path)
			plan, report, err := src.PrepareTransfer(dst)
			if err != nil {
				t.Fatalf("PrepareTransfer: %v\n%+v", err, report.Items)
			}
			if !report.Lossless() {
				t.Errorf("report is not lossless: %+v", report.Items)
			}
			if !plan.IsNoOp() {
				t.Errorf("plan is not a no-op: %+v", plan.Report().Operations)
			}
			_, res, err := plan.Execute(ctx, wl.SaveBack())
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if res.Committed {
				t.Error("the save committed")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(before, after) || !info.ModTime().Equal(old) {
				t.Errorf("the file changed: same bytes %v, modified %s (was %s)", slices.Equal(before, after), info.ModTime(), old)
			}
		})
	}
}

// A file with no audio is still refused a write, whatever asks for it: a copy that brings
// something new, and one that brings nothing under an option that rewrites the file.
func TestTransferOntoNoAudioFileStillRefusesAWrite(t *testing.T) {
	empty := mustParseFile(t, "../testdata/empty.mp3")
	if !hasWarning(empty, wl.WarnNoAudioFrames) {
		t.Fatal("setup: empty.mp3 should read as holding no audio")
	}
	src := mustParseBytes(t, writeBack(t, notagsMP3, func(e *wl.Editor) { e.Set(tag.Title, "New") }))
	if _, _, err := src.PrepareTransfer(empty); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("a copy that sets a title: err = %v, want the no-audio refusal", err)
	}
	if _, err := empty.Edit().Set(tag.Title, "New").Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("an edit: err = %v, want the no-audio refusal", err)
	}
	// An editor is refused whatever it asks, nothing included; only a copy that finds everything
	// already there is let through to a plan that writes nothing.
	if _, err := empty.Edit().Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("an edit of nothing: err = %v, want the no-audio refusal", err)
	}
	// An explicit padding policy re-renders the front tag, which is a write.
	if _, _, err := empty.PrepareTransfer(empty, wl.WithPadding(wl.PaddingPolicy{Min: 4096, Target: 4096, Max: 4096})); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("a copy onto itself under a padding policy that rewrites the tag: err = %v, want the no-audio refusal", err)
	}
}

// A destination no write reaches reports what it does not already hold as dropped, and the
// transfer is refused.
func TestTransferOntoReadOnlyDestinationRefusesWhatItLacks(t *testing.T) {
	chained := append(slices.Clone(readFixture(t, sampleOgg)), readFixture(t, notagsOgg)...)
	dst := mustParseBytes(t, chained)
	src := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Title, "Sample Title").Set(tag.Genre, "New")
	}))
	_, report, err := src.PrepareTransfer(dst)
	if !errors.Is(err, waxerr.ErrChainedStream) {
		t.Fatalf("err = %v, want the chained-stream refusal", err)
	}
	if it := transferItemFor(t, report, tag.Genre); it.Disposition != wl.Dropped || it.Reason != "destination is read-only" {
		t.Errorf("GENRE = %s (%s), want dropped as read-only", it.Disposition, it.Reason)
	}
	// The title the first link already holds is no loss.
	if it := transferItemFor(t, report, tag.Title); it.Disposition != wl.Carried {
		t.Errorf("TITLE = %s (%s), want carried: the destination holds it", it.Disposition, it.Reason)
	}
	// With nothing to bring, there is nothing to refuse.
	if plan, report, err := dst.PrepareTransfer(dst); err != nil || !plan.IsNoOp() || !report.Lossless() {
		t.Errorf("onto itself: err %v, report %+v", err, report.Items)
	}
}

// A copy onto itself is a no-op for files that hold what no fixture does: picture blocks that
// declare other than their bytes, number values with spaces around them, a comment and a lyric in
// ID3's default language beside ones in none.
func TestTransferOntoItselfIsANoOpForOddFiles(t *testing.T) {
	mp3With := func(edit func(*wl.Editor)) []byte { return writeBack(t, notagsMP3, edit) }
	for name, data := range map[string][]byte{
		"loose pictures": loosePictureFLAC(),
		"spaced numbers": testfile.TaggedFLAC("v", "TRACKNUMBER= 3 ", "DATE= 2001 ", "BPM= 120 ", "TITLE=T"),
		"default and no language": mp3With(func(e *wl.Editor) {
			e.SetLyrics(wl.Lyrics{Text: "la", Language: "eng"}, wl.Lyrics{Text: "lu", Language: "xxx"})
			e.SetComments(wl.Comment{Text: "hello", Language: "eng"}, wl.Comment{Text: "x", Language: "xxx"})
		}),
		"an empty comment beside one": testfile.TaggedFLAC("v", "COMMENT=x", "COMMENT="),
	} {
		t.Run(name, func(t *testing.T) {
			src, dst := mustParseBytes(t, data), mustParseBytes(t, data)
			plan, report, err := src.PrepareTransfer(dst)
			if err != nil {
				t.Fatalf("PrepareTransfer: %v", err)
			}
			if !report.Lossless() {
				t.Errorf("report is not lossless: %+v", report.Items)
			}
			if !plan.IsNoOp() {
				t.Errorf("plan is not a no-op: %+v, changes %+v", plan.Report().Operations, plan.Changes())
			}
		})
	}
}

// A copy settles: a second run of the same copy finds everything there and writes nothing, and
// the file it wrote copies onto itself as a no-op.
func TestTransferSettlesOnARepeat(t *testing.T) {
	languages := writeBack(t, "../testdata/notags.mka", func(e *wl.Editor) {
		e.SetComments(
			wl.Comment{Text: "hello", Language: "eng"}, wl.Comment{Text: "hallo", Language: "ger"},
			wl.Comment{Text: "salut", Language: "fre"}, wl.Comment{Text: "plain"})
	})
	for _, c := range []struct {
		name, dst string
		src       []byte
	}{
		{"comments in several languages onto MP3", "notags.mp3", languages},
		{"comments in several languages onto AIFF", "sample.aiff", languages},
		{"a comment and an empty one onto AIFF", "sample.aiff", testfile.TaggedFLAC("v", "COMMENT=x", "COMMENT=")},
		{"a comment and an empty one onto WAV", "notags.wav", testfile.TaggedFLAC("v", "COMMENT=x", "COMMENT=")},
		{"a custom key ID3 reads as another", "notags.mp3", testfile.TaggedFLAC("v", "TCMP=1", "TITLE=T")},
		{"a slashed number among several onto Matroska", "notags.mka", testfile.TaggedFLAC("v", "TRACKNUMBER=3/12", "TRACKNUMBER=4", "TITLE=T")},
		{"a slashed number among several onto MP3", "notags.mp3", testfile.TaggedFLAC("v", "TRACKNUMBER=3/12", "TRACKNUMBER=4", "TITLE=T")},
		{"a key MP4 reads as another beside that key", "notags.m4a", testfile.TaggedFLAC("v", "MUSICBRAINZ ALBUM ID=a", "MUSICBRAINZ_ALBUMID=b")},
		{"empty numbers onto a tagged MP4", "sample.m4a", testfile.TaggedFLAC("v", "TRACKNUMBER=", "BPM=", "TITLE=T")},
		{"loosely declared pictures onto MP3", "notags.mp3", loosePictureFLAC()},
		{"loosely declared pictures onto FLAC", "notags.flac", loosePictureFLAC()},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := mustParseBytes(t, c.src)
			dstBytes := readFixture(t, "../testdata/"+c.dst)
			plan, first, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
			if err != nil {
				t.Fatalf("first copy: %v", err)
			}
			written := applyToBytes(t, dstBytes, plan)
			dst := mustParseBytes(t, written)
			plan, second, err := src.PrepareTransfer(dst)
			if err != nil {
				t.Fatalf("second copy: %v", err)
			}
			if !plan.IsNoOp() {
				t.Errorf("the second copy plans a write: %+v, changes %+v", plan.Report().Operations, plan.Changes())
			}
			// What the first copy stored of an item is what the second finds: no grade gets worse.
			for i, it := range second.Items {
				if i < len(first.Items) && it.Disposition > first.Items[i].Disposition {
					t.Errorf("%s %s: %s on the first copy, %s (%s) on the second", it.Kind, it.Key, first.Items[i].Disposition, it.Disposition, it.Reason)
				}
			}
			if plan, report, err := dst.PrepareTransfer(mustParseBytes(t, written)); err != nil || !plan.IsNoOp() || !report.Lossless() {
				t.Errorf("the written file onto itself: err %v, no-op %v, report %+v", err, plan.IsNoOp(), report.Items)
			}
		})
	}
}
