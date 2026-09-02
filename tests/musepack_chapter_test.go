package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// SV8 chapter synthesis. mpcchap, the reference chapter editor, is the only tool that
// writes CT packets; its byte layout (and libmpcdec's reading of it) is what these
// builders reproduce: a varlen start sample, a 16-bit gain and peak, then an APEv2 tag
// without its "APETAGEX" preamble - the 24-byte header record and the items, no footer.

// mpcSV8Stream builds an SV8 file from a stream header with the given geometry and the
// packets that follow it. The header carries a valid CRC so the reference decoder
// accepts the file too.
func mpcSV8Stream(samples uint64, rateIndex, channels int, packets ...[]byte) []byte {
	sh := []byte{0, 0, 0, 0, 8}
	sh = append(sh, mpcVarlen(samples)...)
	sh = append(sh, mpcVarlen(0)...)
	sh = append(sh, byte(rateIndex<<5)|20)
	sh = append(sh, byte((channels-1)<<4), 0x00)
	binary.BigEndian.PutUint32(sh[0:4], crc32.ChecksumIEEE(sh[4:]))
	out := append([]byte("MPCK"), mpcPacket("SH", sh)...)
	for _, p := range packets {
		out = append(out, p...)
	}
	return out
}

// mpcChapterTag renders the tag a chapter packet carries: the APEv2 header record
// minus its preamble (version, size, item count, flags, reserved) and the items. The
// size counts the items alone, as the record excludes itself and there is no footer.
// No items renders nothing, which is an untitled chapter. testdata/chapters.mpc is the
// editor's own output of the same shape, which TestMusepackChaptersFromEditorWrittenFile
// pins; this builder exists for the placements and malformed forms a real file lacks.
func mpcChapterTag(items ...[2]string) []byte {
	if len(items) == 0 {
		return nil
	}
	var body []byte
	for _, it := range items {
		body = binary.LittleEndian.AppendUint32(body, uint32(len(it[1])))
		body = binary.LittleEndian.AppendUint32(body, 0)
		body = append(body, it[0]...)
		body = append(body, 0)
		body = append(body, it[1]...)
	}
	head := make([]byte, 24)
	binary.LittleEndian.PutUint32(head[0:4], 2000)
	binary.LittleEndian.PutUint32(head[4:8], uint32(len(body)))
	binary.LittleEndian.PutUint32(head[8:12], uint32(len(items)))
	binary.LittleEndian.PutUint32(head[12:16], 0xA0000000)
	return append(head, body...)
}

// mpcChapter builds a CT packet starting at sample with the given tag bytes.
func mpcChapter(sample uint64, tag []byte) []byte {
	p := append(mpcVarlen(sample), 0, 0, 0, 0)
	return mpcPacket("CT", append(p, tag...))
}

// mpcTitled is the common chapter: a single Title item.
func mpcTitled(sample uint64, title string) []byte {
	return mpcChapter(sample, mpcChapterTag([2]string{"Title", title}))
}

// mpcSeekOffset builds an SO packet whose pointer lands gap bytes past the packet's
// own end. The pointer is relative to the packet's start and the packet's length
// depends on the pointer's width, so the value is solved for.
func mpcSeekOffset(gap int) []byte {
	ptr := gap + 4
	for {
		so := mpcPacket("SO", mpcVarlen(uint64(ptr)))
		if len(so)+gap == ptr {
			return so
		}
		ptr = len(so) + gap
	}
}

// mpcAudio is a stub audio packet of n bytes.
func mpcAudio(n int) []byte { return mpcPacket("AP", make([]byte, n)) }

// mpcSeekTable is an empty seek table packet.
func mpcSeekTable() []byte { return mpcPacket("ST", []byte{0, 0}) }

// mpcEnd is the stream end marker.
func mpcEnd() []byte { return mpcPacket("SE", nil) }

