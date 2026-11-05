package waxlabel

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// parityEdit is one write of the parity matrix: kind names it in knownDrift, name in the subtest
// (kind when empty). then lists writes made from what it wrote.
type parityEdit struct {
	kind, name string
	edit       func(*Document, *Editor)
	opts       []WriteOption
	then       []parityEdit
}

func (e parityEdit) label() string { return cmp.Or(e.name, e.kind) }

// The kinds of the edits only some inputs run; a chapter clear is the standard "clear chapters".
const (
	kindTags     = "tags"
	kindChapters = "chapters"
	kindPictures = "pictures"
	kindLyrics   = "synced lyrics"
)

// parityEdits are the edit kinds every input runs.
var parityEdits = []parityEdit{
	{kind: "title", edit: setTag(tag.Title, "Parity Title")},
	{kind: "minimal", edit: func(*Document, *Editor) {}, opts: []WriteOption{Minimal}},
	{kind: "compatible", edit: func(*Document, *Editor) {}, opts: []WriteOption{Compatible}},
	{kind: "add chapter", edit: func(d *Document, e *Editor) {
		e.SetChapters(append(d.Chapters(), Chapter{Start: 500 * time.Millisecond, Title: "Added"})...)
	}},
	{kind: "clear chapters", edit: clearChapters},
	{kind: "add cover", edit: addCover(PicFrontCover)},
}

// parityInput is a file the matrix writes, with the edits only it runs.
type parityInput struct {
	name  string
	data  []byte
	edits []parityEdit
}

// driftKey names parity cases: an input, or a format for every input of it, and the kinds of the
// edits that wrote it, joined by " > ".
type driftKey struct{ input, edit string }

// drift is a known disagreement: the surfaces that differ, comma-separated, and why.
type drift struct{ surfaces, reason string }

// knownDrift holds the disagreements left to fix. The match is exact: an entry whose case reads the
// same as the written bytes, or differs on other surfaces, fails the test. An input's own entry, even
// one with no surfaces, takes precedence over its format's: that exempts an input that does not drift.
var knownDrift = map[driftKey]drift{
	{"mp3.mov", "add chapter"}: {"tags", "the QuickTime chapter-track builder drops the classic udta text atoms (an inherited ENCODER) from the result"},
}

// TestResultBuilderParity: the result a codec builds at Prepare reads the same as a parse of the
// bytes the plan writes, on every surface production reads from it, for every writable input and
// edit kind.
func TestResultBuilderParity(t *testing.T) {
	run := &parityRun{used: map[driftKey]bool{}}
	covered := map[Format]bool{}
	for _, in := range parityInputs(t) {
		run.subtest(t, in.name, func(t *testing.T) {
			doc := mustParseParity(t, in.data)
			if !doc.Format().Writable() {
				t.Skip("read-only format")
			}
			if hasNoAudioWarning(doc.media) {
				t.Skip("no audio essence: the editor refuses the write")
			}
			covered[doc.Format()] = true
			for _, ed := range slices.Concat(parityEdits, in.edits) {
				run.edit(t, in.name, ed.kind, doc, in.data, ed)
			}
		})
	}
	if run.partial {
		return // a -run pattern skipped cases the checks below need
	}
	for f := Format(1); f.Implemented(); f++ {
		if f.Writable() && !covered[f] {
			t.Errorf("no parity input exercises %s, a writable format", f)
		}
	}
	for k := range knownDrift {
		if !run.used[k] {
			t.Errorf("knownDrift entry %v matched no case; delete it", k)
		}
	}
}

// parityRun is the state the matrix's cases share.
type parityRun struct {
	used    map[driftKey]bool // knownDrift entries some case matched
	partial bool              // a -run pattern skipped a case
}

// subtest runs fn as a subtest and notes when a -run pattern skips it.
func (r *parityRun) subtest(t *testing.T, name string, fn func(*testing.T)) {
	ran := false
	t.Run(name, func(t *testing.T) {
		ran = true
		fn(t)
	})
	r.partial = r.partial || !ran
}

