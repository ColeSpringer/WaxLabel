package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// chaptersFromTitles builds chapters at one-second spacing from titles (none of which
// contain "mdat", so bytes.Count(out, "mdat") reliably counts mdat atoms).
func chaptersFromTitles(titles ...string) []wl.Chapter {
	chs := make([]wl.Chapter, len(titles))
	for i, title := range titles {
		chs[i] = wl.Chapter{Start: time.Duration(i) * time.Second, Title: title}
	}
	return chs
}

// QuickTime chapter write appends a chapter-sample mdat at end-of-file. Each subsequent rewrite
// must reclaim the prior one rather than leak it, so the mdat count stays flat from the second edit
// onward and a clear returns it to baseline.
func TestMP4ChapterMdatFlatAndEssenceStable(t *testing.T) {
	base := testfile.MP4AssembleUdta() // ftyp + audio moov + audio mdat, no chapters
	baseline := bytes.Count(base, []byte("mdat"))
	if baseline != 1 {
		t.Fatalf("baseline mdat count = %d, want 1 (the audio mdat)", baseline)
	}
	baseEssence := essenceOf(t, base)

	setChapters := func(src []byte, titles ...string) []byte {
		plan, err := mustParseBytes(t, src).Edit().SetChapters(chaptersFromTitles(titles...)...).Prepare()
		if err != nil {
			t.Fatalf("set chapters %v: %v", titles, err)
		}
		return applyToBytes(t, src, plan)
	}

	// The first edit creates the QuickTime chapter track and appends its mdat: the count rises
	// here (there was no chapter mdat before).
	v := setChapters(base, "A", "B")
	firstCount := bytes.Count(v, []byte("mdat"))
	if firstCount <= baseline {
		t.Fatalf("first chapter write should add a chapter mdat; count %d, baseline %d", firstCount, baseline)
	}
	if e := essenceOf(t, v); !e.Equal(baseEssence) {
		t.Errorf("essence drifted on the first chapter write")
	}

	// Successive rewrites (varying chapter counts) must reclaim the prior chapter mdat:
	// the total stays flat and the essence stays stable.
	rewrites := [][]string{{"C", "D", "E"}, {"F"}, {"G", "H", "I", "J"}, {"K", "L"}}
	for i, titles := range rewrites {
		v = setChapters(v, titles...)
		if c := bytes.Count(v, []byte("mdat")); c != firstCount {
			t.Errorf("rewrite %d leaked an mdat: count %d, want flat at %d", i+2, c, firstCount)
		}
		if e := essenceOf(t, v); !e.Equal(baseEssence) {
			t.Errorf("rewrite %d: essence drifted from baseline", i+2)
		}
	}

	// Clearing removes the chapter track and reclaims the chapter mdat: back to baseline.
	clearPlan, err := mustParseBytes(t, v).Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatalf("clear chapters: %v", err)
	}
	cleared := applyToBytes(t, v, clearPlan)
	if c := bytes.Count(cleared, []byte("mdat")); c != baseline {
		t.Errorf("clear did not restore the baseline mdat count: %d, want %d", c, baseline)
	}
	if e := essenceOf(t, cleared); !e.Equal(baseEssence) {
		t.Errorf("essence not restored to baseline after clear")
	}
}

// mp4OpenEndedMdat declares the mdat that ends data as running to the end of the file.
func mp4OpenEndedMdat(t *testing.T, data []byte) []byte {
	t.Helper()
	mdat, size := mp4FindBox(t, data, 0, len(data), "mdat")
	if mdat+size != len(data) {
		t.Fatal("mdat does not end the file")
	}
	out := slices.Clone(data)
	binary.BigEndian.PutUint32(out[mdat:], 0)
	return out
}

// A final mdat declared as running to the end of the file takes its real size before a chapter
// mdat goes behind it, so the chapter samples never read as audio.
func TestMP4ChapterWriteOnOpenEndedMdat(t *testing.T) {
	sized := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
	base := mp4OpenEndedMdat(t, sized)
	essence := essenceOf(t, base)
	if !essence.Equal(essenceOf(t, sized)) {
		t.Fatal("the open-ended file reads another essence than the sized one")
	}
	edit := func(src []byte, apply func(*wl.Editor) *wl.Editor) []byte {
		plan, err := apply(mustParseBytes(t, src).Edit()).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		return applyToBytes(t, src, plan)
	}

	out := edit(base, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("A", "B")...) })
	if _, size := mp4FindBox(t, out, 0, len(out), "mdat"); size != 8+120 {
		t.Errorf("the audio mdat declares %d bytes, want the %d it holds", size, 8+120)
	}
	if re := mustParseBytes(t, out); len(re.Chapters()) != 2 || !essenceOf(t, out).Equal(essence) {
		t.Errorf("%d chapter(s), essence equal %v", len(re.Chapters()), essenceOf(t, out).Equal(essence))
	}

	out = edit(out, func(e *wl.Editor) *wl.Editor { return e.SetChapters(chaptersFromTitles("C")...) })
	cleared := edit(out, func(e *wl.Editor) *wl.Editor { return e.ClearChapters() })
	if len(cleared) != len(base) || !essenceOf(t, cleared).Equal(essence) {
		t.Errorf("cleared: %d bytes, want the source's %d; essence equal %v", len(cleared), len(base), essenceOf(t, cleared).Equal(essence))
	}

	// No write that appends nothing sizes the mdat.
	tagged := edit(base, func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "Retitled") })
	if !bytes.HasSuffix(tagged, base[bytes.LastIndex(base, []byte("mdat"))-4:]) {
		t.Error("a tag edit rewrote the open-ended mdat header")
	}
}

