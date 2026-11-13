package waxlabel

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
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
	{kind: "comment", edit: func(_ *Document, e *Editor) {
		e.Set(tag.Comment, "Parity comment", "Second comment").Set(tag.Lyrics, "Parity lyrics")
	}},
	{kind: "described comment", edit: func(_ *Document, e *Editor) {
		e.SetComments(Comment{Text: "Described", Description: "Desc", Language: "fra"}, Comment{Text: "Plain"}, Comment{Text: "Also French", Language: "fra"})
		e.SetLyrics(Lyrics{Text: "Words", Description: "Translation", Language: "deu"}, Lyrics{Text: "More words"})
	}, then: []parityEdit{{kind: "requalified comment", edit: func(_ *Document, e *Editor) {
		e.SetComments(Comment{Text: "Described", Language: "spa"}, Comment{Text: "Plain", Description: "Now described"})
	}}}},
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
var knownDrift = map[driftKey]drift{}

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
// tags, pictures, chapters, synced lyrics, comment and lyrics items and output gain Plan.Changes
// diffs, and the essence ranges the read-back verifies.
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
	note("comments", result.Comments, written.Comments, slices.Equal(result.Comments, written.Comments))
	note("lyrics", result.Lyrics, written.Lyrics, slices.Equal(result.Lyrics, written.Lyrics))
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
		// A late first chapter, whose track media runs on to where the last chapter ends: at the
		// movie's end, short of it, off the movie timescale's grid, and a first start of one unit.
		chaps("late first start", setChapters(ch(sec(2), "A"), ch(sec(5), "B")),
			chaps("one more", setChapters(ch(sec(2), "A"), ch(sec(5), "B"), ch(sec(7), "C")))),
		chaps("late first start, last ends early", setChapters(ch(sec(2), "A"), Chapter{Start: sec(5), End: sec(8), Title: "B"})),
		chaps("late first start, end off the grid", setChapters(ch(sec(2.0004), "A"), Chapter{Start: sec(5), End: sec(8) + 400*time.Microsecond, Title: "B"})),
		chaps("first start of one unit", setChapters(ch(time.Millisecond, "A"), Chapter{Start: sec(5), End: sec(8), Title: "B"})),
		chaps("late chapter shorter than a unit", setChapters(Chapter{Start: sec(4), End: sec(4) + 100*time.Microsecond, Title: "Blip"})),
		chaps("first start of one unit, end between units", setChapters(ch(time.Millisecond, "A"), Chapter{Start: sec(5), End: sec(8) + 500*time.Microsecond, Title: "B"})),
		chaps("late first start in a book past the media field", setChapters(ch(10*time.Minute, "A"), Chapter{Start: 6 * time.Hour, End: 13*time.Hour + 20*time.Minute, Title: "B"})),
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
	// Bytes between a leading tag and the stream that no sniffer claims.
	gap := []byte{1, 2, 3}
	apeCover, err := ape.Render([]ape.Item{{Key: "Cover Art (front)", Data: []byte("\x89PNG binary"), Flags: 2}}, 2000, false, 0) // item type 1: binary
	if err != nil {
		t.Fatal(err)
	}
	// Tails after an APE-tagged stream: a duplicate tag holding a value the tag does not, junk, a
	// Lyrics3 block and stacked ID3v1 tags.
	pastEnd := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Cut"))
	copy(pastEnd[6:10], testfile.Syncsafe(1<<20)) // declares more bytes than any input holds
	dupTag, err := ape.Render([]ape.Item{{Key: "Composer", Value: "Only In The Duplicate"}}, 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	sv7Tag, err := ape.Render([]ape.Item{{Key: "Title", Value: "SV7 Title"}}, 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	cover := func(name string, n byte) ape.Item {
		return ape.Item{Key: name, Data: slices.Concat([]byte("art.png\x00"), testfile.PNG(), []byte{n}), Flags: 2}
	}
	roleTag, err := ape.Render([]ape.Item{{Key: "Title", Value: "Roles"}, cover("Cover Art (Artist)", 1), cover("COVER ART (MEDIA)", 2),
		cover("Cover Art (Front)", 3), cover("Artist Picture", 4), cover("Cover Art (Bogus)", 5), cover("Artist Picture", 1),
		{Key: "Cover Art (Booklet)", Data: []byte("no file name ends here"), Flags: 2}}, 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	oddTag, err := ape.Render([]ape.Item{{Key: "X", Value: "one"}, {Key: "Artist", Value: "A\x00\x00B\x00"},
		{Key: "Link", Data: []byte("http://example.com/a"), Flags: 4}, {Key: "Title", Value: "Odd"}, {Key: "Album", Value: "Past The Count"}}, 2000, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	// The header and the footer count four of the five items.
	binary.LittleEndian.PutUint32(oddTag[16:], 4)
	binary.LittleEndian.PutUint32(oddTag[len(oddTag)-16:], 4)
	tailJunk := bytes.Repeat([]byte{0xA5}, 300)
	v1a, v1b := testfile.ID3v1("Legacy One", "", "Legacy Album", "", "", 255), testfile.ID3v1("Legacy Two", "", "", "", "", 255)
	wv, mac := fixture["sample.wv"], fixture["sample.ape"]
	wvTag, macTag := apeTagOffset(t, wv), apeTagOffset(t, mac)
	// An SV7 stream has no audio end, so the junk after its tag stays in the essence.
	sv7 := slices.Concat([]byte("MP+\x07"), testfile.LE32(100), make([]byte, 16), make([]byte, 512))
	// MPEG frames with no VBR header, and so much junk after them that no frame chain sits in the
	// window before the tags: the audio end is unknown, and the junk after the tag stays in the essence.
	unended := slices.Concat(bytes.Repeat(append([]byte{0xFF, 0xFB, 0x90, 0x00}, make([]byte, 413)...), 40),
		bytes.Repeat([]byte{0xA5}, 70000))
	footerOnly, err := ape.Render([]ape.Item{{Key: "Album", Value: "Trailing Album"}}, 2000, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	strip := parityEdit{kind: "strip", edit: func(*Document, *Editor) {}, opts: []WriteOption{WithLegacyPolicy(LegacyStrip)}}
	ffmpegTag := testfile.ID3v2(4, testfile.TextFrame(4, "TSSE", "Lavf61.7.100"))
	// An APE tag longer than the MP3 resync window.
	windowTag, err := ape.Render([]ape.Item{{Key: "Comment", Value: strings.Repeat("x", 100<<10)}}, 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 200 frames of a stream whose Xing header counts 300 frames and their bytes, cut on a frame.
	truncated := bytes.Repeat(append([]byte{0xFF, 0xFB, 0x90, 0x00}, make([]byte, 413)...), 200)
	copy(truncated[36:], "Xing")
	truncated[43] = 3 // frame count and byte count present
	binary.BigEndian.PutUint32(truncated[44:], 300)
	binary.BigEndian.PutUint32(truncated[48:], 300*417)
	// A tag longer than what a frame cut short before it lacks, so the frame's declared end lies inside it.
	cutTag, err := ape.Render([]ape.Item{{Key: "Comment", Value: strings.Repeat("c", 600)}}, 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Where the last frame of notags.flac opens, its header coding frame 9's short block.
	flacLast := bytes.LastIndex(fixture["notags.flac"], []byte{0xFF, 0xF8, 0x79, 0x88, 0x09})
	if flacLast < 0 {
		t.Fatal("no last frame in notags.flac")
	}
	// A tag behind junk whose last two bytes bring that frame's checksum to zero inside the tag, where
	// the frame walk would end the audio: the tag ends it before, the junk ahead of the tag put back.
	behind, err := ape.Render([]ape.Item{{Key: "Title", Value: "Behind Junk"}}, 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	steered := testfile.FLACSteeringJunk(fixture["notags.flac"][flacLast:], behind, 42)
	if steered == nil {
		t.Fatal("no junk steers the checksum")
	}
	stripThenArtist := strip
	stripThenArtist.name, stripThenArtist.then = "strip, then artist", []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"))}
	// Ogg streams with bytes between their pages, among the header pages and the audio pages.
	gapped := func(name string) []byte {
		ogg := fixture[name]
		second, last := bytes.Index(ogg[4:], []byte("OggS"))+4, bytes.LastIndex(ogg, []byte("OggS"))
		return slices.Concat(ogg[:second], gap, ogg[second:last], tailJunk, ogg[last:])
	}
	oga := fixture["sample.oga"]
	// The first audio page's checksum fails: it reads only as the page that follows a header page.
	damaged := gapped("sample.oga")
	damaged[bytes.LastIndex(oga, []byte("OggS"))+len(gap)-1] ^= 0x01
	// MP4 atoms a file holds more than one of, the first read; a final mdat declared open-ended, which
	// a chapter write sizes; tables that place no chunk in any mdat; and bytes after the last atom,
	// which a chapter mdat goes ahead of.
	chpl := func(titles ...string) []byte {
		body := []byte{1, 0, 0, 0, 0, 0, 0, 0, byte(len(titles))}
		for i, title := range titles {
			body = binary.BigEndian.AppendUint64(body, uint64(i)*30_000_000)
			body = append(append(body, byte(len(title))), title...)
		}
		return testfile.MP4Atom("chpl", body)
	}
	udtaText := func(name, text string) []byte {
		return testfile.MP4Atom(name, slices.Concat(testfile.BE16(len(text)), testfile.BE16(0x55C4), []byte(text)))
	}
	mp4Tags := testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "T")))
	mp4Tagged := testfile.MP4AssembleUdta(mp4Tags)
	openMdat := slices.Clone(mp4Tagged)
	binary.BigEndian.PutUint32(openMdat[bytes.LastIndex(openMdat, []byte("mdat"))-4:], 0)
	freeform := func(name, value string) []byte {
		mean := testfile.MP4Atom("mean", append([]byte{0, 0, 0, 0}, "com.apple.iTunes"...))
		nm := testfile.MP4Atom("name", append([]byte{0, 0, 0, 0}, name...))
		return testfile.MP4Atom("----", slices.Concat(mean, nm, testfile.MP4Data(1, []byte(value))))
	}
	trkn := func(num, total byte) []byte {
		return testfile.MP4Atom("trkn", testfile.MP4Data(0, []byte{0, 0, 0, num, 0, total, 0, 0}))
	}
	frameCounted, _, _, _ := testfile.MP4FrameCountedFile(testfile.MP4StsdEntry("twos", 2, 16, 8000), 100, 1, 400)
	moovMeta := testfile.MP4Meta(testfile.MP4HdlrMdta(), testfile.MP4Keys("title", "artist", "location"),
		testfile.MP4Ilst(testfile.MP4KeyItem(1, "Meta Title"), testfile.MP4KeyItem(2, "Meta Artist"), testfile.MP4KeyItem(3, "+48.85+002.35/")))
	// moovFile is a movie of one sound track and kids.
	moovFile := func(trak func(int, uint32, ...[]byte) []byte, kids ...[]byte) []byte {
		return testfile.MP4TracksFile(nil, func(audio, _, _ uint32) []byte {
			return slices.Concat(testfile.MP4Mvhd(1000, 1000), trak(1, audio), slices.Concat(kids...))
		})
	}
	moovMetaEdits := []parityEdit{
		tags("new key", setTag(tag.Album, "New Album"), chaps("two", setChapters(ch(0, "A"), ch(msec(500), "B")))),
		chaps("two with tags", func(_ *Document, e *Editor) { e.SetChapters(ch(0, "A"), ch(msec(500), "B")).Set(tag.Genre, "Spoken") },
			tags("artist", setTag(tag.Artist, "A2")), parityEdit{kind: "clear chapters", name: "cleared", edit: clearChapters}),
		longTitle(70000),
	}
	strayStco := slices.Concat(testfile.MP4Ftyp(), testfile.MP4MoovStsd(nil, 4, nil, nil, testfile.MP4StsdAudio(), 44100),
		testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 120)))
	threeThenOne := chaps("three", setChapters(ch(0, "Opening"), ch(sec(3), "Middle"), ch(sec(7.5), "Closing")),
		chaps("one", setChapters(ch(0, "Only")), parityEdit{kind: "clear chapters", name: "cleared", edit: clearChapters}))
	return append(inputs,
		parityInput{name: "duplicate-chpl.m4a", data: testfile.MP4AssembleUdta(mp4Tags, chpl("One", "Two"), chpl("Old One", "Old Two")),
			edits: []parityEdit{tags("album", setTag(tag.Album, "Unrelated"), chaps("one", setChapters(ch(0, "Only"))))}},
		parityInput{name: "duplicate-chpl-track.m4a", data: testfile.MP4QTFile([]int{0, 3000}, []string{"A", "Caf\x8e"},
			chpl(), chpl("A", "Caf\x8e"), chpl("Stale"))},
		parityInput{name: "duplicate-udta-text.mov", data: testfile.MP4AssembleUdta(udtaText("\xa9nam", ""), udtaText("\xa9ART", "Artist"),
			udtaText("\xa9nam", "First"), udtaText("\xa9nam", "Second")),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), tags("clear title", func(_ *Document, e *Editor) { e.Clear(tag.Title) }))}},
		parityInput{name: "joined.m4a", data: slices.Concat(mp4Tagged, mp4Tagged), edits: []parityEdit{longTitle(70000), threeThenOne}},
		parityInput{name: "open-mdat.m4a", data: openMdat, edits: []parityEdit{threeThenOne}},
		parityInput{name: "stray-stco.m4a", data: strayStco, edits: []parityEdit{threeThenOne,
			chaps("two", setChapters(ch(0, "A"), ch(sec(3), "B")), longTitle(70000))}},
		parityInput{name: "id3v1-tail.m4a", data: slices.Concat(mp4Tagged, v1a), edits: []parityEdit{longTitle(70000), threeThenOne}},
		parityInput{name: "stray-tail.m4a", data: slices.Concat(mp4Tagged, []byte{0, 0, 0, 5}), edits: []parityEdit{threeThenOne}},
		// Titles that end at a NUL, in a list read as Windows-1252 and a track beside it.
		parityInput{name: "nul-titles.m4a", data: testfile.MP4QTFile([]int{0, 3000}, []string{"Intro\x00", "Caf\xe9\x00\x00"},
			chpl("Intro\x00", "Caf\xe9\x00\x00"))},
		// Chapter tracks named beside a video track of thumbnails, from the video track alone, and
		// created in a file of both; UTF-16 titles beside a UTF-8 list; a track title the list fills.
		parityInput{name: "thumbnails.m4a", data: testfile.MP4TracksFile([]string{"One", "Two"}, func(audio, text, thumbs uint32) []byte {
			return slices.Concat(testfile.MP4VideoTrak(3, thumbs), testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(99, 2, 3)),
				testfile.MP4TextTrak(2, 1000, []int{0, 3000}, []string{"One", "Two"}, text))
		}), edits: []parityEdit{threeThenOne}},
		parityInput{name: "video-chap.m4a", data: testfile.MP4TracksFile([]string{"One", "Two"}, func(audio, text, thumbs uint32) []byte {
			return slices.Concat(testfile.MP4SoundTrak(1, audio), testfile.MP4VideoTrak(3, thumbs, testfile.MP4TrefChap(2)),
				testfile.MP4TextTrak(2, 1000, []int{0, 3000}, []string{"One", "Two"}, text))
		}), edits: []parityEdit{threeThenOne}},
		parityInput{name: "sound-and-video.m4a", data: testfile.MP4TracksFile(nil, func(audio, _, thumbs uint32) []byte {
			return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(3)), testfile.MP4VideoTrak(3, thumbs))
		}), edits: []parityEdit{threeThenOne}},
		// A chapter track of track id 0; a text track only its own list names; an edit list that
		// ends before the last chapter starts; and a movie timescale so fine that a long file's
		// edit segment clamps.
		parityInput{name: "chapter-track-id-0.m4a", data: testfile.MP4TracksFile([]string{"One", "Two"}, func(audio, text, _ uint32) []byte {
			return slices.Concat(testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(0)), testfile.MP4TextTrak(0, 1000, []int{0, 3000}, []string{"One", "Two"}, text))
		}), edits: []parityEdit{threeThenOne}},
		parityInput{name: "self-named.m4a", data: testfile.MP4TracksFile([]string{"One", "Two"}, func(audio, text, _ uint32) []byte {
			own := testfile.MP4TextTrak(2, 1000, []int{0, 3000}, []string{"One", "Two"}, text)
			tkhd := 8 + int(binary.BigEndian.Uint32(own[8:]))
			own = slices.Concat(own[:tkhd], testfile.MP4TrefChap(2), own[tkhd:])
			binary.BigEndian.PutUint32(own, uint32(len(own)))
			return slices.Concat(testfile.MP4SoundTrak(1, audio), own)
		}), edits: []parityEdit{threeThenOne}},
		parityInput{name: "short-edit.m4a", data: testfile.MP4TracksFile([]string{"One", "Two", "Three"}, func(audio, text, _ uint32) []byte {
			cut := testfile.MP4TextTrak(2, 1000, []int{0, 3000, 8000, 10000}, []string{"One", "Two", "Three"}, text)
			tkhd := 8 + int(binary.BigEndian.Uint32(cut[8:]))
			cut = slices.Concat(cut[:tkhd], testfile.MP4Edts([2]int64{5000, 0}), cut[tkhd:])
			binary.BigEndian.PutUint32(cut, uint32(len(cut)))
			return slices.Concat(testfile.MP4Mvhd(1000, 10000), testfile.MP4SoundTrak(1, audio, testfile.MP4TrefChap(2)), cut)
		})},
		parityInput{name: "fine-timescale.m4a", data: testfile.MP4TracksFile(nil, func(audio, _, _ uint32) []byte {
			return slices.Concat(testfile.MP4Mvhd(1_000_000, 2*3600*1_000_000), testfile.MP4SoundTrak(1, audio))
		}), edits: []parityEdit{
			chaps("from zero past the edit clamp", setChapters(ch(0, "A"), ch(time.Hour, "B")), chaps("one more", setChapters(ch(0, "A"), ch(time.Hour, "B"), ch(90*time.Minute, "C")))),
			chaps("late start past the edit clamp", setChapters(ch(time.Minute, "A"), ch(90*time.Minute, "B"))),
			chaps("late start under the edit clamp", setChapters(ch(time.Minute, "A"), Chapter{Start: 30 * time.Minute, End: time.Hour, Title: "B"})),
		}},
		parityInput{name: "utf16-titles.m4a", data: testfile.MP4QTFile([]int{0, 3000}, []string{"\xfe\xff\x00C\x00a\x00f\x00\xe9", "\xff\xfeB\x00"},
			chpl("Caf\u00e9", "B"))},
		parityInput{name: "empty-track-title.m4a", data: testfile.MP4QTFile([]int{0, 3000}, []string{"", "Two"}, chpl("One", "Two"))},
		// An mdta keys store under ffmpeg's names, and stores where two items hold one key: the first
		// is read, and the second stays until its key is edited.
		parityInput{name: "mdta-ffmpeg-names.m4a", data: testfile.MP4MdtaFile(
			[]string{"title", "album_artist", "track", "disc", "sort_name", "compilation"}, []string{"T", "AA", "3/12", "1/2", "SN", "1"}),
			edits: []parityEdit{
				tags("track number", setTag(tag.TrackNumber, "5"), tags("no total", func(_ *Document, e *Editor) { e.Clear(tag.TrackTotal) })),
				tags("new keys", func(_ *Document, e *Editor) { e.Set(tag.Show, "SH").Set(tag.DiscTotal, "3").Clear(tag.TrackNumber) }),
				chaps("two", setChapters(ch(0, "A"), ch(sec(4), "B")), tags("album artist", setTag(tag.AlbumArtist, "BB"))),
			}},
		parityInput{name: "mdta-key-twice.m4a", data: testfile.MP4MdtaFile(
			[]string{"software", "creationdate", "date", "encoder", "title"}, []string{"QuickTime 10", "2021-05-04", "2020", "Lavf60", "T"}),
			edits: []parityEdit{
				tags("encoder", setTag(tag.Encoder, "New"), tags("date", setTag(tag.RecordingDate, "2022"))),
				tags("the same encoder", setTag(tag.Encoder, "QuickTime 10")),
				chaps("two", setChapters(ch(0, "A"), ch(sec(4), "B"))),
			}},
		// Cover art in a keys store: under the key WaxLabel once wrote, and in a four-character item.
		parityInput{name: "mdta-covr-key.m4a", data: testfile.MP4Assemble(testfile.MP4HdlrMdta(), testfile.MP4Keys("title", "covr"),
			testfile.MP4Ilst(testfile.MP4KeyItem(1, "T"), testfile.MP4KeyItemData(2, testfile.MP4Data(13, testfile.JPEG())))),
			edits: []parityEdit{pics("back cover", addCover(PicBackCover), tags("artist", setTag(tag.Artist, "A")))}},
		parityInput{name: "mdta-covr-item.m4a", data: testfile.MP4Assemble(testfile.MP4HdlrMdta(), testfile.MP4Keys("title"),
			testfile.MP4Ilst(testfile.MP4KeyItem(1, "T"), testfile.MP4Atom("covr", testfile.MP4Data(13, testfile.JPEG())))),
			edits: []parityEdit{pics("no cover", func(_ *Document, e *Editor) { e.ClearPictures() })}},
		// Encodings of the iTunes atoms no rebuild renders: a trkn with no total field, a disk with
		// a trailing pad, an unsigned stik, an implicit-typed text.
		parityInput{name: "itunes-other-encodings.m4a", data: testfile.MP4Tagged(
			testfile.MP4Atom("trkn", testfile.MP4Data(0, []byte{0, 0, 0, 7})), testfile.MP4Atom("disk", testfile.MP4Data(0, []byte{0, 0, 0, 1, 0, 2, 0, 0})),
			testfile.MP4Atom("stik", testfile.MP4Data(22, []byte{2})), testfile.MP4Atom("\xa9alb", testfile.MP4Data(0, []byte("Album")))),
			edits: []parityEdit{tags("track total", setTag(tag.TrackTotal, "9")), tags("nothing storable", setTag(tag.BPM, "abc"))}},
		// Bytes behind the last atom whose size field forms none; udta text atoms beside an ilst,
		// which a value they cannot hold leaves; the iTunes TV atoms.
		parityInput{name: "junk-tail.m4a", data: slices.Concat(mp4Tagged, []byte("\x00\x00\x00\x05junk")), edits: []parityEdit{longTitle(70000), threeThenOne}},
		parityInput{name: "cut-64-bit-tail.m4a", data: slices.Concat(mp4Tagged, []byte("\x00\x00\x00\x01junk")), edits: []parityEdit{threeThenOne}},
		parityInput{name: "udta-texts-and-ilst.mov", data: testfile.MP4AssembleUdta(udtaText("\xa9cmt", "Old"), udtaText("\xa9ART", "Old"),
			testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9cmt", "Old"), testfile.MP4Text("\xa9ART", "Old")))),
			edits: []parityEdit{
				tags("long comment", setTag(tag.Comment, strings.Repeat("c", 70000)), tags("short again", setTag(tag.Comment, "c"))),
				tags("two artists", setTag(tag.Artist, "One", "Two")),
				chaps("two with a long comment", func(_ *Document, e *Editor) {
					e.SetChapters(ch(0, "A"), ch(msec(500), "B")).Set(tag.Comment, strings.Repeat("c", 70000))
				}),
			}},
		parityInput{name: "tv-atoms.m4a", data: testfile.MP4Tagged(testfile.MP4Text("tvsh", "Show"), testfile.MP4Text("sosn", "Show, The"),
			testfile.MP4Atom("tves", testfile.MP4Data(21, []byte{0, 0, 0, 2})), testfile.MP4Atom("tvsn", testfile.MP4Data(21, []byte{0, 0, 0, 1}))),
			edits: []parityEdit{tags("episode", setTag(tag.EpisodeNumber, "3")), tags("episode as text", setTag(tag.EpisodeNumber, "3a")),
				tags("network", func(_ *Document, e *Editor) { e.Set(tag.Network, "Net").Set(tag.EpisodeID, "S01E03").Clear(tag.Show) })}},
		// Values an item holds without being read for them: a year beside its date, a total beside
		// the pair's, a second store's total; an item past the keys table; a track that counts frames.
		parityInput{name: "year-and-date.m4a", data: testfile.MP4Tagged(freeform("originalyear", "1999"), freeform("originaldate", "1999-05-01")),
			edits: []parityEdit{tags("date", setTag(tag.OriginalDate, "2001-02-03"))}},
		parityInput{name: "total-beside-pair.m4a", data: testfile.MP4Tagged(freeform("TRACKTOTAL", "15"), trkn(3, 12)),
			edits: []parityEdit{tags("number", setTag(tag.TrackNumber, "5")), tags("nothing storable", setTag(tag.TrackNumber, "abc")),
				tags("the same total", setTag(tag.TrackTotal, "15"))}},
		parityInput{name: "mdta-total-twice.m4a", data: testfile.MP4MdtaFile([]string{"TOTALTRACKS", "track", "title"}, []string{"15", "3/12", "T"}),
			edits: []parityEdit{tags("number", setTag(tag.TrackNumber, "5")), tags("the same total", setTag(tag.TrackTotal, "15"))}},
		parityInput{name: "two-stores-pair.m4a", data: moovFile(testfile.MP4SoundTrak,
			testfile.MP4Atom("udta", testfile.MP4Meta(testfile.MP4HdlrMdta(), testfile.MP4Keys("track"), testfile.MP4Ilst(testfile.MP4KeyItem(1, "3/12")))),
			testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(trkn(3, 15), testfile.MP4Text("\xa9ART", "Meta Artist")))),
			edits: []parityEdit{tags("number", setTag(tag.TrackNumber, "5"))}},
		parityInput{name: "second-store-own-keys.m4a", data: moovFile(testfile.MP4SoundTrak, testfile.MP4Atom("udta", mp4Tags),
			testfile.MP4Meta(testfile.MP4HdlrMdta(), testfile.MP4Keys("title", "genre", "com.apple.quicktime.genre", "artist"),
				testfile.MP4Ilst(testfile.MP4KeyItem(1, "Meta Title"), testfile.MP4KeyItem(2, "Rock"), testfile.MP4KeyItem(3, "Pop"), testfile.MP4KeyItem(4, "Meta Artist")))),
			edits: []parityEdit{tags("its own artist", setTag(tag.Artist, "New Artist")), tags("the title it has", setTag(tag.Title, "T")),
				tags("genre", setTag(tag.Genre, "Jazz")), chaps("two with an artist", func(_ *Document, e *Editor) {
					e.SetChapters(ch(0, "A"), ch(msec(500), "B")).Set(tag.Artist, "A2")
				})}},
		parityInput{name: "mdta-lone-total.m4a", data: testfile.MP4MdtaFile([]string{"title", "track", "SOFTWARE"}, []string{"T", "/12", "x"}),
			edits: []parityEdit{tags("number", setTag(tag.TrackNumber, "5"))}},
		parityInput{name: "mdta-orphan-index.m4a", data: testfile.MP4Assemble(testfile.MP4HdlrMdta(), testfile.MP4Keys("title", "artist"),
			testfile.MP4Ilst(testfile.MP4KeyItem(1, "T"), testfile.MP4KeyItem(3, "Orphan"))),
			edits: []parityEdit{tags("new keys", func(_ *Document, e *Editor) { e.Set(tag.Genre, "G").Set(tag.Album, "A") })}},
		parityInput{name: "frame-counted.mov", data: frameCounted, edits: []parityEdit{longTitle(70000), threeThenOne}},
		parityInput{name: "text-atoms.mov", data: testfile.MP4AssembleUdta(udtaText("\xa9nam", "T"), udtaText("\xa9ART", "A")),
			edits: []parityEdit{
				{kind: "padding floor", edit: func(*Document, *Editor) {}, opts: []WriteOption{WithPadding(PaddingPolicy{Target: 4096, Min: 4096, ReuseInPlace: true})}},
				{kind: "cleared with a floor", edit: func(_ *Document, e *Editor) { e.Clear(tag.Title).Clear(tag.Artist) },
					opts: []WriteOption{WithPadding(PaddingPolicy{Target: 4096, Min: 4096, ReuseInPlace: true})}},
			}},
		parityInput{name: "gnre-and-text.m4a", data: testfile.MP4Tagged(testfile.MP4Atom("gnre", testfile.MP4Data(0, []byte{0, 18})),
			testfile.MP4Text("\xa9gen", "Custom"), testfile.MP4Text("\xa9nam", "T")),
			edits: []parityEdit{tags("genre", setTag(tag.Genre, "Jazz"))}},
		// Sound and video chunks interleaved in one mdat, placed by full sample tables: the essence is
		// the sound track's chunks, which every edit moves whole.
		parityInput{name: "interleaved.mp4", data: testfile.MP4AVFile(
			[][]byte{bytes.Repeat([]byte{0xA1}, 100), bytes.Repeat([]byte{0xA2}, 60)}, [][]byte{bytes.Repeat([]byte{0xB1}, 300), bytes.Repeat([]byte{0xB2}, 80)}),
			edits: []parityEdit{longTitle(70000), threeThenOne, chaps("two with tags", func(_ *Document, e *Editor) {
				e.SetChapters(ch(0, "A"), ch(msec(500), "B")).Set(tag.Genre, "Spoken")
			}, tags("artist", setTag(tag.Artist, "A2")), parityEdit{kind: "clear chapters", name: "cleared", edit: clearChapters})}},
		parityInput{name: "interleaved-moov-meta.mp4", data: testfile.MP4AVFile(
			[][]byte{bytes.Repeat([]byte{0xA1}, 100), bytes.Repeat([]byte{0xA2}, 60)}, [][]byte{bytes.Repeat([]byte{0xB1}, 300)}, moovMeta),
			edits: moovMetaEdits},
		// A tag store in a meta under moov: alone, ahead of the track whose tables then move with it,
		// beside udta text atoms, and beside a store under moov.udta.meta, which is the one written.
		parityInput{name: "moov-meta.mov", data: moovFile(testfile.MP4SoundTrak, moovMeta), edits: moovMetaEdits},
		parityInput{name: "moov-meta-first.mov", data: testfile.MP4TracksFile(nil, func(audio, _, _ uint32) []byte {
			return slices.Concat(testfile.MP4Mvhd(1000, 1000), moovMeta, testfile.MP4SoundTrak(1, audio))
		}), edits: moovMetaEdits},
		parityInput{name: "moov-meta-mdir.m4a", data: moovFile(testfile.MP4SoundTrak, testfile.MP4Meta(testfile.MP4HdlrMdir(),
			testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "T")), testfile.MP4Atom("free", make([]byte, 40)))), edits: moovMetaEdits},
		parityInput{name: "moov-meta-udta-texts.mov", data: moovFile(testfile.MP4SoundTrak,
			testfile.MP4Atom("udta", udtaText("\xa9nam", "Udta Title")), moovMeta), edits: moovMetaEdits},
		parityInput{name: "moov-meta-and-udta.m4a", data: moovFile(testfile.MP4SoundTrak, moovMeta, testfile.MP4Atom("udta", mp4Tags)),
			edits: append([]parityEdit{tags("no artist", func(_ *Document, e *Editor) { e.Clear(tag.Artist) })}, moovMetaEdits...)},
		parityInput{name: "text-and-gnre.m4a", data: testfile.MP4Tagged(testfile.MP4Text("\xa9gen", "Rock"),
			testfile.MP4Atom("gnre", testfile.MP4Data(0, []byte{0, 9}))),
			edits: []parityEdit{{kind: "numeric genre", edit: func(*Document, *Editor) {}, opts: []WriteOption{WithNumericGenre()}}}},
		// Ogg behind a leading tag and bytes, ahead of junk and stacked tags; with bytes between its
		// pages, renumbered around them; and ending inside its last page ahead of a tag.
		parityInput{name: "legacy-tags.ogg", data: slices.Concat(v2Title, gap, fixture["sample.ogg"], tailJunk, v1a, v1b),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip), stripThenArtist, longTitle(70000)}},
		parityInput{name: "gapped.ogg", data: gapped("sample.ogg"), edits: []parityEdit{longTitle(70000)}},
		parityInput{name: "gapped.opus", data: gapped("sample.opus"), edits: []parityEdit{longTitle(70000)}},
		parityInput{name: "gapped-damaged.oga", data: damaged, edits: []parityEdit{longTitle(70000)}},
		parityInput{name: "cut-page.oga", data: slices.Concat(oga[:len(oga)-100], v1a),
			edits: []parityEdit{longTitle(70000), tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
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
		// Tails after FLAC frames, every tag legacy: stacked tags around junk; a tag the frames go on past,
		// which ends the audio, the frames after it kept in the essence; zeros past the first window.
		parityInput{name: "legacy-tail.flac", data: slices.Concat(fixture["sample.flac"], dupTag, tailJunk,
			testfile.Lyrics3v2([2]string{"LYR", "words"}), v1a, v1b),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		parityInput{name: "between-frames.flac", data: slices.Concat(fixture["notags.flac"][:flacLast], dupTag, fixture["notags.flac"][flacLast:], v1a),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		parityInput{name: "zero-run.flac", data: slices.Concat(fixture["sample.flac"], make([]byte, 100<<10), v1a),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		parityInput{name: "steered-tail.flac", data: slices.Concat(fixture["notags.flac"], steered, behind, tailJunk),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip), stripThenArtist}},
		parityInput{name: "duplicate-inam.wav", data: testfile.WAV(testfile.WAVFmtPCM(),
			testfile.WAVInfo([2]string{"INAM", "Title A"}, [2]string{"INAM", "Title B"}), testfile.WAVData(400)),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"))}},
		parityInput{name: "two-info.wav", data: testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "First"}),
			testfile.WAVInfo([2]string{"INAM", "Second"}), testfile.WAVData(400))},
		parityInput{name: "chaptered.mpc", data: testfile.MPCChaptered()},
		parityInput{name: "legacy-tail.wv", data: slices.Concat(wv[:wvTag], dupTag, tailJunk, wv[wvTag:],
			testfile.Lyrics3v2([2]string{"LYR", "words"}), v1a, v1b)},
		parityInput{name: "legacy-tail-sv7.mpc", data: slices.Concat(sv7, sv7Tag, tailJunk, testfile.Lyrics3v1("la la"), v1a)},
		parityInput{name: "id3v1-ahead-of-tag.ape", data: slices.Concat(mac[:macTag], v1a, mac[macTag:])},
		// Cover items under the names of several taggers, one image under two names of a role and one
		// item that does not decode among them; a picture edit keeps the items it does not change and
		// names an added picture for its role, and a clear removes them all.
		parityInput{name: "cover-roles.wv", data: slices.Concat(wv[:wvTag], roleTag),
			edits: []parityEdit{
				pics("add a conductor", addCover(PicConductor)),
				pics("add an artist over the file's", addCover(PicArtist)),
				pics("remove the front cover", func(_ *Document, e *Editor) {
					e.RemovePictures(func(p Picture) bool { return p.Type == PicFrontCover })
				}),
				pics("described picture", func(_ *Document, e *Editor) {
					e.AddPicture(Picture{Type: PicBand, Description: "on stage", Data: testfile.PNG()})
				}),
				pics("clear pictures", func(_ *Document, e *Editor) { e.ClearPictures() }),
			}},
		// Items an edit must leave as they are or cannot write whole: a name under two characters, an
		// external locator, empty values between and behind others, and item bytes past the footer's
		// count, in a tag marked read-only, which a legacy strip of the ID3v1 tag behind it copies.
		parityInput{name: "odd-items.ape", data: slices.Concat(mac[:macTag], oddTag, v1a),
			edits: []parityEdit{
				tags("the short name", setTag("X", "two")),
				strip,
				tags("empty values", setTag(tag.Composer, "", "C", ""), tags("artist", setTag(tag.Artist, "Again"))),
			}},
		// A second WavPack stream after the first, which a rewrite moves up behind the first's blocks.
		parityInput{name: "joined.wv", data: slices.Concat(wv, fixture["notags.wv"])},
		parityInput{name: "leading-id3.wv", data: slices.Concat(v2Title, fixture["sample.wv"])},
		parityInput{name: "leading-id3.ape", data: slices.Concat(v2Title, fixture["sample.ape"])},
		parityInput{name: "leading-id3-gap.wv", data: slices.Concat(v2Title, gap, fixture["sample.wv"])},
		parityInput{name: "leading-id3-gap.flac", data: slices.Concat(v2Title, gap, fixture["sample.flac"])},
		parityInput{name: "gap.aac", data: slices.Concat(v2Title, gap, fixture["notags.aac"])},
		parityInput{name: "legacy-tail.aac", data: slices.Concat(v2Title, gap, fixture["notags.aac"], tailJunk, footerOnly,
			testfile.Lyrics3v1("la la"), v1a),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		parityInput{name: "legacy-tail.mp3", data: slices.Concat(v2Title, gap, fixture["notags.mp3"], dupTag, tailJunk,
			testfile.Lyrics3v2([2]string{"LYR", "words"}), v1a, v1b),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		parityInput{name: "junk-after-tag.mp3", data: slices.Concat(v2Title, unended, footerOnly, tailJunk, v1a),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		// Two files joined with cat, each with the tag ffmpeg writes ahead of it: the second stream and
		// the tag between stay audio.
		parityInput{name: "joined-tagged.mp3", data: slices.Concat(ffmpegTag, fixture["notags.mp3"], ffmpegTag, fixture["notags.mp3"], v1a),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		parityInput{name: "joined-tagged.aac", data: slices.Concat(ffmpegTag, fixture["notags.aac"], ffmpegTag, fixture["notags.aac"], v1a),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		// Streams whose last frame the tags cut short: the cut frame stays audio.
		parityInput{name: "cut-frame.mp3", data: slices.Concat(v2Title, unended[:40*417-200], footerOnly, v1a),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		parityInput{name: "cut-frame.aac", data: slices.Concat(v2Title, fixture["notags.aac"][:len(fixture["notags.aac"])-100], footerOnly, v1a),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		// A truncated stream: the tag after its last whole frame is read, and the junk after the tag is
		// no audio.
		parityInput{name: "truncated-tag.mp3", data: slices.Concat(v2Title, truncated, dupTag, tailJunk[:10]),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		// Streams cut inside a frame, then a tag the frame's declared end lies in: the tag ends the audio.
		parityInput{name: "cut-frame-tag.mp3", data: slices.Concat(v2Title, truncated[:199*417+300], cutTag, tailJunk[:10]),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		parityInput{name: "cut-frame-tag.aac", data: slices.Concat(v2Title, fixture["notags.aac"][:len(fixture["notags.aac"])-100], cutTag, tailJunk[:10]),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		// A second tag chained behind the front one: a duplicate, which every rewrite drops.
		parityInput{name: "second-tag.aac", data: slices.Concat(v2Title, testfile.ID3v2(3, testfile.TextFrame(3, "TALB", "Second Tag")),
			fixture["notags.aac"])},
		parityInput{name: "second-tag-gap.mp3", data: slices.Concat(v2Title, testfile.ID3v2(3, testfile.TextFrame(3, "TALB", "Second Tag")),
			gap, fixture["notags.mp3"], v1a)},
		// The kept tag behind an empty one, a duplicate holding a frame the canonical view does not
		// carry, and a ninth chained tag past the chain's bound.
		parityInput{name: "empty-ahead.mp3", data: slices.Concat(testfile.ID3v2(3), v2Title, fixture["notags.mp3"])},
		parityInput{name: "empty-ahead.aac", data: slices.Concat(testfile.ID3v2(3), v2Title, fixture["notags.aac"])},
		parityInput{name: "popm-dup.mp3", data: slices.Concat(v2Title,
			testfile.ID3v2(3, testfile.ID3Frame(3, "POPM", []byte("me@example.com\x00\xC8"))), fixture["notags.mp3"])},
		parityInput{name: "nine-tags.mp3", data: slices.Concat(v2Title, v2Title, v2Title, v2Title, v2Title, v2Title, v2Title, v2Title, v2Title,
			fixture["notags.mp3"])},
		// Front tags the frame walk reads in part or not at all: one past the end of the file, one of a
		// version no specification defines, a lowercase frame ID, a control byte in an ID, a header the
		// tag ends inside; and ones it reads whole: plain v2.4 sizes, grouped and status-flagged frames.
		parityInput{name: "past-end-tag.mp3", data: slices.Concat(pastEnd, fixture["notags.mp3"])},
		parityInput{name: "v25-tag.mp3", data: slices.Concat(testfile.ID3v2(5, testfile.TextFrame(4, "TIT2", "Future")), fixture["notags.mp3"])},
		parityInput{name: "v25-tag.aac", data: slices.Concat(testfile.ID3v2(5, testfile.TextFrame(4, "TIT2", "Future")), fixture["notags.aac"])},
		parityInput{name: "lowercase-id.mp3", data: slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "Tlow", "odd"),
			testfile.TextFrame(3, "TPE1", "After")), fixture["notags.mp3"])},
		parityInput{name: "control-id.mp3", data: slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Song"),
			testfile.TextFrame(3, "T\x01LW", "junk")), fixture["notags.mp3"])},
		parityInput{name: "cut-header.aac", data: slices.Concat(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Song"), []byte("TPE")),
			fixture["notags.aac"])},
		parityInput{name: "plain-v24.mp3", data: slices.Concat(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Song"),
			slices.Concat([]byte("COMM"), testfile.BE32(204), []byte{0, 0}, []byte("\x00eng\x00"), bytes.Repeat([]byte("c"), 199))),
			fixture["notags.mp3"])},
		parityInput{name: "grouped.mp3", data: slices.Concat(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Song"),
			testfile.ID3FrameFlags(4, "TPE1", [2]byte{0x40, 0x40}, []byte("\x07\x00Grouped"))), fixture["notags.mp3"])},
		// A tag between two concatenated streams: the second stays in the essence around the tag, which a
		// strip joins to the first; a tag filling the resync window with junk after it.
		parityInput{name: "joined.mp3", data: slices.Concat(v2Title, fixture["notags.mp3"], dupTag, fixture["notags.mp3"], v1a),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		parityInput{name: "joined.aac", data: slices.Concat(v2Title, fixture["notags.aac"], dupTag, fixture["notags.aac"], v1a),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
		parityInput{name: "window-tag.mp3", data: slices.Concat(v2Title, unended[:40*417], windowTag, tailJunk[:10]),
			edits: []parityEdit{tags("artist", setTag(tag.Artist, "New Artist"), strip)}},
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
		// Tagged by level: the album's TITLE and ARTIST at level 50, the track's at level 30.
		parityInput{name: "level-tagged.mka", data: mkaNoTitle(testfile.El(testfile.IDTags, mkaAlbumTag(), mkaLevelTag(30,
			testfile.SimpleTag("TITLE", "Track Title"), testfile.SimpleTag("ARTIST", "Track Artist"), testfile.SimpleTag("PART_NUMBER", "3/12")))),
			edits: []parityEdit{
				tags("title", setTag(tag.Title, "New Title")),
				tags("album", setTag(tag.Album, "New Album"), tags("album kept and added", setTag(tag.Album, "New Album", "Second"))),
				tags("album artist", setTag(tag.AlbumArtist, "New Album Artist")),
				tags("artist", setTag(tag.Artist, "One", "Track Artist")),
				tags("track number", setTag(tag.TrackNumber, "4")),
				tags("clear title", func(_ *Document, e *Editor) { e.Clear(tag.Title) }, tags("title again", setTag(tag.Title, "Back"))),
				tags("clear album", func(_ *Document, e *Editor) { e.Clear(tag.Album) }),
				tags("clear title, set artist", func(_ *Document, e *Editor) { e.Clear(tag.Title).Set(tag.Artist, "One") }),
			}},
		// The same with an Info.Title too: a title edit writes both.
		parityInput{name: "level-tagged-info.mka", data: testfile.Matroska("matroska", "Track Title", testfile.El(testfile.IDTags, mkaAlbumTag(),
			mkaLevelTag(30, testfile.SimpleTag("TITLE", "Track Title")))),
			edits: []parityEdit{
				tags("title", setTag(tag.Title, "New Title")),
				tags("artist", setTag(tag.Artist, "Track Artist"), tags("artist again", setTag(tag.Artist, "Other"))),
				tags("clear title", func(_ *Document, e *Editor) { e.Clear(tag.Title) }),
			}},
		// An Info.Title beside album-level tags alone: not tagged by level, so the TITLE and ARTIST
		// are the file's own.
		parityInput{name: "info-title-album-level.mka", data: testfile.Matroska("matroska", "Track Title", testfile.El(testfile.IDTags, mkaAlbumTag())),
			edits: []parityEdit{
				tags("title", setTag(tag.Title, "New Title")),
				tags("artist", setTag(tag.Artist, "Track Artist"), tags("artist again", setTag(tag.Artist, "Other"))),
				tags("album", setTag(tag.Album, "Record")),
				tags("clear title", func(_ *Document, e *Editor) { e.Clear(tag.Title) }),
			}},
		// The same with the album named outright.
		parityInput{name: "named-album.mka", data: testfile.Matroska("matroska", "Band - Song", testfile.El(testfile.IDTags, mkaLevelTag(50,
			testfile.SimpleTag("TITLE", "Song"), testfile.SimpleTag("ARTIST", "Band"), testfile.SimpleTag("ALBUM", "Record")))),
			edits: []parityEdit{
				tags("clear album", func(_ *Document, e *Editor) { e.Clear(tag.Album) }),
				tags("artist", setTag(tag.Artist, "Other Band")),
			}},
		// Tags of an attachment, a chapter, an edition and a video track beside the file's own,
		// and attachments a picture edit keeps.
		parityInput{name: "targeted.mka", data: testfile.Matroska("matroska", "Song",
			testfile.El(testfile.IDTracks, mkaTrack(1, 1, "V_MPEG4/ISO/AVC"), mkaTrack(2, 2, "A_FLAC")),
			testfile.El(mkaAttachments,
				mkaAttached("Booklet page 1.jpg", "image/jpeg", testfile.JPEG(), 0x99),
				mkaAttached("notes.txt", "text/plain", []byte("liner notes"), 7),
				mkaAttached("logo.svg", "image/svg+xml", []byte("<svg/>"), 8)),
			testfile.El(testfile.IDTags,
				mkaTargeted(mkaTagAttachUID, 0x99, testfile.SimpleTag("TITLE", "Booklet"), testfile.SimpleTag("ARTIST", "Scanner")),
				testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "File Artist"), testfile.SimpleTag("COMMENT", "File note")),
				mkaTargeted(mkaTagChapUID, 11, testfile.SimpleTag("ARTIST", "Chapter Artist"), testfile.SimpleTag("TITLE", "Chapter One")),
				mkaTargeted(mkaTagEditUID, 5, testfile.SimpleTag("TITLE", "Edition")),
				mkaTargeted(testfile.IDTagTrackUID, 1, testfile.SimpleTag("ENCODER", "Lavc61 libx264")),
				mkaTargeted(testfile.IDTagTrackUID, 2, testfile.SimpleTag("ENCODER", "flac 1.4")))),
			edits: []parityEdit{
				tags("artist", setTag(tag.Artist, "New Artist")),
				tags("clear encoder", func(_ *Document, e *Editor) { e.Clear(tag.Encoder) }),
				{kind: kindPictures, name: "remove a picture", edit: func(_ *Document, e *Editor) {
					e.RemovePictures(func(p Picture) bool { return p.MIME == "image/jpeg" })
				}},
				{kind: kindPictures, name: "replace a picture", edit: func(_ *Document, e *Editor) {
					e.ReplacePictures(func(p Picture) bool { return p.MIME == "image/jpeg" }, Picture{Type: PicFrontCover, Data: testfile.PNG()})
				}},
			}},
		// A title in two languages as TITLE SimpleTags, and an artist with what a flat SimpleTag
		// cannot hold.
		parityInput{name: "titles-and-qualifiers.mka", data: mkaNoTitle(testfile.El(testfile.IDTags, mkaLevelTag(50,
			testfile.El(testfile.IDSimpleTag, testfile.El(testfile.IDTagName, []byte("TITLE")), testfile.El(mkaTagLang, []byte("eng")), testfile.El(testfile.IDTagString, []byte("Song"))),
			testfile.El(testfile.IDSimpleTag, testfile.El(testfile.IDTagName, []byte("TITLE")), testfile.El(mkaTagLang, []byte("ger")), testfile.El(testfile.IDTagString, []byte("Lied"))),
			testfile.El(testfile.IDSimpleTag, testfile.El(testfile.IDTagName, []byte("ARTIST")), testfile.El(mkaTagLangBCP47, []byte("de-DE")),
				testfile.Uint(mkaTagDefault, 0), testfile.El(testfile.IDTagString, []byte("Sterk")))))),
			edits: []parityEdit{
				tags("genre", setTag(tag.Genre, "Rock")),
				tags("artist added", func(_ *Document, e *Editor) { e.Add(tag.Artist, "Second") }),
				tags("titles", setTag(tag.Title, "Song", "Third")),
			}},
		parityInput{name: "plain.mka", data: testfile.Matroska("matroska", "force-cover"),
			edits: []parityEdit{{kind: kindPictures, name: "unrecognized cover", opts: []WriteOption{WithUnrecognizedPictures()},
				edit: func(_ *Document, e *Editor) {
					e.AddPicture(Picture{Type: PicFrontCover, MIME: "application/octet-stream", Data: []byte("plain file, not an image")})
				}}}},
	)
}

// Matroska element IDs the synthesized inputs use beyond testfile's.
const (
	mkaAttachments  = 0x1941A469
	mkaAttachedFile = 0x61A7
	mkaFileName     = 0x466E
	mkaFileMime     = 0x4660
	mkaFileData     = 0x465C
	mkaFileUID      = 0x46AE
	mkaTagAttachUID = 0x63C6
	mkaTagChapUID   = 0x63C4
	mkaTagEditUID   = 0x63C9
	mkaTrackUID     = 0x73C5
	mkaTagLang      = 0x447A
	mkaTagLangBCP47 = 0x447B
	mkaTagDefault   = 0x4484
)

// mkaNoTitle is a Matroska file whose Info holds no Title, with elements behind the cluster.
func mkaNoTitle(elements ...[]byte) []byte {
	return slices.Concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment,
		testfile.El(testfile.IDInfo, testfile.El(testfile.IDDuration, make([]byte, 8))), testfile.AudioCluster(0), slices.Concat(elements...)))
}

