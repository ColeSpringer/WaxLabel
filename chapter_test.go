package waxlabel_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// mp4Chpl builds a Nero chpl atom. version 1 includes a 4-byte reserved field
// before the count (the form ffmpeg writes); version 0 omits it. Each chapter is
// an 8-byte 100 ns start plus a length-prefixed UTF-8 title.
func mp4Chpl(version byte, starts []time.Duration, titles []string) []byte {
	body := []byte{version, 0, 0, 0}
	if version == 1 {
		body = append(body, 0, 0, 0, 0)
	}
	body = append(body, byte(len(titles)))
	for i, title := range titles {
		var s [8]byte
		binary.BigEndian.PutUint64(s[:], uint64(starts[i].Seconds()*10_000_000))
		body = append(body, s[:]...)
		body = append(body, byte(len(title)))
		body = append(body, title...)
	}
	return mp4Atom("chpl", body)
}

func TestMP4ChplReadVersions(t *testing.T) {
	starts := []time.Duration{0, 30 * time.Second, 90 * time.Second}
	titles := []string{"Opening", "Middle", "Finale"}
	for _, version := range []byte{0, 1} {
		chpl := mp4Chpl(version, starts, titles)
		data := mp4AssembleUdta(mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))), chpl)
		doc := mustParseBytes(t, data)
		chs := doc.Chapters()
		if len(chs) != 3 {
			t.Fatalf("v%d: got %d chapters, want 3", version, len(chs))
		}
		for i := range titles {
			if chs[i].Title != titles[i] {
				t.Errorf("v%d: chapter %d title = %q, want %q", version, i, chs[i].Title, titles[i])
			}
			if chs[i].Start != starts[i] {
				t.Errorf("v%d: chapter %d start = %v, want %v", version, i, chs[i].Start, starts[i])
			}
		}
		// End is filled from the next chapter's start (last stays zero = EOF).
		if chs[0].End != starts[1] {
			t.Errorf("v%d: chapter 0 End = %v, want %v", version, chs[0].End, starts[1])
		}
		if chs[2].End != 0 {
			t.Errorf("v%d: last chapter End = %v, want 0", version, chs[2].End)
		}
	}
}

func TestMP4ChapterWriteCreatesChpl(t *testing.T) {
	// A tagged file with no chpl: writing chapters appends a chpl, shifts the mdat,
	// and reads back with the same starts/titles.
	data := mp4Tagged(mp4Text("\xa9nam", "Audiobook"))
	doc := mustParseBytes(t, data)
	if len(doc.Chapters()) != 0 {
		t.Fatalf("expected no chapters, got %d", len(doc.Chapters()))
	}
	plan, err := doc.Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "Prologue"},
		wl.Chapter{Start: 12 * time.Second, Title: "Part One"},
		wl.Chapter{Start: 48 * time.Second, Title: "Part Two"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	mdatAfter, stcoAfter := mp4Index(t, out)
	if stcoAfter != mdatAfter {
		t.Errorf("stco entry %d must point at the moved mdat payload %d", stcoAfter, mdatAfter)
	}
	re := mustParseBytes(t, out)
	chs := re.Chapters()
	if len(chs) != 3 {
		t.Fatalf("after write, got %d chapters, want 3", len(chs))
	}
	if chs[1].Title != "Part One" || chs[1].Start != 12*time.Second {
		t.Errorf("chapter 1 = %q@%v, want Part One@12s", chs[1].Title, chs[1].Start)
	}
	// The title atom must still read, proving the udta tags survived the rewrite.
	if re.Fields().Title != "Audiobook" {
		t.Errorf("title after chapter write = %q", re.Fields().Title)
	}
}

