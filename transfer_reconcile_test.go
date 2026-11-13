package waxlabel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// lossyResultCodec plans with the codec it wraps, then leaves less in the result than the edit
// asked for: no ARTIST, and one TITLE value of two. Its capabilities promise neither loss.
type lossyResultCodec struct{ core.Codec }

func (c lossyResultCodec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	wp, err := c.Codec.Plan(ctx, base, edited, opts)
	if err != nil {
		return nil, err
	}
	wp.Result = wp.Result.Clone()
	wp.Result.Tags.Delete(tag.Artist)
	if titles, _ := wp.Result.Tags.Get(tag.Title); len(titles) > 1 {
		wp.Result.Tags.Set(tag.Title, titles[0])
	}
	return wp, nil
}

// The report a transfer prepares is held to the result the plan leaves, whatever the
// destination's capabilities promised.
func TestTransferReportIsHeldToThePlannedResult(t *testing.T) {
	src := mustParseParity(t, testdataBytes(t, "notags.flac"))
	plan, err := src.Edit().Set(tag.Title, "One", "Two").Set(tag.Artist, "A").Set(tag.Album, "Al").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var srcBytes bytes.Buffer
	if _, _, err := plan.Execute(context.Background(), WriteTo(&srcBytes, BytesSource(testdataBytes(t, "notags.flac")))); err != nil {
		t.Fatal(err)
	}
	src = mustParseParity(t, srcBytes.Bytes())
	dst := mustParseParity(t, testdataBytes(t, "notags.flac"))

	flac, _ := core.ForFormat(FormatFLAC)
	t.Cleanup(func() { codecForFormat = core.ForFormat })
	codecForFormat = func(Format) (core.Codec, bool) { return lossyResultCodec{flac}, true }
	_, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatal(err)
	}
	want := map[tag.Key]struct {
		d      Disposition
		reason string
	}{
		tag.Title:  {Lossy, "only the first of 2 values is stored"},
		tag.Artist: {Dropped, "the destination does not store this value"},
		tag.Album:  {Carried, ""},
	}
	for _, it := range report.Items {
		if w, ok := want[it.Key]; ok && (it.Disposition != w.d || it.Reason != w.reason) {
			t.Errorf("%s: %s %q, want %s %q", it.Key, it.Disposition, it.Reason, w.d, w.reason)
		}
	}
}

// A destination's capabilities grade under the options the write runs with: every format that
// keeps its tags in ID3v2.3 states the join a slash policy makes, before any plan is made.
func TestCapabilitiesGradeUnderTheWriteOptions(t *testing.T) {
	v23 := testfile.ID3v2(3, testfile.TextFrame(3, "TALB", "Held"))
	src := &core.Media{Format: FormatFLAC, Tags: tag.NewTagSet()}
	src.Tags.Set(tag.Artist, "AC/DC", "Queen")
	for name, data := range map[string][]byte{
		"mp3":  append(slices.Clone(v23), testdataBytes(t, "notags.mp3")...),
		"aac":  append(slices.Clone(v23), testdataBytes(t, "notags.aac")...),
		"wav":  testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVChunk("id3 ", v23), testfile.WAVData(4000)),
		"aiff": testfile.AIFF("AIFF", testfile.COMM(2, 1000, 16, 44100), testfile.AIFFChunk("ID3 ", v23), testfile.AIFFChunk("SSND", make([]byte, 8+4000))),
	} {
		dst := mustParseParity(t, data)
		if album, _ := dst.Get(tag.Album); !slices.Equal(album, []string{"Held"}) {
			t.Fatalf("%s setup: ALBUM = %q, want the ID3v2.3 tag read", name, album)
		}
		joined := core.ProjectTransferOnto(src, nil, dst.Capabilities(WithID3MultiValue(ID3MultiSlash))).Items
		if len(joined) != 1 || joined[0].Disposition != Lossy || joined[0].Reason != `the values are stored joined as "AC/DC / Queen"` {
			t.Errorf("%s under the slash policy: %+v, want ARTIST lossy for the join", name, joined)
		}
		if apart := core.ProjectTransferOnto(src, nil, dst.Capabilities()).Items; len(apart) != 1 || apart[0].Disposition != Carried {
			t.Errorf("%s under the default policy: %+v, want ARTIST carried", name, apart)
		}
	}
}