// mkaLevelTag is a Tag at a target level.
func mkaLevelTag(level uint64, simples ...[]byte) []byte {
	return testfile.El(testfile.IDTag, testfile.El(testfile.IDTargets, testfile.Uint(testfile.IDTgtTypeVal, level)), slices.Concat(simples...))
}

// mkaAlbumTag is the album-level Tag of a file tagged by level.
func mkaAlbumTag() []byte {
	return mkaLevelTag(50, testfile.SimpleTag("TITLE", "The Album"), testfile.SimpleTag("ARTIST", "Album Artist"), testfile.SimpleTag("GENRE", "Jazz"))
}

// mkaTargeted is a Tag whose Targets holds one UID element.
func mkaTargeted(uidElement, uid uint64, simples ...[]byte) []byte {
	return testfile.El(testfile.IDTag, testfile.El(testfile.IDTargets, testfile.Uint(uidElement, uid)), slices.Concat(simples...))
}

// mkaTrack is a TrackEntry of the given type (1 video, 2 audio), numbered and identified by uid.
func mkaTrack(uid, trackType uint64, codec string) []byte {
	return testfile.El(testfile.IDTrackEntry, testfile.Uint(0xD7, uid), testfile.Uint(mkaTrackUID, uid),
		testfile.Uint(testfile.IDTrackType, trackType), testfile.El(testfile.IDCodecID, []byte(codec)))
}

func mkaAttached(name, mime string, data []byte, uid uint64) []byte {
	return testfile.El(mkaAttachedFile, testfile.El(mkaFileName, []byte(name)), testfile.El(mkaFileMime, []byte(mime)),
		testfile.El(mkaFileData, data), testfile.Uint(mkaFileUID, uid))
}

// apeTagOffset is where the APE tag that ends data starts.
func apeTagOffset(t *testing.T, data []byte) int {
	t.Helper()
	tg, ok, err := ape.ParseAt(core.BytesSource(data), int64(len(data)), 1<<20, 0)
	if err != nil || !ok {
		t.Fatalf("no APE tag ends the data (err %v)", err)
	}
	return int(tg.Offset)
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