// edit writes ed against doc, read from src, compares the plan's result with a fresh parse of the
// written bytes, and runs ed.then against that parse. path is the kinds of the edits so far.
func (r *parityRun) edit(t *testing.T, input, path string, doc *Document, src []byte, ed parityEdit) {
	r.subtest(t, ed.label(), func(t *testing.T) {
		e := doc.Edit()
		ed.edit(doc, e)
		plan, err := e.Prepare(ed.opts...)
		if errors.Is(err, waxerr.ErrUnsupportedTag) {
			t.Skipf("refused at Prepare: %v", err)
		}
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		var out bytes.Buffer
		if _, _, err := plan.Execute(context.Background(), WriteTo(&out, BytesSource(src))); err != nil {
			t.Fatalf("execute: %v", err)
		}
		written := mustParseParity(t, out.Bytes())
		r.check(t, driftKey{input, path}, doc.Format(), plan, written)
		for _, next := range ed.then {
			r.edit(t, input, path+" > "+next.kind, written, out.Bytes(), next)
		}
	})
}

// check fails unless the plan's result and the parse of the written bytes disagree exactly as
// knownDrift says.
func (r *parityRun) check(t *testing.T, key driftKey, format Format, plan *Plan, written *Document) {
	t.Helper()
	got := parityDrift(plan.resultMedia(), written.media)
	want, known := knownDrift[key]
	if !known {
		key.input = format.String()
		want, known = knownDrift[key]
	}
	if known {
		r.used[key] = true
	}
	names := make([]string, 0, len(got))
	for _, d := range got {
		names = append(names, d.surface)
	}
	if strings.Join(names, ",") == want.surfaces {
		return
	}
	if known {
		t.Errorf("knownDrift %v expects %q (%s), got %q", key, want.surfaces, want.reason, strings.Join(names, ","))
	}
	for _, d := range got {
		t.Errorf("%s:\n  result  %s\n  written %s", d.surface, d.result, d.written)
	}
}

// surfaceDiff is one surface on which a plan's result and the written bytes' parse disagree.
type surfaceDiff struct{ surface, result, written string }

// parityDrift compares, in a fixed order, the surfaces production reads from a plan's result: the
// tags, pictures, chapters, synced lyrics and output gain Plan.Changes diffs, and the essence ranges
// the read-back verifies.
func parityDrift(result, written *core.Media) []surfaceDiff {
	var out []surfaceDiff
	note := func(surface string, a, b any, equal bool) {
		if !equal {
			out = append(out, surfaceDiff{surface, fmt.Sprintf("%+v", a), fmt.Sprintf("%+v", b)})
		}
	}
	note("tags", tagLines(result.Tags), tagLines(written.Tags), result.Tags.Equal(written.Tags))
	note("pictures", pictureLines(result.Pictures), pictureLines(written.Pictures), core.EqualPictures(result.Pictures, written.Pictures))
	note("chapters", result.Chapters, written.Chapters, core.EqualChapters(result.Chapters, written.Chapters))
	note("synced lyrics", result.SyncedLyrics, written.SyncedLyrics, core.EqualSyncedLyrics(result.SyncedLyrics, written.SyncedLyrics))
	rg, wg := result.Properties.First().OutputGain, written.Properties.First().OutputGain
	note("output gain", rg, wg, rg == wg)
	re, we := result.EssenceRanges(), written.EssenceRanges()
	note("essence", re, we, slices.Equal(re, we))
	return out
}

func tagLines(ts tag.TagSet) []string {
	var out []string
	for k, v := range ts.All() {
		out = append(out, fmt.Sprintf("%s=%q", k, v))
	}
	slices.Sort(out)
	return out
}

func pictureLines(ps []Picture) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s %s %q %dx%dx%d/%d %d bytes", p.Type, p.MIME, p.Description, p.Width, p.Height, p.Depth, p.Colors, len(p.Data)))
	}
	return out
}