func TestMP4ChapterEditExistingChpl(t *testing.T) {
	// A file that already has a chpl: editing the chapters replaces it.
	chpl := mp4Chpl(1, []time.Duration{0, 60 * time.Second}, []string{"Old A", "Old B"})
	data := mp4AssembleUdta(mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))), chpl)
	doc := mustParseBytes(t, data)
	if len(doc.Chapters()) != 2 {
		t.Fatalf("setup: got %d chapters, want 2", len(doc.Chapters()))
	}
	plan, err := doc.Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "New Intro"},
		wl.Chapter{Start: 20 * time.Second, Title: "New Mid"},
		wl.Chapter{Start: 40 * time.Second, Title: "New End"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("Old A")) || bytes.Contains(out, []byte("Old B")) {
		t.Error("old chapter titles survived a chapter edit")
	}
	chs := mustParseBytes(t, out).Chapters()
	if len(chs) != 3 || chs[0].Title != "New Intro" || chs[2].Title != "New End" {
		t.Errorf("edited chapters = %+v", chs)
	}
	mdatAfter, stcoAfter := mp4Index(t, out)
	if stcoAfter != mdatAfter {
		t.Errorf("stco %d must point at mdat %d after chapter edit", stcoAfter, mdatAfter)
	}
}

func TestMP4ChapterClearRemovesChpl(t *testing.T) {
	chpl := mp4Chpl(1, []time.Duration{0}, []string{"Solo"})
	data := mp4AssembleUdta(mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))), chpl)
	plan, err := mustParseBytes(t, data).Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("chpl")) {
		t.Error("chpl atom survived ClearChapters")
	}
	if len(mustParseBytes(t, out).Chapters()) != 0 {
		t.Error("chapters present after ClearChapters")
	}
}

func TestMP4ChapterTagAndChapterTogether(t *testing.T) {
	// Editing tags and chapters in one pass: both land, offsets stay valid, and the
	// audio payload survives verbatim.
	chpl := mp4Chpl(1, []time.Duration{0}, []string{"First"})
	data := mp4AssembleUdta(mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "Old"))), chpl)
	plan, err := mustParseBytes(t, data).Edit().
		Set(tag.Title, "A Much Longer Title Forcing The Region To Grow").
		SetChapters(wl.Chapter{Start: 0, Title: "First"}, wl.Chapter{Start: 5 * time.Second, Title: "Second"}).
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	re := mustParseBytes(t, out)
	if re.Fields().Title != "A Much Longer Title Forcing The Region To Grow" {
		t.Errorf("title = %q", re.Fields().Title)
	}
	if len(re.Chapters()) != 2 || re.Chapters()[1].Title != "Second" {
		t.Errorf("chapters = %+v", re.Chapters())
	}
	mdatAfter, stcoAfter := mp4Index(t, out)
	if stcoAfter != mdatAfter {
		t.Errorf("stco %d must point at mdat %d", stcoAfter, mdatAfter)
	}
	if !bytes.Equal(out[mdatAfter:mdatAfter+120], bytes.Repeat([]byte{0xA7}, 120)) {
		t.Error("audio payload not preserved through a combined tag+chapter edit")
	}
}

func TestMP4ChapterPreservesUnknownUdtaSibling(t *testing.T) {
	// An unknown udta sibling (here a cprt copyright atom) must survive a chapter
	// rewrite byte-for-byte - the udta is spliced, not rebuilt from a fixed shape.
	cprt := mp4Atom("cprt", append([]byte{0, 0, 0, 0}, []byte("PRESERVE-THIS-NOTICE")...))
	chpl := mp4Chpl(1, []time.Duration{0}, []string{"One"})
	data := mp4AssembleUdta(mp4Meta(mp4HdlrMdir(), mp4Ilst(mp4Text("\xa9nam", "T"))), cprt, chpl)
	plan, err := mustParseBytes(t, data).Edit().
		SetChapters(wl.Chapter{Start: 0, Title: "Renamed"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("PRESERVE-THIS-NOTICE")) {
		t.Error("unknown udta sibling lost on a chapter rewrite")
	}
	if mustParseBytes(t, out).Chapters()[0].Title != "Renamed" {
		t.Error("chapter edit did not apply")
	}
}

func TestMP4ChapterTooManyRejected(t *testing.T) {
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	chs := make([]wl.Chapter, 256)
	for i := range chs {
		chs[i] = wl.Chapter{Start: time.Duration(i) * time.Second, Title: "c"}
	}
	_, err := mustParseBytes(t, data).Edit().SetChapters(chs...).Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("error = %v, want ErrUnsupportedTag", err)
	}
}