// mpcChaptered is the fixture most tests share: three titled chapters before the end
// marker, stored out of time order to show the projection sorts them.
func mpcChaptered() []byte {
	return mpcSV8Stream(44100*120, 0, 2,
		mpcAudio(64),
		mpcTitled(0, "Intro"),
		mpcTitled(44100*90, "Finale"),
		mpcTitled(44100*30, "Middle"),
		mpcEnd())
}

// chaptersMPC is an mpcenc stream mpcchap r475 wrote chapters into: Title=Intro at
// sample 0, a three-item chapter at 8000 whose title item comes last, TITLE=Coda at
// 16000, and an untitled chapter at 19000 carrying no tag bytes. It is the reference
// editor's byte layout, so the reader is pinned to real output rather than to the
// builder's understanding of it.
const chaptersMPC = "../testdata/chapters.mpc"

func TestMusepackChaptersFromEditorWrittenFile(t *testing.T) {
	doc := mustParseFile(t, chaptersMPC)
	chs := doc.Chapters()
	want := []struct {
		sample uint64
		title  string
	}{{0, "Intro"}, {8000, "Middle"}, {16000, "Coda"}, {19000, ""}}
	if len(chs) != len(want) {
		t.Fatalf("chapters = %+v, want %d", chs, len(want))
	}
	for i, w := range want {
		start := time.Duration(float64(w.sample) / 44100 * float64(time.Second))
		if chs[i].Start != start || chs[i].Title != w.title || chs[i].End != 0 {
			t.Errorf("chapter %d = %+v, want sample %d (%v) titled %q", i, chs[i], w.sample, start, w.title)
		}
	}
	if len(doc.Warnings()) != 0 {
		t.Errorf("the editor's own output should read cleanly; warnings = %v", doc.Warnings())
	}
}

func TestMusepackSV8ChaptersRead(t *testing.T) {
	doc := mustParseBytes(t, mpcChaptered())
	chs := doc.Chapters()
	want := []wl.Chapter{
		{Start: 0, Title: "Intro"},
		{Start: 30 * time.Second, Title: "Middle"},
		{Start: 90 * time.Second, Title: "Finale"},
	}
	if len(chs) != len(want) {
		t.Fatalf("chapters = %+v, want %d", chs, len(want))
	}
	for i := range want {
		if chs[i].Start != want[i].Start || chs[i].Title != want[i].Title || chs[i].End != 0 {
			t.Errorf("chapter %d = %+v, want start %v title %q and no end", i, chs[i], want[i].Start, want[i].Title)
		}
	}
	caps := doc.Capabilities().Chapters
	if caps.Read != wl.AccessFull || caps.Write != wl.AccessNone {
		t.Errorf("chapters capability = read %s, write %s; want read full, write none", caps.Read, caps.Write)
	}
	if c := wl.CapabilitiesFor(wl.FormatMusepack).Chapters; c.Read != wl.AccessFull || c.Write != wl.AccessNone {
		t.Errorf("format-level chapters capability = read %s, write %s; want read full, write none", c.Read, c.Write)
	}
}

// TestMusepackSV8ChapterRunPlacement pins where the reference decoder looks for
// chapters: right after the seek table an SO packet points at, or else the run of CT
// packets that ends at the end marker. A run anywhere else is not seen.
func TestMusepackSV8ChapterRunPlacement(t *testing.T) {
	audio := mpcAudio(64)
	st := mpcSeekTable()
	for _, c := range []struct {
		name    string
		packets [][]byte
		want    []string
	}{
		{"run before the end marker",
			[][]byte{audio, mpcTitled(0, "a"), mpcTitled(1152, "b"), mpcEnd()}, []string{"a", "b"}},
		{"run after the seek table, without a seek offset",
			[][]byte{audio, st, mpcTitled(0, "a"), mpcEnd()}, []string{"a"}},
		{"run after the seek table the seek offset points at, audio following",
			[][]byte{mpcSeekOffset(len(audio)), audio, st, mpcTitled(0, "after st"), audio, mpcEnd()}, []string{"after st"}},
		{"run interrupted by audio is not the run before the end marker",
			[][]byte{audio, mpcTitled(0, "lost"), audio, mpcEnd()}, nil},
		{"seek offset pointing at a non-table packet falls back to the run before the end marker",
			[][]byte{mpcSeekOffset(0), audio, mpcTitled(0, "a"), mpcEnd()}, []string{"a"}},
		{"chapters at the front, before the audio, are not seen",
			[][]byte{mpcTitled(0, "front"), audio, mpcEnd()}, nil},
		{"no end marker",
			[][]byte{audio, mpcTitled(0, "a")}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, ch := range mustParseBytes(t, mpcSV8Stream(44100, 0, 2, c.packets...)).Chapters() {
				got = append(got, ch.Title)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("chapters = %v, want %v", got, c.want)
			}
		})
	}
}