// emptyDeletesCodec plans with the codec it wraps and has no form for an empty ARTIST: asked to
// write one, its result holds no ARTIST at all, the destination's included.
type emptyDeletesCodec struct{ core.Codec }

func (c emptyDeletesCodec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	wp, err := c.Codec.Plan(ctx, base, edited, opts)
	if err != nil {
		return nil, err
	}
	if vals, _ := edited.Tags.Get(tag.Artist); len(vals) == 1 && vals[0] == "" && wp.Result != nil {
		wp.Result = wp.Result.Clone()
		wp.Result.Tags.Delete(tag.Artist)
	}
	return wp, nil
}

// A field the write would store nothing of is not written, so the destination keeps the value
// it held, and the rest of the copy goes on.
func TestTransferLeavesUnwrittenWhatTheWriteWouldOnlyDelete(t *testing.T) {
	build := func(edit func(*Editor)) *Document {
		doc := mustParseParity(t, testdataBytes(t, "notags.flac"))
		ed := doc.Edit()
		edit(ed)
		plan, err := ed.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if _, _, err := plan.Execute(context.Background(), WriteTo(&out, BytesSource(testdataBytes(t, "notags.flac")))); err != nil {
			t.Fatal(err)
		}
		return mustParseParity(t, out.Bytes())
	}
	src := build(func(e *Editor) { e.Set(tag.Artist, "").Set(tag.Title, "New") })
	dst := build(func(e *Editor) { e.Set(tag.Artist, "Held").Set(tag.Title, "Old") })

	flac, _ := core.ForFormat(FormatFLAC)
	t.Cleanup(func() { codecForFormat = core.ForFormat })
	codecForFormat = func(Format) (core.Codec, bool) { return emptyDeletesCodec{flac}, true }
	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range report.Items {
		switch it.Key {
		case tag.Artist:
			if it.Disposition != Dropped || it.Reason != "the destination does not store this value and keeps the one it had" {
				t.Errorf("ARTIST: %s %q, want dropped with the destination's kept", it.Disposition, it.Reason)
			}
		case tag.Title:
			if it.Disposition != Carried {
				t.Errorf("TITLE: %s %q, want carried", it.Disposition, it.Reason)
			}
		}
	}
	result := plan.plan.Result.Tags
	if got, _ := result.Get(tag.Artist); !slices.Equal(got, []string{"Held"}) {
		t.Errorf("planned ARTIST = %q, want the destination's left as it was", got)
	}
	if got, _ := result.Get(tag.Title); !slices.Equal(got, []string{"New"}) {
		t.Errorf("planned TITLE = %q, want the source's", got)
	}
}

