package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// Synthetic QuickTime chapter text track. ffmpeg writes such a track alongside
// the chpl when it adds chapters; these builders let the reader be exercised
// without ffmpeg (the real fixture covers the integration). Layout: an audio
// trak whose tref "chap" points at a text trak, and that text trak's sample
// tables locating one text sample (2-byte length + UTF-8) per chapter in mdat.

func mp4be16(n int) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, uint16(n))
	return b
}

// mp4AudioTrakChap builds an audio trak (track_id 1) that references the chapter
// text track via a tref "chap", with its single stco entry at audioStco.
func mp4AudioTrakChap(chapTrackID int, audioStco uint32) []byte {
	tkhd := mp4Atom("tkhd", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), mp4be32(1), make([]byte, 4)))
	tref := mp4Atom("tref", mp4Atom("chap", mp4be32(chapTrackID)))
	stbl := mp4Atom("stbl", slices.Concat(mp4StsdAudio(), mp4Stco(audioStco)))
	minf := mp4Atom("minf", stbl)
	mdia := mp4Atom("mdia", slices.Concat(mp4HdlrSoun(), mp4Mdhd(), minf))
	return mp4Atom("trak", slices.Concat(tkhd, tref, mdia))
}

// mp4TextTrak builds a chapter text trak: one sample per title at the given media
// timescale, with the chunk offset at textStco.
func mp4TextTrak(trackID, timescale int, startsMS []int, titles []string, textStco uint32) []byte {
	var stts []byte
	for i := range titles {
		delta := 1000
		if i+1 < len(startsMS) {
			delta = startsMS[i+1] - startsMS[i]
		}
		stts = append(stts, slices.Concat(mp4be32(1), mp4be32(delta))...)
	}
	sttsAtom := mp4Atom("stts", slices.Concat([]byte{0, 0, 0, 0}, mp4be32(len(titles)), stts))
	stscAtom := mp4Atom("stsc", slices.Concat([]byte{0, 0, 0, 0}, mp4be32(1), mp4be32(1), mp4be32(len(titles)), mp4be32(1)))
	var sizes []byte
	for _, t := range titles {
		sizes = append(sizes, mp4be32(2+len(t))...)
	}
	stszAtom := mp4Atom("stsz", slices.Concat([]byte{0, 0, 0, 0}, mp4be32(0), mp4be32(len(titles)), sizes))
	stcoAtom := mp4Atom("stco", slices.Concat([]byte{0, 0, 0, 0}, mp4be32(1), mp4be32(int(textStco))))
	stbl := mp4Atom("stbl", slices.Concat(sttsAtom, stscAtom, stszAtom, stcoAtom))
	minf := mp4Atom("minf", stbl)
	mdhd := mp4Atom("mdhd", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), mp4be32(timescale), mp4be32(0)))
	hdlr := mp4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("text"), make([]byte, 12)))
	mdia := mp4Atom("mdia", slices.Concat(hdlr, mdhd, minf))
	tkhd := mp4Atom("tkhd", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), mp4be32(trackID), make([]byte, 4)))
	return mp4Atom("trak", slices.Concat(tkhd, mdia))
}

// mp4SamplesBlob renders the text samples (length-prefixed UTF-8) that the text
// track's stco points at.
func mp4SamplesBlob(titles []string) []byte {
	var blob []byte
	for _, t := range titles {
		blob = append(blob, slices.Concat(mp4be16(len(t)), []byte(t))...)
	}
	return blob
}

// mp4QTFile assembles a file with a QuickTime chapter track (and optional extra
// udta children, e.g. a chpl), patching both the audio and text stco offsets to
// point at their data in the trailing mdat.
func mp4QTFile(startsMS []int, titles []string, udtaKids ...[]byte) []byte {
	return mp4QTFileTS(1000, startsMS, titles, udtaKids...)
}

// mp4QTFileTS is mp4QTFile with an explicit media timescale (to exercise edge
// cases such as a zero timescale).
func mp4QTFileTS(timescale int, startsMS []int, titles []string, udtaKids ...[]byte) []byte {
	audioFiller := bytes.Repeat([]byte{0xA7}, 120)
	samples := mp4SamplesBlob(titles)
	build := func(audioStco, textStco uint32) []byte {
		kids := slices.Concat(mp4AudioTrakChap(2, audioStco), mp4TextTrak(2, timescale, startsMS, titles, textStco))
		if len(udtaKids) > 0 {
			kids = append(kids, mp4Atom("udta", slices.Concat(udtaKids...))...)
		}
		moov := mp4Atom("moov", kids)
		mdat := mp4Atom("mdat", slices.Concat(audioFiller, samples))
		return slices.Concat(mp4Ftyp(), moov, mdat)
	}
	tmp := build(0, 0)
	mdatIdx := bytes.Index(tmp, []byte("mdat"))
	audioOff := mdatIdx + 4
	textOff := audioOff + len(audioFiller)
	return build(uint32(audioOff), uint32(textOff))
}