// TestMusepackSV8ChapterTagForms covers what a chapter packet's tag can hold.
func TestMusepackSV8ChapterTagForms(t *testing.T) {
	for _, c := range []struct {
		name  string
		tag   []byte
		title string
	}{
		{"no tag: untitled", nil, ""},
		{"title item name in any case", mpcChapterTag([2]string{"TITLE", "Loud"}), "Loud"},
		{"other items are not the title", mpcChapterTag([2]string{"Track", "1/3"}, [2]string{"Artist", "A"}, [2]string{"Title", "T"}), "T"},
		{"a multi-valued title reads its first value", mpcChapterTag([2]string{"Title", "One\x00Two"}), "One"},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := mpcSV8Stream(44100, 0, 2, mpcAudio(8), mpcChapter(0, c.tag), mpcEnd())
			chs := mustParseBytes(t, data).Chapters()
			if len(chs) != 1 || chs[0].Title != c.title {
				t.Errorf("chapters = %+v, want one titled %q", chs, c.title)
			}
		})
	}
}

// TestMusepackSV8MalformedChapterPackets: a tag too short for its header record still
// yields the chapter (untitled), a packet whose start sample runs off its end is
// skipped while the packets after it are still read, and a start no duration can hold
// is skipped rather than placed at the start; each is reported once however many
// packets share the fault, and the packets count against the element cap whether or
// not they yield a chapter.
func TestMusepackSV8MalformedChapterPackets(t *testing.T) {
	t.Run("start sample past any duration", func(t *testing.T) {
		data := mpcSV8Stream(44100, 0, 2, mpcAudio(8), mpcTitled(1<<62, "far"), mpcTitled(1152, "near"), mpcEnd())
		doc := mustParseBytes(t, data)
		if chs := doc.Chapters(); len(chs) != 1 || chs[0].Title != "near" {
			t.Errorf("chapters = %+v, want only the placeable chapter", chs)
		}
		if !hasWarning(doc, wl.WarnMalformedTagEntry) {
			t.Error("expected a malformed-tag-entry warning for the unplaceable chapter")
		}
	})
	t.Run("faults are reported once and count against the cap", func(t *testing.T) {
		// The run sits behind a seek offset, so the walk's own cap stays out of it.
		audio, bad := mpcAudio(8), mpcPacket("CT", []byte{0xFF, 0xFF})
		data := mpcSV8Stream(44100, 0, 2, mpcSeekOffset(len(audio)), audio, mpcSeekTable(), bad, bad, bad, bad, mpcTitled(0, "ok"), mpcEnd())
		doc, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxElements: 20}))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(doc.Warnings()); n != 1 {
			t.Errorf("warnings = %v, want the one coalesced malformed-packet warning", doc.Warnings())
		}
		if chs := doc.Chapters(); len(chs) != 1 {
			t.Errorf("chapters = %+v, want the one readable chapter", chs)
		}
		// Four packets of budget: the malformed packets spend it all before the
		// readable one is reached, and the cap warning is the only other report.
		doc, err = wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxElements: 4}))
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Chapters()) != 0 || len(doc.Warnings()) != 2 || !hasWarning(doc, wl.WarnElementCap) {
			t.Errorf("under a cap the malformed packets exhaust: chapters = %+v, warnings = %v", doc.Chapters(), doc.Warnings())
		}
	})
	t.Run("short tag", func(t *testing.T) {
		data := mpcSV8Stream(44100, 0, 2, mpcAudio(8), mpcChapter(1152, []byte("junk")), mpcEnd())
		doc := mustParseBytes(t, data)
		if chs := doc.Chapters(); len(chs) != 1 || chs[0].Title != "" {
			t.Errorf("chapters = %+v, want one untitled chapter", chs)
		}
		if !hasWarning(doc, wl.WarnMalformedTagEntry) {
			t.Error("expected a malformed-tag-entry warning for the unreadable chapter tag")
		}
	})
	t.Run("empty payload at the end of the stream", func(t *testing.T) {
		// A zero-length read at a source's end is one io.ReaderAt may answer with EOF
		// (bytes.Reader does, a file does not); the packet is truncated either way.
		audio := mpcAudio(64)
		data := mpcSV8Stream(44100, 0, 2, mpcSeekOffset(len(audio)), audio, mpcSeekTable(), mpcPacket("CT", nil))
		file := mustParseFile(t, writeTempFile(t, "trailing-ct.mpc", data))
		for name, src := range map[string]wl.ReaderAtSized{"bytes source": wl.BytesSource(data), "bytes.Reader": bytes.NewReader(data)} {
			doc, err := wl.Parse(context.Background(), src)
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Chapters()) != 0 || !hasWarning(doc, wl.WarnMalformedTagEntry) {
				t.Errorf("%s: chapters = %+v, warnings = %v; want none and the truncated report", name, doc.Chapters(), doc.Warnings())
			}
			for i, w := range doc.Warnings() {
				if !strings.Contains(w.Message, "truncated") || i >= len(file.Warnings()) || w.Message != file.Warnings()[i].Message {
					t.Errorf("%s: warning %q; want the file's %v", name, w.Message, file.Warnings())
				}
			}
		}
	})
	t.Run("payload the source cannot read", func(t *testing.T) {
		audio := mpcAudio(64)
		head := mpcSV8Stream(44100, 0, 2, mpcSeekOffset(len(audio)), audio, mpcSeekTable())
		data := append(append(head, mpcTitled(0, "x")...), mpcEnd()...)
		doc, err := wl.Parse(context.Background(), failAfterSource{data: data, failAt: int64(len(head) + 3)})
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range doc.Warnings() {
			if w.Code == wl.WarnMalformedTagEntry && strings.Contains(w.Message, "allocation limit") {
				t.Errorf("warning %q names the allocation limit for a read failure", w.Message)
			}
		}
		if len(doc.Chapters()) != 0 || !hasWarning(doc, wl.WarnMalformedTagEntry) {
			t.Errorf("chapters = %+v, warnings = %v; want none and a malformed-entry report", doc.Chapters(), doc.Warnings())
		}
	})
	t.Run("truncated start sample", func(t *testing.T) {
		data := mpcSV8Stream(44100, 0, 2, mpcAudio(8),
			mpcTitled(0, "kept"), mpcPacket("CT", []byte{0xFF, 0xFF}), mpcTitled(1152, "after"), mpcEnd())
		doc := mustParseBytes(t, data)
		if chs := doc.Chapters(); len(chs) != 2 || chs[0].Title != "kept" || chs[1].Title != "after" {
			t.Errorf("chapters = %+v, want the two readable chapters around the truncated packet", chs)
		}
		if !hasWarning(doc, wl.WarnMalformedTagEntry) {
			t.Error("expected a malformed-tag-entry warning for the truncated chapter packet")
		}
	})
}