// A WAV or AIFF destination is graded by the containers it holds: a value only the native chunk
// has a form for is carried where the write puts it there, and not where the file's tags go to an
// ID3 chunk alone, or a legacy strip leaves no native chunk.
func TestCapabilitiesGradeByTheDestinationsContainers(t *testing.T) {
	v24 := testfile.ID3v2(4, testfile.TextFrame(4, "TALB", "Held"))
	emptyDate := &core.Media{Format: FormatFLAC, Tags: tag.NewTagSet()}
	emptyDate.Tags.Set(tag.RecordingDate, "")
	trailingEmpty := &core.Media{Format: FormatFLAC, Tags: tag.NewTagSet()}
	trailingEmpty.Tags.Set(tag.Comment, "x", "")
	core.SyncTextItems(trailingEmpty)
	wav := func(chunks ...[]byte) []byte {
		return testfile.WAV(append(append([][]byte{testfile.WAVFmtPCM()}, chunks...), testfile.WAVData(4000))...)
	}
	aiff := func(chunks ...[]byte) []byte {
		all := append([][]byte{testfile.COMM(2, 1000, 16, 44100)}, chunks...)
		return testfile.AIFF("AIFF", append(all, testfile.AIFFChunk("SSND", make([]byte, 8+4000)))...)
	}
	info := testfile.WAVInfo([2]string{"INAM", "Held"})
	for _, c := range []struct {
		name string
		dst  []byte
		src  *core.Media
		opts []WriteOption
		want Disposition
	}{
		{"WAV with no tags", wav(), emptyDate, nil, Carried},
		{"WAV with an id3 chunk alone", wav(testfile.WAVChunk("id3 ", v24)), emptyDate, nil, Dropped},
		{"WAV with no tags under a legacy strip", wav(), emptyDate, []WriteOption{WithLegacyPolicy(LegacyStrip)}, Dropped},
		{"WAV with LIST/INFO alone", wav(info), emptyDate, nil, Carried},
		{"WAV with both", wav(info, testfile.WAVChunk("id3 ", v24)), emptyDate, nil, Carried},
		{"WAV with both under a legacy strip", wav(info, testfile.WAVChunk("id3 ", v24)), emptyDate, []WriteOption{WithLegacyPolicy(LegacyStrip)}, Dropped},
		{"AIFF with no tags", aiff(), trailingEmpty, nil, Carried},
		{"AIFF with an ID3 chunk alone", aiff(testfile.AIFFChunk("ID3 ", v24)), trailingEmpty, nil, Lossy},
		{"AIFF with no tags under a legacy strip", aiff(), trailingEmpty, []WriteOption{WithLegacyPolicy(LegacyStrip)}, Lossy},
	} {
		dst := mustParseParity(t, c.dst)
		items := core.ProjectTransferOnto(c.src, nil, dst.Capabilities(c.opts...)).Items
		if len(items) != 1 || items[0].Disposition != c.want {
			t.Errorf("%s: %+v, want %s", c.name, items, c.want)
			continue
		}
		// The grade is what the write does.
		holder := &Document{media: c.src}
		_, report, err := holder.PrepareTransfer(dst, c.opts...)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := report.Items[0].Disposition; got != c.want {
			t.Errorf("%s: the prepared copy grades it %s (%s), the capabilities %s", c.name, got, report.Items[0].Reason, c.want)
		}
	}
}

// chapterlessCodec plans with the codec it wraps and stores no chapter list it is given: asked
// to write one, it warns of the discard and its result holds no chapters at all, the
// destination's included.
type chapterlessCodec struct{ core.Codec }

func (c chapterlessCodec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	wp, err := c.Codec.Plan(ctx, base, edited, opts)
	if err != nil {
		return nil, err
	}
	if !core.EqualChapters(edited.Chapters, base.Chapters) && wp.Result != nil {
		wp.Result = wp.Result.Clone()
		wp.Result.Chapters = nil
		wp.Report.Warnings = append(wp.Report.Warnings, core.Warning{Code: core.WarnChaptersUnsupported, Message: "chapters are not stored"})
	}
	return wp, nil
}