// The 32-bit size field cannot say how long an open-ended mdat past 4 GiB is, so a chapter track
// cannot go behind one.
func TestMP4ChapterWriteRefusedOnOpenEndedMdatOver4GiB(t *testing.T) {
	src := sparse{size: 5 << 30, pieces: map[int64][]byte{0: mp4OpenEndedMdat(t, testfile.MP4AssembleUdta())}}
	doc, err := wl.Parse(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Edit().SetChapters(chaptersFromTitles("A")...).Prepare(); !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("chapter write: %v, want ErrUnsupportedFormat", err)
	}
	if _, err := doc.Edit().Set(tag.Title, "T").Prepare(); err != nil {
		t.Errorf("tag edit: %v", err)
	}
}

// An mdat cut short of its declared size reads to the end of the file, so it would take in a
// chapter mdat appended behind it.
func TestMP4ChapterWriteRefusedBehindTruncatedMdat(t *testing.T) {
	data := testfile.MP4AssembleUdta()
	mdat, size := mp4FindBox(t, data, 0, len(data), "mdat")
	binary.BigEndian.PutUint32(data[mdat:], uint32(size+100))
	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnTruncatedAudio) {
		t.Fatalf("warnings %v, want truncated-audio", warningCodes(doc))
	}
	if _, err := doc.Edit().SetChapters(chaptersFromTitles("A")...).Prepare(); !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("chapter write: %v, want ErrUnsupportedFormat", err)
	}
	if _, err := doc.Edit().Set(tag.Title, "T").Prepare(); err != nil {
		t.Errorf("tag edit: %v", err)
	}
}

// A chapter mdat goes behind the last whole atom, ahead of bytes that are none: appended after
// them it would read as part of them, so no rewrite could find it to remove it. A second moov
// that reaches the end of the file only by its declared size is such bytes too.
func TestMP4ChapterMdatGoesAheadOfTrailingBytes(t *testing.T) {
	tagged := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
	ftyp := testfile.MP4Ftyp()
	moovLast := slices.Concat(ftyp, testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 120)),
		testfile.MP4Atom("moov", testfile.MP4SoundTrak(1, uint32(len(ftyp)+8))))
	// Two files joined, the second moov declaring the given size.
	joined := func(declare func(size uint32) uint32) (data, tail []byte) {
		data = slices.Concat(moovLast, moovLast)
		at := bytes.LastIndex(data, []byte("moov")) - 4
		binary.BigEndian.PutUint32(data[at:], declare(binary.BigEndian.Uint32(data[at:])))
		return data, data[at:]
	}
	type shape struct{ data, tail []byte }
	behind := func(tail []byte) shape { return shape{slices.Concat(tagged, tail), tail} }
	cut, cutTail := joined(func(size uint32) uint32 { return size + 10 })
	open, openTail := joined(func(uint32) uint32 { return 0 })
	for name, c := range map[string]shape{
		"an ID3v1 tag":            behind(testfile.ID3v1("Title", "", "", "", "", 255)),
		"three bytes":             behind([]byte("abc")),
		"three zero bytes":        behind([]byte{0, 0, 0}),
		"a size below a header":   behind([]byte{0, 0, 0, 5}),
		"an open-ended free box":  behind(append([]byte{0, 0, 0, 0, 'f', 'r', 'e', 'e'}, make([]byte, 40)...)),
		"a second moov cut short": {cut, cutTail},
		"a second moov of size 0": {open, openTail},
	} {
		t.Run(name, func(t *testing.T) {
			essence := essenceOf(t, c.data)
			out := c.data
			var sizes []int
			for _, titles := range [][]string{{"A", "B"}, {"C", "D"}, nil} {
				plan, err := mustParseBytes(t, out).Edit().SetChapters(chaptersFromTitles(titles...)...).Prepare()
				if err != nil {
					t.Fatalf("chapters %q: %v", titles, err)
				}
				out = applyToBytes(t, out, plan)
				sizes = append(sizes, len(out))
				if re := mustParseBytes(t, out); len(re.Chapters()) != len(titles) || !bytes.HasSuffix(out, c.tail) || !essenceOf(t, out).Equal(essence) {
					t.Errorf("chapters %q: %d read back, tail kept %v, digest equal %v",
						titles, len(re.Chapters()), bytes.HasSuffix(out, c.tail), essenceOf(t, out).Equal(essence))
				}
			}
			if sizes[0] != sizes[1] || sizes[2] != len(c.data) {
				t.Errorf("sizes after two writes and a clear %v, want the two equal and the source's %d", sizes, len(c.data))
			}
		})
	}
}

