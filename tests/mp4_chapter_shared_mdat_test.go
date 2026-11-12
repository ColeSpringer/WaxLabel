package waxlabel_test

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
)

// mp4QTFileSharedMdat builds a QuickTime-chapter MP4 whose chapter text track stores its single
// chunk at the start of the trailing mdat, which also holds the audio (chapter samples first, then
// audio). This layout must not be mistaken for a standalone chapter mdat during a chapter rewrite.
func mp4QTFileSharedMdat(startsMS []int, titles []string) []byte {
	audioFiller := bytes.Repeat([]byte{0xA7}, 120)
	samples := testfile.MP4SamplesBlob(titles)
	build := func(audioStco, textStco uint32) []byte {
		kids := slices.Concat(testfile.MP4AudioTrakChap(2, audioStco), testfile.MP4TextTrak(2, 1000, startsMS, titles, textStco))
		moov := testfile.MP4Atom("moov", kids)
		mdat := testfile.MP4Atom("mdat", slices.Concat(samples, audioFiller)) // chapter samples first
		return slices.Concat(testfile.MP4Ftyp(), moov, mdat)
	}
	tmp := build(0, 0)
	mdatIdx := bytes.Index(tmp, []byte("mdat"))
	textOff := mdatIdx + 4             // chapter chunk == mdat payload start
	audioOff := textOff + len(samples) // audio follows the chapter samples in the same mdat
	return build(uint32(audioOff), uint32(textOff))
}

// When a foreign chapter track's single chunk sits at the start of the trailing mdat that also
// holds the audio, a chapter rewrite must not reclaim that mdat, which would drop the audio.
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

// mp4QTFileForeignTrackMdat builds a QuickTime-chapter MP4 whose chapter text track stores its
// single chunk at the start of the trailing mdat, which also holds a second audio track's samples
// (chapter samples first, then that track).
func mp4QTFileForeignTrackMdat(startsMS []int, titles []string) []byte {
	audio1 := bytes.Repeat([]byte{0xA7}, 120) // first audio track -> its own (earlier) mdat
	foreign := bytes.Repeat([]byte{0xBB}, 80) // a second audio track -> the trailing mdat
	samples := testfile.MP4SamplesBlob(titles)
	build := func(audio1Stco, foreignStco, textStco uint32) []byte {
		kids := slices.Concat(
			testfile.MP4AudioTrakChap(3, audio1Stco),                  // track 1: audio, tref chap -> track 3
			mp4AudioTrakTkhd(2, foreignStco),                          // track 2: a second, independent audio track
			testfile.MP4TextTrak(3, 1000, startsMS, titles, textStco), // track 3: the chapter text track
		)
		moov := testfile.MP4Atom("moov", kids)
		mdat1 := testfile.MP4Atom("mdat", audio1)
		mdat2 := testfile.MP4Atom("mdat", slices.Concat(samples, foreign)) // chapter samples first, then track 2
		return slices.Concat(testfile.MP4Ftyp(), moov, mdat1, mdat2)
	}
	tmp := build(0, 0, 0)
	audio1Off := bytes.Index(tmp, []byte("mdat")) + 4
	mdat2Payload := bytes.Index(tmp[audio1Off:], []byte("mdat")) + audio1Off + 4
	foreignOff := mdat2Payload + len(samples) // track 2 follows the chapter samples in the trailing mdat
	return build(uint32(audio1Off), uint32(foreignOff), uint32(mdat2Payload))
}

// When the chapter track's single chunk sits at the front of the trailing mdat that also holds
// a second audio track (the first audio track lives in an earlier mdat), a chapter rewrite must
// not reclaim that trailing mdat.
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

// A trailing mdat that opens with the chapter samples and holds more is not the chapter track's
// to remove, whether or not a table says what the rest is.
func TestMP4ChapterEditKeepsMdatHoldingMoreThanChapters(t *testing.T) {
	// The audio table points at no mdat.
	astray := mp4QTFileSharedMdat([]int{0, 5000}, []string{"A", "B"})
	binary.BigEndian.PutUint32(astray[bytes.Index(astray, []byte("stco"))+12:], 4)
	// The second track's table points at the first track's audio, so none names the bytes behind
	// the chapter samples.
	unnamed := mp4QTFileForeignTrackMdat([]int{0, 5000}, []string{"A", "B"})
	first := bytes.Index(unnamed, []byte("stco"))
	second := first + 4 + bytes.Index(unnamed[first+4:], []byte("stco"))
	copy(unnamed[second+12:second+16], unnamed[first+12:first+16])
	// Two chunks of one 4-byte sample at one offset: their sizes add up to an 8-byte mdat of
	// which they cover half.
	overlapping := func(audioAt, textAt int) []byte {
		table := func(name string, body ...[]byte) []byte {
			return testfile.MP4Atom(name, slices.Concat(append([][]byte{{0, 0, 0, 0}}, body...)...))
		}
		stbl := testfile.MP4Atom("stbl", slices.Concat(
			table("stts", testfile.BE32(1), testfile.BE32(2), testfile.BE32(1000)),
			table("stsc", testfile.BE32(1), testfile.BE32(1), testfile.BE32(1), testfile.BE32(1)),
			table("stsz", testfile.BE32(4), testfile.BE32(2)),
			table("stco", testfile.BE32(2), testfile.BE32(textAt), testfile.BE32(textAt))))
		mdia := testfile.MP4Atom("mdia", slices.Concat(
			testfile.MP4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("text"), make([]byte, 12))),
			table("mdhd", make([]byte, 8), testfile.BE32(1000), testfile.BE32(0)), testfile.MP4Atom("minf", stbl)))
		text := testfile.MP4Atom("trak", slices.Concat(table("tkhd", make([]byte, 8), testfile.BE32(2), make([]byte, 4)), mdia))
		return slices.Concat(testfile.MP4Ftyp(), testfile.MP4Atom("moov", slices.Concat(testfile.MP4AudioTrakChap(2, uint32(audioAt)), text)),
			testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 120)), testfile.MP4Atom("mdat", []byte("\x00\x02AB\xEE\xEE\xEE\xEE")))
	}
	audioAt := bytes.Index(overlapping(0, 0), []byte("mdat")) + 4
	for name, c := range map[string]struct{ data, rest []byte }{
		"audio table astray":          {astray, bytes.Repeat([]byte{0xA7}, 120)},
		"bytes no table names":        {unnamed, bytes.Repeat([]byte{0xBB}, 80)},
		"overlapping chapter samples": {overlapping(audioAt, audioAt+120+8), bytes.Repeat([]byte{0xEE}, 4)},
	} {
		t.Run(name, func(t *testing.T) {
			essence := essenceOf(t, c.data)
			out := c.data
			for _, titles := range [][]string{{"X", "Y"}, nil} {
				plan, err := mustParseBytes(t, out).Edit().SetChapters(chaptersFromTitles(titles...)...).Prepare(wl.WithVerifyEssence())
				if err != nil {
					t.Fatalf("chapters %q: %v", titles, err)
				}
				out = applyToBytes(t, out, plan)
				if re := mustParseBytes(t, out); len(re.Chapters()) != len(titles) || !bytes.Contains(out, c.rest) || !essenceOf(t, out).Equal(essence) {
					t.Errorf("chapters %q: %d read back, rest kept %v, digest equal %v",
						titles, len(re.Chapters()), bytes.Contains(out, c.rest), essenceOf(t, out).Equal(essence))
				}
			}
		})
	}
}