func TestMP4QTChapterRead(t *testing.T) {
	startsMS := []int{0, 5000, 12000}
	titles := []string{"Cold Open", "Act One", "Act Two"}
	data := mp4QTFile(startsMS, titles)
	doc := mustParseBytes(t, data)
	chs := doc.Chapters()
	if len(chs) != 3 {
		t.Fatalf("got %d QuickTime chapters, want 3", len(chs))
	}
	for i, want := range titles {
		if chs[i].Title != want {
			t.Errorf("chapter %d title = %q, want %q", i, chs[i].Title, want)
		}
		if chs[i].Start != time.Duration(startsMS[i])*time.Millisecond {
			t.Errorf("chapter %d start = %v, want %dms", i, chs[i].Start, startsMS[i])
		}
	}
	// The QuickTime track carries End from the next sample's start.
	if chs[0].End != 5*time.Second {
		t.Errorf("chapter 0 End = %v, want 5s", chs[0].End)
	}
	// The native view notes the preserved chapter track.
	hasQT := false
	for _, e := range doc.Native().Describe() {
		if e.Note == "QuickTime chapter text track (preserved)" {
			hasQT = true
		}
	}
	if !hasQT {
		t.Error("native view should note the QuickTime chapter track")
	}
}

func TestMP4ChapterSourceConflict(t *testing.T) {
	// A chpl and a QuickTime track that disagree (different titles) must warn, and
	// the richer QuickTime representation wins the projection.
	startsMS := []int{0, 5000}
	qtTitles := []string{"QT Intro", "QT Body"}
	chpl := mp4Chpl(1, []time.Duration{0, 5 * time.Second}, []string{"Nero Intro", "Nero Body"})
	data := mp4QTFile(startsMS, qtTitles, chpl)

	doc := mustParseBytes(t, data)
	if !hasWarning(doc, wl.WarnChapterSourceConflict) {
		t.Errorf("expected a chapter-source-conflict warning; got %v", doc.Warnings())
	}
	if chs := doc.Chapters(); len(chs) != 2 || chs[0].Title != "QT Intro" {
		t.Errorf("conflict resolution should prefer the QuickTime track; got %+v", chs)
	}
}

func TestMP4ChapterAgreementNoConflict(t *testing.T) {
	// A chpl and a QuickTime track that agree must NOT warn.
	startsMS := []int{0, 8000}
	titles := []string{"Same A", "Same B"}
	chpl := mp4Chpl(1, []time.Duration{0, 8 * time.Second}, titles)
	data := mp4QTFile(startsMS, titles, chpl)
	doc := mustParseBytes(t, data)
	if hasWarning(doc, wl.WarnChapterSourceConflict) {
		t.Errorf("agreeing chapter sources should not warn; got %v", doc.Warnings())
	}
}

func TestMP4ChapterStaleWarningOnEdit(t *testing.T) {
	// Editing chapters on a file with a QuickTime track writes only the chpl and
	// preserves the QuickTime track, which is then stale: the plan must warn, and
	// the preserved track's sample bytes must survive.
	data := mp4QTFile([]int{0, 5000}, []string{"Original One", "Original Two"})
	plan, err := mustParseBytes(t, data).Edit().
		SetChapters(wl.Chapter{Start: 0, Title: "Edited One"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	stale := false
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnChaptersStale {
			stale = true
		}
	}
	if !stale {
		t.Errorf("expected a chapters-stale warning; got %v", plan.Report().Warnings)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("Original One")) {
		t.Error("preserved QuickTime chapter sample bytes were lost on a chapter edit")
	}
	// The new chpl is what we read back now (chpl preferred only when QT absent —
	// here the QT track is stale, so the file is internally inconsistent by design).
	if !bytes.Contains(out, []byte("Edited One")) {
		t.Error("edited chapter title not written to the chpl")
	}
}

func TestMP4QTChapterReEditAfterTagEdit(t *testing.T) {
	// Tag-edit a QuickTime-chapter file, then (without reparse) chapter-edit the
	// result: the udta bytes must have been carried forward so the chpl splices in
	// correctly.
	data := mp4QTFile([]int{0, 5000}, []string{"A", "B"})
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Tagged").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var w writerTo
	doc, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := doc.Edit().SetChapters(wl.Chapter{Start: 0, Title: "Added After Tag"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, w.b, plan2)
	// The chpl must be written into the udta carried forward from the tag edit, and
	// the earlier tag edit must survive. (A fresh read still prefers the stale
	// QuickTime track, by design — that is what WarnChaptersStale flags.)
	if !bytes.Contains(out2, []byte("Added After Tag")) {
		t.Error("chpl not written when chapter-editing a tag-edited result (udta not carried forward?)")
	}
	if mustParseBytes(t, out2).Fields().Title != "Tagged" {
		t.Error("the tag edit was lost after a follow-up chapter edit")
	}
}