func TestMP4ChapterTitleTruncatedTo255(t *testing.T) {
	// A title longer than the 8-bit chpl length prefix is truncated, not corrupted,
	// and the truncation is surfaced (never silent).
	long := string(bytes.Repeat([]byte("x"), 300))
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	plan, err := mustParseBytes(t, data).Edit().SetChapters(wl.Chapter{Start: 0, Title: long}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	truncated := false
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnChapterTitleTruncated {
			truncated = true
		}
	}
	if !truncated {
		t.Errorf("expected a chapter-title-truncated warning; got %v", plan.Report().Warnings)
	}
	chs := mustParseBytes(t, applyToBytes(t, data, plan)).Chapters()
	if len(chs) != 1 || len(chs[0].Title) != 255 {
		t.Fatalf("truncated title length = %d, want 255", len(chs[0].Title))
	}
}

func TestMP4ChapterStartRoundsNotTruncates(t *testing.T) {
	// A chapter start is encoded in the chapter track's timescale, so a finer start
	// must round to the nearest unit, not truncate down (which would silently lose
	// time). The QuickTime track wins on read; with the default movie timescale
	// (1 ms) a 2.7006 s start rounds to 2.701 s, where truncation would give 2.700 s.
	// (The rounding is checked on the second chapter: the first is the track's
	// time-zero anchor, which a sample table always reads back as 0.)
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	start := 2700600 * time.Microsecond // 2700.6 ms -> rounds to 2701
	plan, err := mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "A"},
		wl.Chapter{Start: start, Title: "X"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	chs := mustParseBytes(t, applyToBytes(t, data, plan)).Chapters()
	want := 2701 * time.Millisecond // nearest ms (truncation would give 2700 ms)
	if len(chs) != 2 || chs[1].Start != want {
		t.Errorf("rounded start = %v, want %v", chs[1].Start, want)
	}
}

func TestMP4AudiobookFieldsRoundTrip(t *testing.T) {
	// stik (media kind), desc/ldes descriptions, and a NARRATOR freeform read into
	// the canonical model and survive an unrelated edit.
	data := mp4Tagged(
		mp4Atom("stik", mp4Data(21, []byte{2})), // 2 = audiobook
		mp4Text("desc", "Short blurb"),
		mp4Text("ldes", "A longer description of the work."),
		mp4Freeform("com.apple.iTunes", "NARRATOR", "Jane Reader"),
	)
	check := func(d *wl.Document) {
		t.Helper()
		for key, want := range map[tag.Key]string{
			tag.MediaType:       "2",
			tag.Description:     "Short blurb",
			tag.LongDescription: "A longer description of the work.",
			tag.Narrator:        "Jane Reader",
		} {
			if v, ok := d.Get(key); !ok || len(v) != 1 || v[0] != want {
				t.Errorf("%s = %v (ok=%v), want %q", key, v, ok, want)
			}
		}
	}
	doc := mustParseBytes(t, data)
	check(doc)
	plan, err := doc.Edit().Set(tag.Title, "Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	check(mustParseBytes(t, applyToBytes(t, data, plan)))
}

func TestMP4MediaTypeWriteCreatesStik(t *testing.T) {
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.MediaType, "2").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("stik")) {
		t.Error("MediaType write did not produce a stik atom")
	}
	if v, _ := mustParseBytes(t, out).Get(tag.MediaType); len(v) != 1 || v[0] != "2" {
		t.Errorf("MediaType after write = %v", v)
	}
}

func TestMP4ChapterRoundTripStable(t *testing.T) {
	// Same input + same edit must yield identical bytes (deterministic).
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	edit := func() []byte {
		plan, err := mustParseBytes(t, data).Edit().
			SetChapters(wl.Chapter{Start: 0, Title: "A"}, wl.Chapter{Start: time.Second, Title: "B"}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		return applyToBytes(t, data, plan)
	}
	if !bytes.Equal(edit(), edit()) {
		t.Error("chapter write is not deterministic")
	}
}