// A moov that runs to the end of the file by its declared size takes its real size when a chapter
// mdat goes behind it, even on a rewrite that leaves its length as it was.
func TestMP4ChapterRewriteSizesAMoovThatRunsToTheEnd(t *testing.T) {
	ftyp := testfile.MP4Ftyp()
	base := slices.Concat(ftyp, testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 120)),
		testfile.MP4Atom("moov", testfile.MP4SoundTrak(1, uint32(len(ftyp)+8))))
	rewrite := func(src []byte, titles ...string) []byte {
		plan, err := mustParseBytes(t, src).Edit().SetChapters(chaptersFromTitles(titles...)...).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		return applyToBytes(t, src, plan)
	}
	// Move the chapter mdat a first write appends ahead of the moov, and leave the moov open-ended.
	written := rewrite(base, "AA", "BB")
	moov, moovLen := mp4FindBox(t, written, 0, len(written), "moov")
	chap := written[moov+moovLen:]
	entry := binary.BigEndian.AppendUint32(nil, uint32(moov+moovLen+8))
	if bytes.Count(written[moov:moov+moovLen], entry) != 1 {
		t.Fatal("the chapter chunk offset is not unique in the moov")
	}
	openMoov := bytes.Replace(written[moov:moov+moovLen], entry, binary.BigEndian.AppendUint32(nil, uint32(moov+8)), 1)
	binary.BigEndian.PutUint32(openMoov, 0)
	data := slices.Concat(written[:moov], chap, openMoov)
	if chs := mustParseBytes(t, data).Chapters(); len(chs) != 2 || chs[1].Title != "BB" {
		t.Fatalf("setup: chapters %+v", chs)
	}

	first := rewrite(data, "CC", "DD")
	second := rewrite(first, "EE", "FF")
	if chs := mustParseBytes(t, second).Chapters(); len(chs) != 2 || chs[1].Title != "FF" || len(second) != len(first) {
		t.Errorf("chapters %+v, %d bytes after the second rewrite and %d after the first", chs, len(second), len(first))
	}
}

// A chapter mdat that ends the file open-ended or short of its declared size is the one a rewrite
// removes, so its size needs no settling.
func TestMP4ChapterRewriteReclaimsAnUnsizedChapterMdat(t *testing.T) {
	rewrite := func(src []byte, titles ...string) []byte {
		plan, err := mustParseBytes(t, src).Edit().SetChapters(chaptersFromTitles(titles...)...).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		return applyToBytes(t, src, plan)
	}
	written := rewrite(testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T")), "A", "B")
	want := rewrite(written, "C")
	at := bytes.LastIndex(written, []byte("mdat")) - 4
	for name, declared := range map[string]uint32{"open-ended": 0, "cut short": binary.BigEndian.Uint32(written[at:]) + 100} {
		t.Run(name, func(t *testing.T) {
			data := slices.Clone(written)
			binary.BigEndian.PutUint32(data[at:], declared)
			if got := rewrite(data, "C"); !bytes.Equal(got, want) {
				t.Errorf("the rewrite differs from that of the sized file (%d bytes, want %d)", len(got), len(want))
			}
		})
	}
}

// Bytes behind the last whole atom that a table names stay where the table says: nothing moves
// them, so the chapter mdat goes after them, not ahead.
func TestMP4ChapterMdatStaysBehindBytesATableNames(t *testing.T) {
	media := bytes.Repeat([]byte{0xD4}, 64)
	build := func(first, second uint32) []byte {
		moov := testfile.MP4Atom("moov", slices.Concat(testfile.MP4SoundTrak(1, first), testfile.MP4SoundTrak(2, second)))
		return slices.Concat(testfile.MP4Ftyp(), moov, testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 120)),
			[]byte{0, 0, 0, 0, 'j', 'u', 'n', 'k'}, media)
	}
	probe := build(0, 0)
	data := build(uint32(bytes.Index(probe, []byte("mdat"))+4), uint32(bytes.Index(probe, []byte("junk"))+4))
	plan, err := mustParseBytes(t, data).Edit().SetChapters(chaptersFromTitles("A", "B")...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	first := bytes.Index(out, []byte("stco"))
	second := first + 4 + bytes.Index(out[first+4:], []byte("stco"))
	at := int(binary.BigEndian.Uint32(out[second+12:]))
	if at+len(media) > len(out) || !bytes.Equal(out[at:at+len(media)], media) {
		t.Errorf("the second track's chunk offset %d no longer points at its media", at)
	}
	if chs := mustParseBytes(t, out).Chapters(); len(chs) != 2 {
		t.Errorf("chapters %+v", chs)
	}
}
