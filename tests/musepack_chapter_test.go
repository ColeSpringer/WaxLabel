package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// SV8 chapter synthesis.

// mpcSeekOffset builds an SO packet whose pointer lands gap bytes past the packet's own end.
func mpcSeekOffset(gap int) []byte {
	ptr := gap + 4
	for {
		so := testfile.MPCPacket("SO", testfile.MPCVarlen(uint64(ptr)))
		if len(so)+gap == ptr {
			return so
		}
		ptr = len(so) + gap
	}
}

// mpcSeekTable is an empty seek table packet.
func mpcSeekTable() []byte { return testfile.MPCPacket("ST", []byte{0, 0}) }

// chaptersMPC is an mpcenc stream mpcchap r475 wrote chapters into: Title=Intro at sample 0, a
// three-item chapter at 8000 whose title item comes last, TITLE=Coda at 16000, and an untitled
// chapter at 19000 carrying no tag bytes.
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
	doc := mustParseBytes(t, testfile.MPCChaptered())
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

// where the reference decoder looks for chapters: right after the seek table an SO packet points
// at, or else the run of CT packets that ends at the end marker.
func TestMusepackSV8ChapterRunPlacement(t *testing.T) {
	audio := testfile.MPCAudio(64)
	st := mpcSeekTable()
	for _, c := range []struct {
		name    string
		packets [][]byte
		want    []string
	}{
		{"run before the end marker",
			[][]byte{audio, testfile.MPCTitled(0, "a"), testfile.MPCTitled(1152, "b"), testfile.MPCEnd()}, []string{"a", "b"}},
		{"run after the seek table, without a seek offset",
			[][]byte{audio, st, testfile.MPCTitled(0, "a"), testfile.MPCEnd()}, []string{"a"}},
		{"run after the seek table the seek offset points at, audio following",
			[][]byte{mpcSeekOffset(len(audio)), audio, st, testfile.MPCTitled(0, "after st"), audio, testfile.MPCEnd()}, []string{"after st"}},
		{"run interrupted by audio is not the run before the end marker",
			[][]byte{audio, testfile.MPCTitled(0, "lost"), audio, testfile.MPCEnd()}, nil},
		{"seek offset pointing at a non-table packet falls back to the run before the end marker",
			[][]byte{mpcSeekOffset(0), audio, testfile.MPCTitled(0, "a"), testfile.MPCEnd()}, []string{"a"}},
		{"chapters at the front, before the audio, are not seen",
			[][]byte{testfile.MPCTitled(0, "front"), audio, testfile.MPCEnd()}, nil},
		{"no end marker",
			[][]byte{audio, testfile.MPCTitled(0, "a")}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, ch := range mustParseBytes(t, testfile.MPCStream(44100, 0, 2, c.packets...)).Chapters() {
				got = append(got, ch.Title)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("chapters = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMusepackSV8ChapterTagForms(t *testing.T) {
	for _, c := range []struct {
		name  string
		tag   []byte
		title string
	}{
		{"no tag: untitled", nil, ""},
		{"title item name in any case", testfile.MPCChapterTag([2]string{"TITLE", "Loud"}), "Loud"},
		{"other items are not the title", testfile.MPCChapterTag([2]string{"Track", "1/3"}, [2]string{"Artist", "A"}, [2]string{"Title", "T"}), "T"},
		{"a multi-valued title reads its first value", testfile.MPCChapterTag([2]string{"Title", "One\x00Two"}), "One"},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := testfile.MPCStream(44100, 0, 2, testfile.MPCAudio(8), testfile.MPCChapter(0, c.tag), testfile.MPCEnd())
			chs := mustParseBytes(t, data).Chapters()
			if len(chs) != 1 || chs[0].Title != c.title {
				t.Errorf("chapters = %+v, want one titled %q", chs, c.title)
			}
		})
	}
}

// A tag too short for its header record still yields an untitled chapter; a packet whose start
// sample runs off its end is skipped while the packets after it are still read; a start no
// duration can hold is skipped rather than placed at the start. Each fault is reported once
// however many packets share it, and every packet counts against the element cap.
func TestMusepackSV8MalformedChapterPackets(t *testing.T) {
	t.Run("start sample past any duration", func(t *testing.T) {
		data := testfile.MPCStream(44100, 0, 2, testfile.MPCAudio(8), testfile.MPCTitled(1<<62, "far"), testfile.MPCTitled(1152, "near"), testfile.MPCEnd())
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
		audio, bad := testfile.MPCAudio(8), testfile.MPCPacket("CT", []byte{0xFF, 0xFF})
		data := testfile.MPCStream(44100, 0, 2, mpcSeekOffset(len(audio)), audio, mpcSeekTable(), bad, bad, bad, bad, testfile.MPCTitled(0, "ok"), testfile.MPCEnd())
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
		data := testfile.MPCStream(44100, 0, 2, testfile.MPCAudio(8), testfile.MPCChapter(1152, []byte("junk")), testfile.MPCEnd())
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
		audio := testfile.MPCAudio(64)
		data := testfile.MPCStream(44100, 0, 2, mpcSeekOffset(len(audio)), audio, mpcSeekTable(), testfile.MPCPacket("CT", nil))
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
		// The packets lie inside the file, so a payload read that fails is an I/O fault the parse
		// returns, not a chapter reported malformed; the packet headers around it read.
		audio := testfile.MPCAudio(64)
		head := testfile.MPCStream(44100, 0, 2, mpcSeekOffset(len(audio)), audio, mpcSeekTable())
		data := append(append(head, testfile.MPCTitled(0, "x")...), testfile.MPCEnd()...)
		payload := int64(len(head) + 3)
		_, err := wl.Parse(context.Background(), &testfile.Faulty{Src: wl.BytesSource(data), Fails: func(off, _ int64) bool { return off == payload }})
		if !errors.Is(err, syscall.EIO) || errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("err = %v, want the read's own failure", err)
		}
	})
	t.Run("truncated start sample", func(t *testing.T) {
		data := testfile.MPCStream(44100, 0, 2, testfile.MPCAudio(8),
			testfile.MPCTitled(0, "kept"), testfile.MPCPacket("CT", []byte{0xFF, 0xFF}), testfile.MPCTitled(1152, "after"), testfile.MPCEnd())
		doc := mustParseBytes(t, data)
		if chs := doc.Chapters(); len(chs) != 2 || chs[0].Title != "kept" || chs[1].Title != "after" {
			t.Errorf("chapters = %+v, want the two readable chapters around the truncated packet", chs)
		}
		if !hasWarning(doc, wl.WarnMalformedTagEntry) {
			t.Error("expected a malformed-tag-entry warning for the truncated chapter packet")
		}
	})
}

// element cap bounds the packets walked to find the run, the packets of the run itself, and the
// items a chapter tag is read for, each reported when it trips.
func TestMusepackSV8ChapterElementCap(t *testing.T) {
	audio := testfile.MPCAudio(64)
	viaSeekTable := testfile.MPCStream(44100, 0, 2, mpcSeekOffset(len(audio)), audio, mpcSeekTable(),
		testfile.MPCTitled(0, "a"), testfile.MPCTitled(1152, "b"), testfile.MPCTitled(2304, "c"), testfile.MPCEnd())
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
	doc, err = wl.Parse(context.Background(), wl.BytesSource(testfile.MPCChaptered()), wl.WithLimits(wl.Limits{MaxElements: 2}))
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
	// cap is reported.
	items := make([][2]string, 0, 9)
	for i := range 8 {
		items = append(items, [2]string{"Comment", strings.Repeat("x", i+1)})
	}
	items = append(items, [2]string{"Title", "Last"})
	data := testfile.MPCStream(44100, 0, 2, testfile.MPCAudio(8), testfile.MPCChapter(0, testfile.MPCChapterTag(items...)), testfile.MPCEnd())
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

// once the run cap trips nothing more of the run is read, so a run of any length past the cap costs
// the same reads as one just over it.
func TestMusepackSV8ChapterRunCapBoundsWork(t *testing.T) {
	audio := testfile.MPCAudio(64)
	reads := func(n int) int {
		packets := [][]byte{mpcSeekOffset(len(audio)), audio, mpcSeekTable()}
		for i := range n {
			packets = append(packets, testfile.MPCTitled(uint64(i)*1152, "c"))
		}
		src := &countingSource{ReaderAtSized: wl.BytesSource(testfile.MPCStream(44100, 0, 2, append(packets, testfile.MPCEnd())...))}
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

// The walk to the SE packet resumes after the seek table an SO packet points at, so the audio packets
// it skips cost no reads.
func TestMusepackSEWalkSkipsTheAudio(t *testing.T) {
	reads := func(n int) int {
		aps := bytes.Repeat(testfile.MPCAudio(0), n)
		src := &countingSource{ReaderAtSized: wl.BytesSource(testfile.MPCStream(44100*60, 0, 2,
			mpcSeekOffset(len(aps)), aps, mpcSeekTable(), testfile.MPCEnd()))}
		if _, err := wl.Parse(context.Background(), src); err != nil {
			t.Fatal(err)
		}
		return src.reads
	}
	if short, long := reads(1000), reads(10000); long != short {
		t.Errorf("10000 audio packets behind the seek offset cost %d reads, 1000 cost %d; the walk should skip them", long, short)
	}
}

// MPCK stream must declare stream version 8 in its header packet, as the reference decoder and
// ffmpeg both require, and a packet key outside A-Z is not a packet, so a stream opening with one
// has no header.
func TestMusepackSV8StreamVersionAndKeys(t *testing.T) {
	data := mpcSV8(44100, 0, 0, 2)
	data[4+2+1+4] = 7 // the version byte after the SH key, size, and CRC
	if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("SH version 7 in an MPCK stream: err = %v, want ErrUnsupportedFormat", err)
	}
	data = append([]byte("MPCK"), testfile.MPCPacket("s1", nil)...)
	data = append(data, mpcSV8(44100, 0, 0, 2)[4:]...)
	if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("non-letter packet key ahead of SH: err = %v, want ErrInvalidData", err)
	}
}

func TestMusepackReservedRateReportsNoChapters(t *testing.T) {
	doc := mustParseBytes(t, testfile.MPCStream(44100, 5, 2, testfile.MPCAudio(8), testfile.MPCTitled(0, "a"), testfile.MPCEnd()))
	if caps := doc.Capabilities().Chapters; caps.Read != wl.AccessNone || len(doc.Chapters()) != 0 {
		t.Errorf("reserved rate index: chapters cannot be placed, so read = %s and %d chapters; want none", caps.Read, len(doc.Chapters()))
	}
}

// Chapter packets sit inside the stream a rewrite copies verbatim, so a tag edit keeps them.
func TestMusepackChaptersSurviveTagEdit(t *testing.T) {
	src := testfile.MPCChaptered()
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Edited").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if re := mustParseBytes(t, out); len(re.Chapters()) != 3 {
		t.Fatalf("chapters after a tag edit = %+v, want 3", re.Chapters())
	}
	if !bytes.Contains(out, []byte("Finale")) {
		t.Error("the chapter packets were not copied through")
	}
}

// chapters are read but not written, so an edit that would change them is refused, or dropped with
// a warning that says the file keeps what it had. A clear on a chapterless file and an unchanged
// set stay no-ops.
func TestMusepackChapterEditRefused(t *testing.T) {
	src := testfile.MPCChaptered()
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
			// The exact gate wording: the writer's own refusal reads differently.
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

// A Musepack source's chapters carry into a destination that stores them, and a destination
// Musepack file keeps its own chapters while the report says why the source's were not written.
func TestMusepackChaptersTransfer(t *testing.T) {
	t.Run("out of Musepack", func(t *testing.T) {
		src := mustParseBytes(t, testfile.MPCChaptered())
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
		dstBytes := testfile.MPCChaptered()
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
	for _, e := range mustParseBytes(t, testfile.MPCChaptered()).Native().Describe() {
		if strings.Contains(e.Note, "3 chapters") {
			return
		}
	}
	t.Error("the native view should describe the chapter packets with their count")
}