// TestMusepackSV8ChapterElementCap: the element cap bounds the packets walked to find
// the run, the packets of the run itself, and the items a chapter tag is read for, each
// reported when it trips. The run cap is reached through a seek offset, which ends the
// walk at its second packet; a walk that must reach the end marker hits the cap first.
func TestMusepackSV8ChapterElementCap(t *testing.T) {
	audio := mpcAudio(64)
	viaSeekTable := mpcSV8Stream(44100, 0, 2, mpcSeekOffset(len(audio)), audio, mpcSeekTable(),
		mpcTitled(0, "a"), mpcTitled(1152, "b"), mpcTitled(2304, "c"), mpcEnd())
	doc, err := wl.Parse(context.Background(), wl.BytesSource(viaSeekTable), wl.WithLimits(wl.Limits{MaxElements: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(doc.Chapters()); n != 2 {
		t.Errorf("got %d chapters under a cap of 2, want 2", n)
	}
	if !hasWarning(doc, wl.WarnElementCap) {
		t.Error("expected an element-cap warning")
	}
	doc, err = wl.Parse(context.Background(), wl.BytesSource(mpcChaptered()), wl.WithLimits(wl.Limits{MaxElements: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(doc.Chapters()); n != 0 || !hasWarning(doc, wl.WarnElementCap) {
		t.Errorf("a walk cut short by the cap reads no chapters and says so: %d chapters, warnings %v", n, doc.Warnings())
	}
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnElementCap && !strings.HasSuffix(w.Message, "no chapters are read") {
			t.Errorf("walk-cap warning %q should say no chapters are read", w.Message)
		}
	}
	// The title item sits past the per-tag item cap: the chapter reads untitled and the
	// cap is reported rather than the title silently going missing.
	items := make([][2]string, 0, 9)
	for i := range 8 {
		items = append(items, [2]string{"Comment", strings.Repeat("x", i+1)})
	}
	items = append(items, [2]string{"Title", "Last"})
	data := mpcSV8Stream(44100, 0, 2, mpcAudio(8), mpcChapter(0, mpcChapterTag(items...)), mpcEnd())
	doc, err = wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxElements: 6}))
	if err != nil {
		t.Fatal(err)
	}
	if chs := doc.Chapters(); len(chs) != 1 || chs[0].Title != "" || !hasWarning(doc, wl.WarnElementCap) {
		t.Errorf("capped tag: chapters = %+v, warnings = %v", chs, doc.Warnings())
	}
}

// countingSource counts the reads a parse makes of its source.
type countingSource struct {
	wl.ReaderAtSized
	reads int
}

func (s *countingSource) ReadAt(p []byte, off int64) (int, error) {
	s.reads++
	return s.ReaderAtSized.ReadAt(p, off)
}

// TestMusepackSV8ChapterRunCapBoundsWork: once the run cap trips nothing more of the
// run is read, so a run of any length past the cap costs the same reads as one just
// over it. The run is reached through a seek offset, which the walk's own cap does not
// count.
func TestMusepackSV8ChapterRunCapBoundsWork(t *testing.T) {
	audio := mpcAudio(64)
	reads := func(n int) int {
		packets := [][]byte{mpcSeekOffset(len(audio)), audio, mpcSeekTable()}
		for i := range n {
			packets = append(packets, mpcTitled(uint64(i)*1152, "c"))
		}
		src := &countingSource{ReaderAtSized: wl.BytesSource(mpcSV8Stream(44100, 0, 2, append(packets, mpcEnd())...))}
		doc, err := wl.Parse(context.Background(), src, wl.WithLimits(wl.Limits{MaxElements: 2}))
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Chapters()) != 2 || !hasWarning(doc, wl.WarnElementCap) {
			t.Errorf("run of %d under a cap of 2: chapters = %+v, warnings = %v", n, doc.Chapters(), doc.Warnings())
		}
		return src.reads
	}
	if short, long := reads(3), reads(300); long != short {
		t.Errorf("a run of 300 packets cost %d reads, a run of 3 cost %d; past the cap the length should not matter", long, short)
	}
}

// TestMusepackSV8StreamVersionAndKeys: an MPCK stream must declare stream version 8
// in its header packet, as the reference decoder and ffmpeg both require, and a
// packet key outside A-Z is not a packet, so a stream opening with one has no header.
func TestMusepackSV8StreamVersionAndKeys(t *testing.T) {
	data := mpcSV8(44100, 0, 0, 2)
	data[4+2+1+4] = 7 // the version byte after the SH key, size, and CRC
	if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("SH version 7 in an MPCK stream: err = %v, want ErrUnsupportedFormat", err)
	}
	data = append([]byte("MPCK"), mpcPacket("s1", nil)...)
	data = append(data, mpcSV8(44100, 0, 0, 2)[4:]...)
	if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("non-letter packet key ahead of SH: err = %v, want ErrInvalidData", err)
	}
}

