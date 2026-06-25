package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// mp4QTFileSharedMdat builds a QuickTime-chapter MP4 whose chapter text track stores its
// single chunk at the start of the trailing mdat, which also holds the audio (chapter
// samples first, then audio). This layout must not be mistaken for a standalone chapter
// mdat during a chapter rewrite.
func mp4QTFileSharedMdat(startsMS []int, titles []string) []byte {
	audioFiller := bytes.Repeat([]byte{0xA7}, 120)
	samples := mp4SamplesBlob(titles)
	build := func(audioStco, textStco uint32) []byte {
		kids := slices.Concat(mp4AudioTrakChap(2, audioStco), mp4TextTrak(2, 1000, startsMS, titles, textStco))
		moov := mp4Atom("moov", kids)
		mdat := mp4Atom("mdat", slices.Concat(samples, audioFiller)) // chapter samples first
		return slices.Concat(mp4Ftyp(), moov, mdat)
	}
	tmp := build(0, 0)
	mdatIdx := bytes.Index(tmp, []byte("mdat"))
	textOff := mdatIdx + 4             // chapter chunk == mdat payload start
	audioOff := textOff + len(samples) // audio follows the chapter samples in the same mdat
	return build(uint32(audioOff), uint32(textOff))
}

// TestMP4ChapterEditKeepsSharedAudioMdat verifies that when a foreign chapter track's single chunk
// sits at the start of the trailing mdat that also holds the audio, a chapter rewrite must
// not reclaim that mdat, since doing so would drop the audio. The audio essence stays
// byte-stable across the edit.
func TestMP4ChapterEditKeepsSharedAudioMdat(t *testing.T) {
	data := mp4QTFileSharedMdat([]int{0, 5000}, []string{"A", "B"})
	before := essenceOf(t, data)

	plan, err := mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "X"},
		wl.Chapter{Start: 3 * time.Second, Title: "Y"},
	).Prepare()
	if err != nil {
		t.Fatalf("chapter edit: %v", err)
	}
	out := applyToBytes(t, data, plan)

	if after := essenceOf(t, out); !after.Equal(before) {
		t.Errorf("chapter edit dropped the shared audio mdat: essence changed (audio lost)")
	}
	// The new chapters still round-trip, proving the edit succeeded (not a silent skip).
	if chs := mustParseBytes(t, out).Chapters(); len(chs) != 2 || chs[0].Title != "X" {
		t.Errorf("chapters after edit = %+v, want [X Y]", chs)
	}
}

// mp4QTFileForeignTrackMdat builds a QuickTime-chapter MP4 whose chapter text track stores
// its single chunk at the start of the trailing mdat, which also holds a second audio
// track's samples (chapter samples first, then that track). The first audio track - the one
// d.audioTrak points at - lives in an earlier, separate mdat. This is the multi-track shape
// an audio-only reclaim gate misreads: the first audio track is absent from the trailing
// mdat, so a gate consulting only it would wrongly delete that mdat and drop the second
// track's media. The reclaim check must consult every non-chapter track instead.
func mp4QTFileForeignTrackMdat(startsMS []int, titles []string) []byte {
	audio1 := bytes.Repeat([]byte{0xA7}, 120) // first audio track -> its own (earlier) mdat
	foreign := bytes.Repeat([]byte{0xBB}, 80) // a second audio track -> the trailing mdat
	samples := mp4SamplesBlob(titles)
	build := func(audio1Stco, foreignStco, textStco uint32) []byte {
		kids := slices.Concat(
			mp4AudioTrakChap(3, audio1Stco),                  // track 1: audio, tref chap -> track 3
			mp4SounTrak(2, foreignStco),                      // track 2: a second, independent audio track
			mp4TextTrak(3, 1000, startsMS, titles, textStco), // track 3: the chapter text track
		)
		moov := mp4Atom("moov", kids)
		mdat1 := mp4Atom("mdat", audio1)
		mdat2 := mp4Atom("mdat", slices.Concat(samples, foreign)) // chapter samples first, then track 2
		return slices.Concat(mp4Ftyp(), moov, mdat1, mdat2)
	}
	tmp := build(0, 0, 0)
	audio1Off := bytes.Index(tmp, []byte("mdat")) + 4
	mdat2Payload := bytes.Index(tmp[audio1Off:], []byte("mdat")) + audio1Off + 4
	foreignOff := mdat2Payload + len(samples) // track 2 follows the chapter samples in the trailing mdat
	return build(uint32(audio1Off), uint32(foreignOff), uint32(mdat2Payload))
}

// TestMP4ChapterEditKeepsForeignTrackMdat verifies that when the chapter track's single chunk sits
// at the front of the trailing mdat that also holds a second audio track - while the first
// audio track lives in an earlier mdat - a chapter rewrite must not reclaim that
// trailing mdat. Doing so would drop the second track's samples, which a gate consulting
// only the first audio track would have missed.
func TestMP4ChapterEditKeepsForeignTrackMdat(t *testing.T) {
	data := mp4QTFileForeignTrackMdat([]int{0, 5000}, []string{"A", "B"})
	foreign := bytes.Repeat([]byte{0xBB}, 80)
	if !bytes.Contains(data, foreign) {
		t.Fatal("setup: foreign track samples missing from fixture")
	}

	plan, err := mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "X"},
		wl.Chapter{Start: 3 * time.Second, Title: "Y"},
	).Prepare()
	if err != nil {
		t.Fatalf("chapter edit: %v", err)
	}
	out := applyToBytes(t, data, plan)

	if !bytes.Contains(out, foreign) {
		t.Error("chapter edit reclaimed an mdat shared with a second audio track: that track's samples were lost")
	}
	// The new chapters still round-trip, proving the edit succeeded (not a silent skip).
	if chs := mustParseBytes(t, out).Chapters(); len(chs) != 2 || chs[0].Title != "X" {
		t.Errorf("chapters after edit = %+v, want [X Y]", chs)
	}
}