func mustParseParity(t *testing.T, data []byte) *Document {
	t.Helper()
	doc, err := Parse(context.Background(), BytesSource(data))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func setTag(key tag.Key, vals ...string) func(*Document, *Editor) {
	return func(_ *Document, e *Editor) { e.Set(key, vals...) }
}

func setChapters(chs ...Chapter) func(*Document, *Editor) {
	return func(_ *Document, e *Editor) { e.SetChapters(chs...) }
}

func clearChapters(_ *Document, e *Editor) { e.ClearChapters() }

func addCover(role PictureType) func(*Document, *Editor) {
	return func(_ *Document, e *Editor) {
		e.AddPicture(Picture{Type: role, MIME: "image/png", Data: testfile.PNG()})
	}
}

// parityInputs is every testdata file plus the synthesized inputs, each with the edits the
// behavior it stands for needs.
func parityInputs(t *testing.T) []parityInput {
	t.Helper()
	sec := func(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }
	msec := func(n int64) time.Duration { return time.Duration(n) * time.Millisecond }
	ch := func(start time.Duration, title string) Chapter { return Chapter{Start: start, Title: title} }
	tags := func(name string, edit func(*Document, *Editor), then ...parityEdit) parityEdit {
		return parityEdit{kind: kindTags, name: name, edit: edit, then: then}
	}
	chaps := func(name string, edit func(*Document, *Editor), then ...parityEdit) parityEdit {
		return parityEdit{kind: kindChapters, name: name, edit: edit, then: then}
	}
	pics := func(name string, edit func(*Document, *Editor), then ...parityEdit) parityEdit {
		return parityEdit{kind: kindPictures, name: name, edit: edit, then: then}
	}
	overRange := []parityEdit{
		chaps("chapter past the time ceiling", setChapters(ch(1_000_000*time.Hour+time.Second, "Ghost"))),
		{kind: kindLyrics, name: "lyric past the time ceiling", edit: func(_ *Document, e *Editor) {
			e.SetSyncedLyrics(SyncedLyrics{Lines: []SyncedLine{{Time: core.MaxLRCTime + time.Minute, Text: "x"}}})
		}},
	}
	// Chapter lists whose QuickTime track and chpl must stay in step: ends at and past the movie
	// duration, starts on the unit grid, coincident starts and stts or edit-list saturation.
	m4bChapters := []parityEdit{
		chaps("plain", setChapters(ch(0, "A"), ch(sec(3), "B"))),
		chaps("last end below duration", setChapters(ch(0, "A"), Chapter{Start: sec(4), End: sec(6), Title: "B"})),
		chaps("open last within duration", setChapters(ch(0, "A"), ch(sec(4), "B"))),
		chaps("open last past duration", setChapters(ch(0, "A"), ch(sec(9.5), "Epilogue"))),
		chaps("open last on the unit grid", setChapters(ch(0, "A"), ch(9000044444*time.Nanosecond, "Epilogue"))),
		chaps("coincident starts", setChapters(ch(sec(3), "A"), ch(sec(3), "B"), ch(sec(3), "C"),
			ch(sec(3.001), "D"), ch(sec(3.002), "E"))),
		chaps("coincident open last", setChapters(ch(sec(2), "A"), ch(sec(2), "B"))),
		chaps("gap past the stts clamp", setChapters(ch(0, "A"), ch(14*time.Hour, "B"))),
		chaps("start past the edit clamp", setChapters(ch(5_000_000*time.Second, "Far"))),
		chaps("start past the edit clamp with open last", setChapters(ch(5_000_000*time.Second, "Far"), ch(5_000_001*time.Second, "Farther"))),
		chaps("start at the edit ceiling", setChapters(ch(4_294_967_295*time.Millisecond, "Edge"))),
		chaps("non-zero first start", setChapters(ch(sec(4), "Four"), ch(sec(9), "Nine"))),
		chaps("single non-zero start", setChapters(ch(sec(4), "Only"))),
		tags("grow title", setTag(tag.Title, strings.Repeat("Grown Title ", 7)),
			chaps("two chapters", setChapters(ch(0, "Chained A"), ch(sec(3), "Chained B")))),
	}
	stripEncoder := parityEdit{kind: "strip encoder", edit: func(*Document, *Editor) {}, opts: []WriteOption{WithStripEncoderStamp()}}
	extra := map[string][]parityEdit{
		"notags.flac": overRange,
		"notags.ogg":  overRange,
		"sample.flac": {tags("title set to no values", setTag(tag.Title)), stripEncoder},
		"sample.ogg":  {stripEncoder},
		"sample.opus": {stripEncoder},
		"sample.oga":  {stripEncoder},
		"sample.wav":  {tags("composer", setTag(tag.Composer, "Promoted"))},
		"sample.m4a": {chaps("ends at the movie duration", setChapters(
			Chapter{Start: 0, End: sec(0.5), Title: "A"}, Chapter{Start: sec(0.5), End: sec(1), Title: "B"}))},
		"sample_chapters.m4b": m4bChapters,
		"sample.mka": {
			tags("artist", setTag(tag.Artist, "Edit One"), tags("album", setTag(tag.Album, "Edit Two"))),
			pics("large cover", func(_ *Document, e *Editor) {
				e.ClearPictures().AddPicture(Picture{Type: PicFrontCover, MIME: "image/png",
					Data: append(testfile.PNG(), bytes.Repeat([]byte{0x42}, 200000)...)})
			}, tags("artist", setTag(tag.Artist, "Re-edited Artist"))),
			pics("back cover", func(_ *Document, e *Editor) {
				e.ClearPictures().AddPicture(Picture{Type: PicBackCover, MIME: "image/png", Data: testfile.PNG()})
			}),
		},
		"sample.webm": {tags("large title", setTag(tag.Title, strings.Repeat("w", 70000)))},
		"chapters.mka": {
			chaps("three", setChapters(
				Chapter{Start: 0, End: msec(150), Title: "Opening"},
				Chapter{Start: msec(150), End: msec(450), Title: "Body"},
				Chapter{Start: msec(450), End: msec(600), Title: "Closing"})),
			chaps("first pass", setChapters(Chapter{Start: 0, End: msec(300), Title: "First Pass"}),
				chaps("second pass", setChapters(
					Chapter{Start: 0, End: msec(100), Title: "Second A"},
					Chapter{Start: msec(100), End: msec(500), Title: "Second B"}))),
		},
	}

	// Every format that writes chapters stores, then clears, a three-chapter list; every format that
	// writes synced lyrics, a set.
	for _, name := range []string{"notags.mp3", "notags.aac", "notags.aiff", "notags.wav", "notags.flac",
		"notags.ogg", "notags.opus", "notags.oga", "notags.m4a", "notags.mka"} {
		extra[name] = append(extra[name], chaps("three", setChapters(ch(0, "Opening"), ch(sec(3), "Middle"), ch(sec(7.5), "Closing")),
			parityEdit{kind: "clear chapters", edit: clearChapters}))
		if !strings.HasSuffix(name, ".m4a") && !strings.HasSuffix(name, ".mka") {
			extra[name] = append(extra[name], parityEdit{kind: kindLyrics, name: "lyrics", edit: func(_ *Document, e *Editor) {
				e.SetSyncedLyrics(SyncedLyrics{Language: "eng", Description: "Main", Lines: []SyncedLine{
					{Time: 0, Text: "Opening"}, {Time: sec(12), Text: "Verse one"}, {Time: sec(30), Text: ""}}})
			}, then: []parityEdit{{kind: kindLyrics, name: "cleared", edit: func(_ *Document, e *Editor) { e.ClearSyncedLyrics() }}}})
		}
	}

	paths, err := filepath.Glob("testdata/*")
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob testdata: %v (%d files)", err, len(paths))
	}
	var inputs []parityInput
	fixture := map[string][]byte{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(p)
		fixture[name] = data
		inputs = append(inputs, parityInput{name: name, data: data, edits: extra[name]})
		delete(extra, name)
	}
	for name := range extra {
		t.Fatalf("extra edits name %s, which is not in testdata", name)
	}

	longTitle := func(n int) parityEdit {
		return tags(fmt.Sprintf("%d-byte title", n), setTag(tag.Title, strings.Repeat("T", n)),
			tags("artist", setTag(tag.Artist, "A2")))
	}
	audio := bytes.Repeat([]byte{0xA7}, 1000*4)
	v2Title := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "V2 Title"))
	apeCover, err := ape.Render([]ape.Item{{Key: "Cover Art (front)", Data: []byte("\x89PNG binary"), Flags: 2}}, 2000, false) // item type 1: binary
	if err != nil {
		t.Fatal(err)
	}
	return append(inputs,
		parityInput{name: "stray-tags-two-vc.flac", data: slices.Concat(
			testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Stray Title")),
			testfile.FLAC(testfile.CommentBlock("test", "TITLE=x"), testfile.CommentBlock("test", "ALBUM=y"),
				testfile.FLACBlock(1, make([]byte, 4))),
			testfile.ID3v1("Trailing Title", "", "", "", "", 255))},
		parityInput{name: "two-vc.oga", data: testfile.OggFLAC(testfile.CommentBlock("test", "TITLE=x"),
			testfile.CommentBlock("test", "ALBUM=y"))},
		parityInput{name: "lavf-vendor.flac", data: testfile.TaggedFLAC("Lavf58.76.100", "TITLE=Song"),
			edits: []parityEdit{stripEncoder}},
		parityInput{name: "orphan-key.flac", data: testfile.TaggedFLAC("test", "TITLE=x", "=orphan"),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "A"))}},
		parityInput{name: "malformed-picture.flac", data: testfile.FLAC(testfile.CommentBlock("test", "TITLE=x"),
			testfile.FLACBlock(6, []byte{0, 0, 0, 3, 1, 2})),
			edits: []parityEdit{pics("front cover", addCover(PicFrontCover), pics("back cover", addCover(PicBackCover)))}},
		parityInput{name: "leading-id3-cover.flac", data: slices.Concat(
			testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Lead Title"), testfile.FrontCover("image/png", testfile.PNG())),
			testfile.TaggedFLAC("test")),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"))}},
		parityInput{name: "junk-tail.flac", data: slices.Concat(fixture["sample.flac"], bytes.Repeat([]byte{0x4A}, 512),
			testfile.ID3v1("", "", "", "", "", 0)),
			edits: []parityEdit{tags("new title", setTag(tag.Title, "New"), tags("album", setTag(tag.Album, "Again")))}},
		parityInput{name: "duplicate-inam.wav", data: testfile.WAV(testfile.WAVFmtPCM(),
			testfile.WAVInfo([2]string{"INAM", "Title A"}, [2]string{"INAM", "Title B"}), testfile.WAVData(400)),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"))}},
		parityInput{name: "two-info.wav", data: testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "First"}),
			testfile.WAVInfo([2]string{"INAM", "Second"}), testfile.WAVData(400))},
		parityInput{name: "chaptered.mpc", data: testfile.MPCChaptered()},
		parityInput{name: "truncated-pcm.aiff", data: testfile.AIFF("AIFF", testfile.COMM(2, 1000, 16, 44100),
			testfile.TruncatedSSND(8+1000*4, 8+100*4))},
		parityInput{name: "truncated-ima4.aifc", data: testfile.AIFF("AIFC", testfile.COMMC(2, 1000, 16, 44100, "ima4"),
			testfile.TruncatedSSND(8+1000*68, 8+100*68))},
		parityInput{name: "ssnd-offset.aiff", data: testfile.AIFF("AIFF", testfile.AIFFChunk("NAME", []byte("Old")),
			testfile.COMM(2, 1000, 16, 44100), testfile.SSNDOffset(4, []byte{0xFF, 0xFF, 0xFF, 0xFF}, audio)),
			edits: []parityEdit{tags("new title", setTag(tag.Title, "New"), tags("artist", setTag(tag.Artist, "Chained")))}},
		parityInput{name: "ape-binary.mp3", data: slices.Concat(v2Title, fixture["notags.mp3"], apeCover),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"))}},
		parityInput{name: "malformed-tail.mp3", data: slices.Concat(
			testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Song"), testfile.OverrunFrame("TALB")), fixture["notags.mp3"]),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "Band"))}},
		parityInput{name: "legacy-v1.mp3", data: slices.Concat(v2Title, fixture["notags.mp3"],
			testfile.ID3v1("V1 Different", "", "", "", "", 255))},
		parityInput{name: "malformed-apic.mp3", data: slices.Concat(
			testfile.ID3v2(4, testfile.ID3Frame(4, "APIC", []byte("\x00image/png"))), fixture["notags.mp3"])},
		parityInput{name: "qt-chapters-abc.m4a", data: testfile.MP4QTFile([]int{0, 3000, 6000}, []string{"A", "B", "C"}),
			edits: []parityEdit{chaps("two", setChapters(ch(0, "NEW1"), ch(sec(4), "NEW2")))}},
		parityInput{name: "qt-chapters-ab.m4a", data: testfile.MP4QTFile([]int{0, 5000}, []string{"A", "B"}),
			edits: []parityEdit{
				{kind: "clear chapters", name: "cleared", edit: clearChapters,
					then: []parityEdit{chaps("two", setChapters(ch(0, "New One"), ch(sec(2), "New Two")))}},
				chaps("edited", setChapters(ch(0, "Edited A"), ch(sec(4), "Edited B")),
					tags("grow title", setTag(tag.Title, strings.Repeat("A Long Title ", 6)))),
				tags("tagged", setTag(tag.Title, "Tagged"), chaps("one", setChapters(ch(0, "Added After Tag")))),
			}},
		parityInput{name: "tagged.m4a", data: testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T")),
			edits: []parityEdit{
				chaps("two", setChapters(ch(0, "A"), ch(sec(4), "B")),
					parityEdit{kind: "clear chapters", name: "cleared", edit: clearChapters}),
				chaps("three", setChapters(ch(0, "Intro"), ch(sec(5), "Body"), ch(sec(9), "Outro"))),
				chaps("unsorted", setChapters(ch(sec(6), "Third"), ch(0, "First"), ch(sec(3), "Second"))),
			}},
		parityInput{name: "multi-cluster.mka", data: testfile.MultiCluster(false, false), edits: []parityEdit{longTitle(40), longTitle(70000)}},
		parityInput{name: "front-cues.mka", data: testfile.MultiCluster(true, false), edits: []parityEdit{longTitle(40), longTitle(70000)}},
		parityInput{name: "narrow-segment.mka", data: testfile.MultiCluster(false, true), edits: []parityEdit{longTitle(70000)}},
		parityInput{name: "multi-scope.mka", data: testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags,
			testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "AA"), testfile.SimpleTag("ENCODER", "album-enc")),
			testfile.TagGroup(7, testfile.SimpleTag("ENCODER", "track-enc"), testfile.SimpleTag("COMPOSER", "TrackComposer")))),
			edits: []parityEdit{tags("clear encoder", func(_ *Document, e *Editor) { e.Clear(tag.Encoder) },
				tags("album", setTag(tag.Album, "NewAlbum")))}},
		parityInput{name: "echoed-artist.mka", data: testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags,
			testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "Solo"), testfile.SimpleTag("ARTIST", "Solo")),
			testfile.TagGroup(7, testfile.SimpleTag("ARTIST", "Solo")))),
			edits: []parityEdit{tags("album", setTag(tag.Album, "X"))}},
		parityInput{name: "scoped-title.mka", data: slices.Concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment,
			testfile.El(testfile.IDInfo, testfile.El(testfile.IDDuration, make([]byte, 8))), testfile.AudioCluster(0),
			testfile.El(testfile.IDTags, testfile.TagGroup(0, testfile.SimpleTag("TITLE", "MyTitle"), testfile.SimpleTag("ARTIST", "AA"))))),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "BB"))}},
		parityInput{name: "plain.mka", data: testfile.Matroska("matroska", "force-cover"),
			edits: []parityEdit{{kind: kindPictures, name: "unrecognized cover", opts: []WriteOption{WithUnrecognizedPictures()},
				edit: func(_ *Document, e *Editor) {
					e.AddPicture(Picture{Type: PicFrontCover, MIME: "application/octet-stream", Data: []byte("plain file, not an image")})
				}}}},
	)
}