func TestMusepackReservedRateReportsNoChapters(t *testing.T) {
	doc := mustParseBytes(t, mpcSV8Stream(44100, 5, 2, mpcAudio(8), mpcTitled(0, "a"), mpcEnd()))
	if caps := doc.Capabilities().Chapters; caps.Read != wl.AccessNone || len(doc.Chapters()) != 0 {
		t.Errorf("reserved rate index: chapters cannot be placed, so read = %s and %d chapters; want none", caps.Read, len(doc.Chapters()))
	}
}

// TestMusepackChaptersSurviveTagEdit: the packets sit inside the stream a rewrite
// copies verbatim, so a tag edit keeps them and the plan's result document agrees
// with a fresh parse.
func TestMusepackChaptersSurviveTagEdit(t *testing.T) {
	src := mpcChaptered()
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	result, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(src)))
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, w.b)
	if len(re.Chapters()) != 3 {
		t.Fatalf("chapters after a tag edit = %+v, want 3", re.Chapters())
	}
	if !bytes.Contains(w.b, []byte("Finale")) {
		t.Error("the chapter packets were not copied through")
	}
	assertSameProjection(t, result, re)
}

// TestMusepackChapterEditRefused: chapters are read but not written, so an edit that
// would change them is refused, or dropped with a warning that says the file keeps
// what it had. A clear on a chapterless file and an unchanged set stay no-ops.
func TestMusepackChapterEditRefused(t *testing.T) {
	src := mpcChaptered()
	doc := mustParseBytes(t, src)
	for _, c := range []struct {
		name string
		edit func(*wl.Editor)
	}{
		{"set", func(e *wl.Editor) { e.SetChapters(wl.Chapter{Start: time.Second, Title: "New"}) }},
		{"clear", func(e *wl.Editor) { e.ClearChapters() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			ed := doc.Edit()
			c.edit(ed)
			_, err := ed.Prepare()
			// The exact gate wording: the writer's own backstop refuses differently.
			if !errors.Is(err, waxerr.ErrUnsupportedTag) || !strings.HasSuffix(err.Error(), "chapters cannot be written to a Musepack file") {
				t.Errorf("err = %v, want the editor's unsupported-tag refusal naming the format", err)
			}
		})
	}
	t.Run("dropped under the drop option", func(t *testing.T) {
		plan, err := doc.Edit().ClearChapters().Set(tag.Title, "Kept").Prepare(wl.WithAllowUnsupportedDrop())
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, w := range plan.Report().Warnings {
			if w.Code == wl.WarnChaptersUnsupported {
				found = true
				if !strings.Contains(w.Message, "read-only") || !strings.Contains(w.Message, "keeps its chapters") {
					t.Errorf("warning %q should say the chapters are read-only and kept", w.Message)
				}
			}
		}
		if !found {
			t.Error("expected a chapters-unsupported warning for the dropped clear")
		}
		re := mustParseBytes(t, applyToBytes(t, src, plan))
		if len(re.Chapters()) != 3 || re.Fields().Title != "Kept" {
			t.Errorf("after the dropped clear: %d chapters, title %q; want 3 and Kept", len(re.Chapters()), re.Fields().Title)
		}
		// A chapterless file has nothing to keep: the warning says the added chapters went.
		plan, err = mustParseBytes(t, mpcSV8(44100, 0, 0, 2)).Edit().
			SetChapters(wl.Chapter{Start: time.Second, Title: "New"}).Prepare(wl.WithAllowUnsupportedDrop())
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range plan.Report().Warnings {
			if w.Code == wl.WarnChaptersUnsupported && (strings.Contains(w.Message, "keeps") || !strings.Contains(w.Message, "added chapters were dropped")) {
				t.Errorf("warning %q should say the added chapters were dropped", w.Message)
			}
		}
	})
	t.Run("unchanged set is a no-op", func(t *testing.T) {
		plan, err := doc.Edit().SetChapters(doc.Chapters()...).Prepare()
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if !plan.IsNoOp() {
			t.Error("re-setting the file's own chapters should plan no change")
		}
	})
	t.Run("clear on a chapterless file is a no-op", func(t *testing.T) {
		plan, err := mustParseBytes(t, mpcSV8(44100, 0, 0, 2)).Edit().ClearChapters().Prepare()
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if !plan.IsNoOp() || len(plan.Report().Warnings) != 0 {
			t.Errorf("clear on a chapterless file: noop=%v warnings=%v", plan.IsNoOp(), plan.Report().Warnings)
		}
	})
}