// A chapter list the write would store none of is not written, so the destination keeps the
// chapters it held, and the rest of the copy goes on.
func TestTransferLeavesUnwrittenAListTheWriteStoresNoneOf(t *testing.T) {
	build := func(title string, chapters ...Chapter) *Document {
		doc := mustParseParity(t, testdataBytes(t, "notags.flac"))
		plan, err := doc.Edit().Set(tag.Title, title).SetChapters(chapters...).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if _, _, err := plan.Execute(context.Background(), WriteTo(&out, BytesSource(testdataBytes(t, "notags.flac")))); err != nil {
			t.Fatal(err)
		}
		return mustParseParity(t, out.Bytes())
	}
	src := build("New", Chapter{Title: "a"}, Chapter{Start: time.Second, Title: "b"})
	dst := build("Old", Chapter{Title: "own"})

	flac, _ := core.ForFormat(FormatFLAC)
	t.Cleanup(func() { codecForFormat = core.ForFormat })
	codecForFormat = func(Format) (core.Codec, bool) { return chapterlessCodec{flac}, true }
	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatal(err)
	}
	want := TransferItem{Kind: TransferChapter, Count: 2, Disposition: Dropped, Reason: "the destination does not store this chapter"}
	if at := slices.IndexFunc(report.Items, func(it TransferItem) bool { return it.Kind == TransferChapter }); at < 0 || report.Items[at] != want {
		t.Errorf("chapters: %+v, want %+v", report.Items, want)
	}
	result := plan.plan.Result
	if len(result.Chapters) != 1 || result.Chapters[0].Title != "own" {
		t.Errorf("chapters after the copy: %+v, want the destination's own", result.Chapters)
	}
	if title, _ := result.Tags.Get(tag.Title); !slices.Equal(title, []string{"New"}) {
		t.Errorf("TITLE after the copy = %q, want the source's", title)
	}
	if ws := plan.Report().Warnings; len(ws) != 0 {
		t.Errorf("the plan still warns of a list it does not write: %v", ws)
	}
}

// failingCodec refuses every plan that would write.
type failingCodec struct{ core.Codec }

func (c failingCodec) Plan(ctx context.Context, base, edited *core.Media, opts core.WriteOptions) (*core.WritePlan, error) {
	if wp, err := c.Codec.Plan(ctx, base, edited, opts); err != nil || wp.NoOp {
		return wp, err
	}
	return nil, fmt.Errorf("%w: the edit does not fit", waxerr.ErrContentTooLarge)
}

// A file with no audio takes no write, and that is the reason a copy onto one is refused for,
// whatever else stands in its way: a key no file stores, a refusal of the codec's. A copy that
// has nothing to write is no write.
func TestCopyOntoAFileWithNoAudioIsRefusedForThat(t *testing.T) {
	noAudio := append(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "x")), []byte("this is not audio, just plain text\n")...)
	carry := func(edit func(*Editor)) (*Plan, error) {
		ed := mustParseParity(t, noAudio).Edit()
		ed.carried = true
		edit(ed)
		return ed.Prepare()
	}
	refused := func(name string, err error) {
		t.Helper()
		if err == nil || !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "no audio") {
			t.Errorf("%s: err = %v, want the no-audio refusal", name, err)
		}
	}
	_, err := carry(func(e *Editor) { e.Set(tag.Title, "y") })
	refused("a field to write", err)
	_, err = carry(func(e *Editor) { e.Set(tag.Key("bad=key"), "y") })
	refused("a key no file stores", err)

	mp3, _ := core.ForFormat(FormatMP3)
	t.Cleanup(func() { codecForFormat = core.ForFormat })
	codecForFormat = func(Format) (core.Codec, bool) { return failingCodec{mp3}, true }
	_, err = carry(func(e *Editor) { e.Set(tag.Title, "y") })
	refused("a write the codec refuses", err)
	if plan, err := carry(func(e *Editor) { e.Set(tag.Title, "x") }); err != nil || !plan.IsNoOp() {
		t.Errorf("a copy with nothing to write: err %v, want a plan that writes nothing", err)
	}
	// A plain edit of such a file is refused as it always was, after the checks that came first.
	if _, err := mustParseParity(t, noAudio).Edit().SetOutputGain(1 << 20).Prepare(); err == nil || !strings.Contains(err.Error(), "output gain") {
		t.Errorf("a plain edit with a gain no field holds: %v, want that refusal", err)
	}
	// The same refusals stand as they are on a file that has audio.
	ed := mustParseParity(t, testdataBytes(t, "notags.mp3")).Edit()
	ed.carried = true
	if _, err := ed.Set(tag.Title, "y").Prepare(); !errors.Is(err, waxerr.ErrContentTooLarge) {
		t.Errorf("the codec's refusal on a file with audio: %v, want it as given", err)
	}
}