// dropChapterCodec plans with the codec it wraps, then loses the result's last chapter, naming the
// loss with a discard warning when warn is set.
type dropChapterCodec struct {
	core.Codec
	warn bool
}

func (c dropChapterCodec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	wp, err := c.Codec.Plan(ctx, base, edited, opts)
	if err != nil {
		return nil, err
	}
	wp.Result = wp.Result.Clone()
	wp.Result.Chapters = wp.Result.Chapters[:len(wp.Result.Chapters)-1]
	if c.warn {
		wp.Report.Warnings = core.Warn(wp.Report.Warnings, core.WarnChaptersUnsupported, "a chapter was dropped")
	}
	return wp, nil
}

// TestPrepareChecksThePlannedResult: Prepare refuses a plan whose result silently lost a chapter the
// edit asked for, and accepts one whose report names the discard.
func TestPrepareChecksThePlannedResult(t *testing.T) {
	doc := mustParseParity(t, testdataBytes(t, "sample.flac"))
	flac, _ := core.ForFormat(FormatFLAC)
	t.Cleanup(func() { codecForFormat = core.ForFormat })
	for _, warn := range []bool{false, true} {
		codecForFormat = func(Format) (core.Codec, bool) { return dropChapterCodec{flac, warn}, true }
		_, err := doc.Edit().SetChapters(Chapter{Title: "A"}, Chapter{Start: time.Second, Title: "B"}).Prepare()
		switch {
		case warn && err != nil:
			t.Errorf("a discard-warned drop: err = %v, want the plan", err)
		case !warn && (!errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "requested chapters")):
			t.Errorf("a silent drop: err = %v, want ErrInvalidData naming the chapters", err)
		}
	}
}