// TestMusepackChaptersTransfer is the transcode case: a Musepack source's chapters
// carry into a destination that stores them, and a destination Musepack file keeps
// its own chapters while the report says why the source's were not written.
func TestMusepackChaptersTransfer(t *testing.T) {
	t.Run("out of Musepack", func(t *testing.T) {
		src := mustParseBytes(t, mpcChaptered())
		dstBytes := readFixture(t, notagsFLAC)
		plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
		if err != nil {
			t.Fatal(err)
		}
		var carried int
		for _, it := range report.Items {
			if it.Kind == wl.TransferChapter && it.Disposition == wl.Carried {
				carried += it.Count
			}
		}
		if carried != 3 {
			t.Errorf("report carried %d chapters, want 3: %+v", carried, report.Items)
		}
		got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters()
		if len(got) != 3 || got[1].Title != "Middle" || got[1].Start != 30*time.Second {
			t.Errorf("destination chapters = %+v", got)
		}
	})
	t.Run("into Musepack", func(t *testing.T) {
		src := mustParseBytes(t, flacWithComments("TITLE=Book", "CHAPTER001=00:00:00.000", "CHAPTER001NAME=One"))
		dstBytes := mpcChaptered()
		plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
		if err != nil {
			t.Fatal(err)
		}
		var dropped *wl.TransferItem
		for i := range report.Items {
			if report.Items[i].Kind == wl.TransferChapter {
				dropped = &report.Items[i]
			}
		}
		if dropped == nil || dropped.Disposition != wl.Dropped || !strings.Contains(dropped.Reason, "cannot write chapters") {
			t.Errorf("chapter item = %+v, want dropped because the destination cannot write chapters", dropped)
		}
		re := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
		if chs := re.Chapters(); len(chs) != 3 || chs[0].Title != "Intro" {
			t.Errorf("destination chapters after the copy = %+v, want its own three", chs)
		}
		if re.Fields().Title != "Book" {
			t.Errorf("title = %q, want the carried Book", re.Fields().Title)
		}
	})
}

func TestMusepackSV7HasNoChapterStore(t *testing.T) {
	caps := mustParseBytes(t, mpcSV7(100, 0)).Capabilities().Chapters
	if caps.Read != wl.AccessNone || caps.Write != wl.AccessNone {
		t.Errorf("SV7 chapters capability = read %s, write %s; want none", caps.Read, caps.Write)
	}
}

func TestMusepackNativeDescribesChapterPackets(t *testing.T) {
	for _, e := range mustParseBytes(t, mpcChaptered()).Native().Describe() {
		if strings.Contains(e.Note, "3 chapters") {
			return
		}
	}
	t.Error("the native view should describe the chapter packets with their count")
}
