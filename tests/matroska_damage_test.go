package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// overrunSimpleTag is chapters.mka with the size of its first SimpleTag, the album ARTIST, turned
// into a four-byte one that runs past the Tag. The album ENCODER behind it is inside that reach.
func overrunSimpleTag(t *testing.T) []byte {
	t.Helper()
	data := readFixture(t, chaptersMKA)
	if data[575] != 0x67 || data[576] != 0xC8 || data[577] != 0x97 {
		t.Fatalf("chapters.mka: no SimpleTag header at 575: % x", data[575:578])
	}
	data[577] = 0x17
	return data
}

// damagedMatroska is the fuzz seeds of a damaged file: chapters.mka with a SimpleTag, then an
// EditionFlagDefault, whose size runs past its parent; with a byte that would start an ID of
// eight bytes; with a Segment of nearly the largest size; with zero bytes ahead of its Cues;
// and cut short inside its Cluster, its Cues and its Tags.
func damagedMatroska() [][]byte {
	b, err := os.ReadFile(chaptersMKA)
	if err != nil {
		return nil
	}
	tags, edition, longID, huge := slices.Clone(b), slices.Clone(b), slices.Clone(b), slices.Clone(b)
	tags[577], edition[457], longID[457] = 0x17, 0x01, 0x80
	copy(huge[44:52], []byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F})
	cues := bytes.LastIndex(b, testfile.IDBytes(testfile.IDCues))
	holed := slices.Concat(b[:cues], make([]byte, 16), b[cues:])
	binary.BigEndian.PutUint64(holed[44:], 1<<56|uint64(len(holed)-52))
	return [][]byte{tags, edition, longID, huge, holed, b[:len(b)-1024], b[:len(b)-4], b[:700]}
}

// refreshCRC recomputes the CRC-32 that leads the master element at start.
func refreshCRC(t *testing.T, b []byte, start int) {
	t.Helper()
	_, idn, _ := readVint(b, start, true)
	size, szn, ok := readVint(b, start+idn, false)
	ds := start + idn + szn
	if !ok || b[ds] != idCRC32 || b[ds+1] != 0x84 {
		t.Fatalf("no leading CRC-32 in the element at %d", start)
	}
	binary.LittleEndian.PutUint32(b[ds+2:], crc32.ChecksumIEEE(b[ds+6:ds+int(size)]))
}

// tagsLast is chapters.mka with its Tags element moved behind the Cues, the SeekHead and Cues
// positions following, and where the last TagString starts.
func tagsLast(t *testing.T) (data []byte, lastString int) {
	t.Helper()
	return tagsMoved(t, true)
}

// tagsMoved is chapters.mka with its Tags element moved behind the Cluster, and behind the Cues
// too when last is set.
func tagsMoved(t *testing.T, last bool) (data []byte, lastString int) {
	t.Helper()
	src := readFixture(t, chaptersMKA)
	_, segData, segEnd, _ := elemRange(src, 0, len(src), testfile.IDSegment, nil)
	tags, _, tagsEnd, ok := elemRange(src, segData, segEnd, testfile.IDTags, nil)
	cluster, _, _, ok2 := elemRange(src, segData, segEnd, testfile.IDCluster, nil)
	cues, _, _, ok3 := elemRange(src, segData, segEnd, testfile.IDCues, nil)
	seek, _, _, ok4 := elemRange(src, segData, segEnd, 0x114D9B74, nil)
	if !ok || !ok2 || !ok3 || !ok4 || tagsEnd != cluster || cues < cluster {
		t.Fatal("chapters.mka: expected SeekHead, Tags, Cluster, Cues in that order")
	}
	n := tagsEnd - tags
	at := len(src) - n // where the Tags start
	if data = slices.Concat(src[:tags], src[tagsEnd:], src[tags:tagsEnd]); !last {
		at = cues - n
		data = slices.Concat(src[:tags], src[tagsEnd:cues], src[tags:tagsEnd], src[cues:])
	}
	// Every position the file stores is two bytes wide: a SeekPosition in the SeekHead for Tags
	// and for Cues, and the Cues' CueClusterPosition.
	put := func(in int, id []byte, old, pos int) {
		el := append(slices.Clone(id), 0x82, byte((old-segData)>>8), byte(old-segData))
		i := bytes.Index(data[in:], el)
		if i < 0 {
			t.Fatalf("no position %d behind offset %d", old-segData, in)
		}
		binary.BigEndian.PutUint16(data[in+i+len(id)+1:], uint16(pos-segData))
	}
	newCues := len(data) - (len(src) - cues)
	if last {
		newCues = cues - n
		put(seek, []byte{0x53, 0xAC}, cues, newCues)
	}
	put(seek, []byte{0x53, 0xAC}, tags, at)
	put(newCues, []byte{idCueClusterPos}, cluster, cluster-n)
	refreshCRC(t, data, seek)
	refreshCRC(t, data, newCues)

	into := map[uint64]bool{testfile.IDTags: true, testfile.IDTag: true, testfile.IDSimpleTag: true}
	for off := at; ; {
		s, _, e, found := elemRange(data, off, at+n, testfile.IDTagString, into)
		if !found {
			break
		}
		lastString, off = s, e
	}
	return data, lastString
}

func tagValues(doc *wl.Document, key tag.Key) []string {
	vs, _ := doc.Tags().Get(key)
	return vs
}

// setComposerAndTrack plans the edit of a damaged file, and returns the plan and what it writes.
func setComposerAndTrack(t *testing.T, src []byte) (*wl.Plan, []byte) {
	t.Helper()
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Composer, "Bach").Set(tag.TrackNumber, "9").Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return plan, applyToBytes(t, src, plan)
}

// ffprobeReadsComposerAndTrack fails unless ffprobe reads both keys from data without an error.
func ffprobeReadsComposerAndTrack(t *testing.T, data []byte) {
	t.Helper()
	requireTool(t, "ffprobe")
	path := writeTempFile(t, "out.mka", data)
	tags := ffprobeFormatTags(t, path)
	if got := lookupCI(tags, "COMPOSER"); got != "Bach" {
		t.Errorf("ffprobe COMPOSER = %q, want Bach (all: %v)", got, tags)
	}
	if got := lookupCI(tags, "track"); got != "9" {
		t.Errorf("ffprobe track = %q, want 9 (all: %v)", got, tags)
	}
	if log, _ := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error", path).CombinedOutput(); len(log) > 0 {
		t.Errorf("ffprobe reports errors in the written file:\n%s", log)
	}
}

// A SimpleTag that runs past its Tag is reported and read as nothing, and what it reaches over is
// not read through it. An edit writes a Tag without it, so the new tags read back.
func TestMatroskaOverrunningSimpleTagIsNotReused(t *testing.T) {
	src := overrunSimpleTag(t)
	doc := mustParseBytes(t, src)
	if w, ok := docWarning(doc, wl.WarnMalformedTagEntry); !ok || !strings.Contains(w.Message, "54 byte(s)") {
		t.Errorf("read warning = %q (found %v), want malformed-tag-entry naming the 54 bytes", w.Message, ok)
	}
	if doc.Tags().Has(tag.Artist) {
		t.Errorf("ARTIST = %v, want none: its SimpleTag is not readable", tagValues(doc, tag.Artist))
	}
	trackEncoder := []string{"Lavc61.19.101 flac"}
	if got := tagValues(doc, tag.Encoder); !slices.Equal(got, trackEncoder) {
		t.Errorf("ENCODER = %q, want %q", got, trackEncoder)
	}

	plan, out := setComposerAndTrack(t, src)
	if w, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); !ok || !strings.Contains(w.Message, "54") {
		t.Errorf("plan warning = %q (found %v), want malformed-tag-entry-dropped naming the 54 bytes", w.Message, ok)
	}
	got := mustParseBytes(t, out)
	if v := tagValues(got, tag.Composer); !slices.Equal(v, []string{"Bach"}) {
		t.Errorf("COMPOSER = %q, want [Bach]", v)
	}
	if v := tagValues(got, tag.TrackNumber); !slices.Equal(v, []string{"9"}) {
		t.Errorf("TRACKNUMBER = %q, want [9]", v)
	}
	if v := tagValues(got, tag.Encoder); !slices.Equal(v, trackEncoder) {
		t.Errorf("ENCODER = %q, want %q", v, trackEncoder)
	}
	if w, ok := docWarning(got, wl.WarnMalformedTagEntry); ok {
		t.Errorf("the written file still warns: %s", w.Message)
	}
	essenceUnchanged(t, src, out)
	checkCRCs(t, out, 0, len(out), 0)
	ffprobeReadsComposerAndTrack(t, out)
}

// A file that ends inside its last SimpleTag reads every tag ahead of the cut, whole, and an edit
// writes a file that is whole too.
func TestMatroskaTagsCutByTheEndOfTheFile(t *testing.T) {
	whole, lastString := tagsLast(t)
	if doc := mustParseBytes(t, whole); !slices.Equal(warningCodes(doc), warningCodes(mustParseFile(t, chaptersMKA))) ||
		!slices.Equal(tagValues(doc, tag.Artist), []string{"Test Artist"}) {
		t.Fatalf("the uncut file reads %v with warnings %v", doc.Tags(), doc.Warnings())
	}
	requireTool(t, "ffprobe")
	if got := lookupCI(ffprobeFormatTags(t, writeTempFile(t, "whole.mka", whole)), "ARTIST"); got != "Test Artist" {
		t.Fatalf("ffprobe reads ARTIST %q from the uncut file, want Test Artist", got)
	}
	encoders := []string{"Lavf61.7.100", "Lavc61.19.101 flac"}
	for _, into := range []int{3, 11} {
		src := whole[:lastString+into]
		doc := mustParseBytes(t, src)
		for _, code := range []wl.WarningCode{wl.WarnMalformedTagEntry, wl.WarnTruncatedAudio} {
			if _, ok := docWarning(doc, code); !ok {
				t.Errorf("cut %d bytes into the TagString: no %s warning in %v", into, code, doc.Warnings())
			}
		}
		if got := tagValues(doc, tag.Encoder); !slices.Equal(got, encoders) {
			t.Errorf("cut %d: ENCODER = %q, want %q", into, got, encoders)
		}
		for _, e := range doc.Native().Describe() {
			if strings.Contains(e.Kind, "DURATION") {
				t.Errorf("cut %d: the cut DURATION tag is listed: %+v", into, e)
			}
		}

		plan, out := setComposerAndTrack(t, src)
		if _, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); !ok {
			t.Errorf("cut %d: no malformed-tag-entry-dropped warning in %v", into, plan.Report().Warnings)
		}
		got := mustParseBytes(t, out)
		if v := tagValues(got, tag.Composer); !slices.Equal(v, []string{"Bach"}) {
			t.Errorf("cut %d: COMPOSER = %q, want [Bach]", into, v)
		}
		if v := tagValues(got, tag.Encoder); !slices.Equal(v, encoders) {
			t.Errorf("cut %d: ENCODER = %q, want %q", into, v, encoders)
		}
		for _, code := range []wl.WarningCode{wl.WarnMalformedTagEntry, wl.WarnTruncatedAudio} {
			if w, ok := docWarning(got, code); ok {
				t.Errorf("cut %d: the written file still warns: %s", into, w.Message)
			}
		}
		if got, want := segmentSize(t, out), len(out)-52; got != want {
			t.Errorf("cut %d: the Segment declares %d bytes, want the %d the written file holds", into, got, want)
		}
		essenceUnchanged(t, src, out)
		checkCRCs(t, out, 0, len(out), 0)
		ffprobeReadsComposerAndTrack(t, out)

		// With every tag cleared the Tags element goes, and the bytes it lacked with it.
		cleared, err := mustParseBytes(t, src).Edit().Clear(tag.Artist).Clear(tag.Encoder).Prepare()
		if err != nil {
			t.Fatalf("cut %d: Prepare: %v", into, err)
		}
		out = applyToBytes(t, src, cleared)
		if countSegChildren(t, out, testfile.IDTags) != 0 || segmentSize(t, out) != len(out)-52 {
			t.Errorf("cut %d, tags cleared: %d Tags elements, the Segment declaring %d bytes of %d", into, countSegChildren(t, out, testfile.IDTags), segmentSize(t, out), len(out)-52)
		}
	}
}

// A file that ends inside its Cues still takes an edit that moves the Cluster: a position the
// file holds whole follows the Cluster, and one it holds part of is left as it is.
func TestMatroskaCuesCutByTheEndOfTheFile(t *testing.T) {
	whole := readFixture(t, chaptersMKA)
	_, segData, _, _ := elemRange(whole, 0, len(whole), testfile.IDSegment, nil)
	into := map[uint64]bool{testfile.IDCues: true, testfile.IDCuePoint: true, testfile.IDCueTrackPos: true}
	for cut := 1; cut <= 27; cut++ {
		src := whole[:len(whole)-cut]
		plan, err := mustParseBytes(t, src).Edit().Set(tag.Artist, strings.Repeat("a long artist ", 12)).Prepare()
		if err != nil {
			t.Errorf("cut %d: Prepare: %v", cut, err)
			continue
		}
		out := applyToBytes(t, src, plan)
		if len(out) <= len(src) {
			t.Fatalf("cut %d: the edit moved nothing (%d -> %d bytes)", cut, len(src), len(out))
		}
		if got, want := segmentSize(t, out), segmentSize(t, src)+len(out)-len(src); got != want {
			t.Errorf("cut %d: the Segment declares %d bytes, want %d: the Cues are as short as they were", cut, got, want)
		}
		essenceUnchanged(t, src, out)
		cluster, _, _, _ := elemRange(out, segData, len(out), testfile.IDCluster, nil)
		_, ds, de, ok := elemRange(out, segData, len(out), idCueClusterPos, into)
		if whole := cut <= 3; ok != (cut <= 5) || whole != (ok && de-ds == 2) {
			t.Fatalf("cut %d: CueClusterPosition found %v with %d bytes", cut, ok, de-ds)
		}
		if ok && de-ds == 2 && segData+int(binary.BigEndian.Uint16(out[ds:de])) != cluster {
			t.Errorf("cut %d: CueClusterPosition %d does not point at the Cluster at %d", cut, binary.BigEndian.Uint16(out[ds:de]), cluster-segData)
		}
		if ok && de-ds == 1 && out[ds] != 0x02 {
			t.Errorf("cut %d: the half of a CueClusterPosition the file holds was rewritten to %#x", cut, out[ds])
		}
	}
}

// swallowedEdition is chapters.mka with the size of its EditionFlagDefault turned into an
// eight-byte one that runs past the edition, over its three ChapterAtoms.
func swallowedEdition(t *testing.T) []byte {
	t.Helper()
	data := readFixture(t, chaptersMKA)
	if !bytes.Equal(data[455:459], []byte{0x45, 0xDB, 0x81, 0x01}) {
		t.Fatalf("chapters.mka: no EditionFlagDefault at 455: % x", data[455:459])
	}
	data[457] = 0x01
	return data
}

// An edition whose leading bytes are no whole element is reported, and chapters written to it
// are not written behind them, where no reader would find them.
func TestMatroskaUnreadableEditionDoesNotSwallowChapters(t *testing.T) {
	two := []wl.Chapter{{Start: 0, Title: "One"}, {Start: ms(300), Title: "Two"}}
	junk := testfile.El(idChapters, mkEdition(true, bytes.Repeat([]byte{0xFF}, 102), mkAtom(1, 0, 0, "A"), mkAtom(2, 1e9, 0, "B")))
	for _, tc := range []struct {
		name string
		src  []byte
	}{
		{"a flag that runs past the edition", swallowedEdition(t)},
		{"a child of unknown size", buildMatroskaCh("matroska", "T", junk, nil)},
	} {
		doc := mustParseBytes(t, tc.src)
		if _, ok := docWarning(doc, wl.WarnMalformedTagEntry); !ok || len(doc.Chapters()) != 0 {
			t.Errorf("%s: %d chapters read with warnings %v, want none and malformed-tag-entry", tc.name, len(doc.Chapters()), doc.Warnings())
		}
		if tagged, err := doc.Edit().Set(tag.Artist, "A").Prepare(); err != nil || planHasWarning(tagged, wl.WarnMalformedTagEntryDropped) {
			t.Errorf("%s: a tag edit: %v, warnings %v; want no word of the Chapters", tc.name, err, tagged.Report().Warnings)
		}
		plan, err := doc.Edit().SetChapters(two...).Prepare()
		if err != nil {
			t.Errorf("%s: Prepare: %v", tc.name, err)
			continue
		}
		if _, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); !ok {
			t.Errorf("%s: no malformed-tag-entry-dropped warning in %v", tc.name, plan.Report().Warnings)
		}
		out := applyToBytes(t, tc.src, plan)
		got := mustParseBytes(t, out)
		if chs := got.Chapters(); len(chs) != 2 || chs[0].Title != "One" || chs[1].Title != "Two" {
			t.Errorf("%s: chapters read back as %+v, want One and Two", tc.name, chs)
		}
		if w, ok := docWarning(got, wl.WarnMalformedTagEntry); ok {
			t.Errorf("%s: the written file still warns: %s", tc.name, w.Message)
		}
		essenceUnchanged(t, tc.src, out)
		checkCRCs(t, out, 0, len(out), 0)
	}

	requireTool(t, "ffprobe")
	src := swallowedEdition(t)
	plan, err := mustParseBytes(t, src).Edit().SetChapters(two...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	chs, log := ffprobeChapters(t, writeTempFile(t, "out.mka", applyToBytes(t, src, plan)))
	if len(chs) != 2 || chs[0].Title != "One" || chs[1].Start != ms(300) || log != "" {
		t.Errorf("ffprobe reads chapters %+v with log %q, want One at 0 and Two at 300 ms and no errors", chs, log)
	}
}

// past is a testfile element with 256 added to the size it declares.
func past(el []byte, idLen int) []byte {
	out := slices.Clone(el)
	out[idLen+6]++
	return out
}

// An AttachedFile that is no whole element is read as neither a picture nor an attachment, and a
// picture edit writes the Attachments without it and with every file that is whole.
func TestMatroskaUnreadableAttachmentIsNotReused(t *testing.T) {
	file := func(name, mime string, data []byte) []byte {
		return testfile.El(idAttached, concat(mkStr(idFileName, name), mkStr(idFileMime, mime), data))
	}
	font := file("font.ttf", "font/ttf", testfile.El(idFileData, []byte("font bytes")))
	cover := file("cover.png", "image/png", testfile.El(idFileData, testfile.PNG()))
	for _, tc := range []struct {
		name     string
		attach   []byte
		pictures int
	}{
		{"a FileData past its AttachedFile", concat(font, file("cover.png", "image/png", past(testfile.El(idFileData, testfile.PNG()), 2))), 0},
		{"an AttachedFile past the Attachments", concat(font, past(cover, 2)), 0},
		{"bytes that are no element", concat(cover, font, []byte{0, 0, 0}), 1},
	} {
		src := testfile.Matroska("matroska", "T", testfile.El(idAttachments, tc.attach))
		doc := mustParseBytes(t, src)
		if _, ok := docWarning(doc, wl.WarnMalformedTagEntry); !ok || len(doc.Pictures()) != tc.pictures {
			t.Errorf("%s: %d pictures read with warnings %v, want %d and malformed-tag-entry", tc.name, len(doc.Pictures()), doc.Warnings(), tc.pictures)
		}
		tagged, err := doc.Edit().Set(tag.Artist, "A").Prepare()
		if err != nil || planHasWarning(tagged, wl.WarnMalformedTagEntryDropped) || !bytes.Contains(applyToBytes(t, src, tagged), tc.attach) {
			t.Errorf("%s: a tag edit: %v, warnings %v; want the Attachments kept as they are without a word", tc.name, err, tagged.Report().Warnings)
		}
		plan, err := doc.Edit().ClearPictures().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.JPEG()}).Prepare()
		if err != nil {
			t.Errorf("%s: Prepare: %v", tc.name, err)
			continue
		}
		if _, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); !ok {
			t.Errorf("%s: no malformed-tag-entry-dropped warning in %v", tc.name, plan.Report().Warnings)
		}
		out := applyToBytes(t, src, plan)
		got := mustParseBytes(t, out)
		if pics := got.Pictures(); len(pics) != 1 || pics[0].MIME != "image/jpeg" {
			t.Errorf("%s: %d pictures read back, want the JPEG alone", tc.name, len(pics))
		}
		if !bytes.Contains(out, font) {
			t.Errorf("%s: the font attachment is not in the written file as it was", tc.name)
		}
		if w, ok := docWarning(got, wl.WarnMalformedTagEntry); ok {
			t.Errorf("%s: the written file still warns: %s", tc.name, w.Message)
		}
		essenceUnchanged(t, src, out)
	}
}

// Damage in a Tag an edit does not touch is not written back either: every Tag of a rewritten
// Tags element is whole.
func TestMatroskaRewrittenTagsHoldNoUnreadableTag(t *testing.T) {
	src := readFixture(t, chaptersMKA)
	if !bytes.Equal(src[646:649], []byte{0x67, 0xC8, 0x9F}) {
		t.Fatalf("chapters.mka: no track SimpleTag at 646: % x", src[646:649])
	}
	src[648] = 0x1F
	doc := mustParseBytes(t, src)
	if w, ok := docWarning(doc, wl.WarnMalformedTagEntry); !ok || !strings.Contains(w.Message, "70 byte(s)") {
		t.Errorf("read warning = %q (found %v), want malformed-tag-entry naming the 70 bytes", w.Message, ok)
	}
	if got, want := tagValues(doc, tag.Encoder), []string{"Lavf61.7.100"}; !slices.Equal(got, want) {
		t.Errorf("ENCODER = %q, want %q", got, want)
	}
	plan, out := setComposerAndTrack(t, src)
	if _, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); !ok {
		t.Errorf("no malformed-tag-entry-dropped warning in %v", plan.Report().Warnings)
	}
	if w, ok := docWarning(mustParseBytes(t, out), wl.WarnMalformedTagEntry); ok {
		t.Errorf("the written file still warns: %s", w.Message)
	}
	checkCRCs(t, out, 0, len(out), 0)
	ffprobeReadsComposerAndTrack(t, out)

	// An edit that leaves the Tags element alone leaves the damage with it.
	titled, err := mustParseBytes(t, src).Edit().Set(tag.Title, "Renamed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(titled, wl.WarnMalformedTagEntryDropped); ok {
		t.Errorf("a title edit warns of a drop: %s", w.Message)
	}
	if kept := applyToBytes(t, src, titled); !bytes.Contains(kept, src[557:716]) {
		t.Error("a title edit did not keep the Tags element as it was")
	}
}

// segmentSize is the size the Segment of data declares.
func segmentSize(t *testing.T, data []byte) int {
	t.Helper()
	seg, _, _, ok := elemRange(data, 0, len(data), testfile.IDSegment, nil)
	size, _, ok2 := readVint(data, seg+4, false)
	if !ok || !ok2 {
		t.Fatal("no Segment")
	}
	return int(size)
}

// ffmpegDecodeLog is what ffmpeg logs at error level while decoding data to nowhere.
func ffmpegDecodeLog(t *testing.T, data []byte) string {
	t.Helper()
	requireTool(t, "ffmpeg")
	log, _ := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", writeTempFile(t, "cut.mka", data), "-f", "null", "-").CombinedOutput()
	return strings.TrimSpace(string(log))
}

// A file cut short is reported, and an edit leaves it as short as it was: the Segment goes on
// declaring the bytes the file lacks, so its last Cluster still ends inside it.
func TestMatroskaTruncatedFileKeepsItsDeclaredSegmentSize(t *testing.T) {
	whole := readFixture(t, sampleMKA)
	if w, ok := docWarning(mustParseBytes(t, whole), wl.WarnTruncatedAudio); ok {
		t.Fatalf("the whole file warns: %s", w.Message)
	}
	cues, _, _, _ := elemRange(whole, 52, len(whole), testfile.IDCues, nil)
	for _, tc := range []struct {
		name  string
		short int
	}{
		{"inside the Cluster", 2048},
		{"at the end of the Cluster", len(whole) - cues},
		{"inside the ID of the Cues", len(whole) - cues - 2},
	} {
		src := whole[:len(whole)-tc.short]
		lacks := strconv.Itoa(tc.short) + " more byte(s) than the file holds"
		doc := mustParseBytes(t, src)
		if w, ok := docWarning(doc, wl.WarnTruncatedAudio); !ok || !strings.Contains(w.Message, lacks) {
			t.Errorf("%s: read warning = %q (found %v), want truncated-audio naming the %d bytes", tc.name, w.Message, ok, tc.short)
		}
		if w, ok := docWarning(doc, wl.WarnTrailingBytes); ok {
			t.Errorf("%s: the head of the element the file ends in is reported apart: %s", tc.name, w.Message)
		}
		for _, artist := range []string{"B", strings.Repeat("a long artist ", 30)} {
			plan, err := mustParseBytes(t, src).Edit().Set(tag.Artist, artist).Prepare()
			if err != nil {
				t.Fatalf("%s: Prepare: %v", tc.name, err)
			}
			out := applyToBytes(t, src, plan)
			moved := len(out) - len(src)
			if (moved > 0) != (len(artist) > 1) || moved == 0 {
				t.Fatalf("%s: a %d-byte artist moved the tail by %d", tc.name, len(artist), moved)
			}
			if got, want := segmentSize(t, out), segmentSize(t, src)+moved; got != want {
				t.Errorf("%s: the Segment declares %d bytes, want the %d it declared and the %d the edit added", tc.name, got, want-moved, moved)
			}
			if !bytes.HasSuffix(out, src[len(src)-64:]) {
				t.Errorf("%s: the written file does not end as the cut file did", tc.name)
			}
			if w, ok := docWarning(mustParseBytes(t, out), wl.WarnTruncatedAudio); !ok || !strings.Contains(w.Message, lacks) {
				t.Errorf("%s: the written file warns %q (found %v), want the same %d bytes short", tc.name, w.Message, ok, tc.short)
			}
			essenceUnchanged(t, src, out)
			if log := ffmpegDecodeLog(t, out); strings.Contains(log, "\n") || !strings.Contains(log, "File ended prematurely") {
				t.Errorf("%s: ffmpeg logs %q for the written file, want only that the file ended prematurely", tc.name, log)
			}
		}
	}
}

// Bytes at the end of the Segment that no element header parses from are reported and carried
// through an edit that moves the tail.
func TestMatroskaBytesBehindTheLastElementAreKept(t *testing.T) {
	tags := testfile.El(testfile.IDTags, testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "A")))
	junk := []byte{0, 0, 0}
	src := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment,
		testfile.El(testfile.IDInfo, mkStr(testfile.IDSegTitle, "T")), testfile.AudioCluster(0), tags, junk))
	doc := mustParseBytes(t, src)
	const want = "3 byte(s) inside the Segment are no element; preserved verbatim"
	if w, ok := docWarning(doc, wl.WarnTrailingBytes); !ok || w.Message != want {
		t.Errorf("read warning = %q (found %v), want %q", w.Message, ok, want)
	}
	plan, err := doc.Edit().Set(tag.Artist, "A longer artist").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if body := len(out) - len(testfile.EBMLHeader("matroska")) - 12; len(out) == len(src) || !bytes.HasSuffix(out, junk) || segmentSize(t, out) != body {
		t.Errorf("%d bytes written for %d, ending % x, the Segment declaring %d of its %d", len(out), len(src), out[len(out)-3:], segmentSize(t, out), body)
	}
	got := mustParseBytes(t, out)
	if w, ok := docWarning(got, wl.WarnTrailingBytes); !ok || w.Message != want {
		t.Errorf("the written file warns %q (found %v), want %q", w.Message, ok, want)
	}
	if v := tagValues(got, tag.Artist); !slices.Equal(v, []string{"A longer artist"}) {
		t.Errorf("ARTIST = %q", v)
	}
}

// Text ends at its first NUL. Bytes behind one are reported with the key that reads the text,
// NUL padding is not, and an edit of something else keeps the bytes.
func TestMatroskaTextStopsAtTheFirstNUL(t *testing.T) {
	artist := testfile.SimpleTag("ARTIST", "Alpha\x00Beta")
	tags := testfile.El(testfile.IDTags, testfile.TagGroup(0, artist, testfile.SimpleTag("ARTIST", "Gamma\x00Delta"), testfile.SimpleTag("GEN\x00RE", "Rock"),
		testfile.SimpleTag("COMMENT", "padded\x00\x00"), mkSimpleNested("PART", "p", testfile.SimpleTag("SUB", "s\x00t"))))
	chapters := testfile.El(idChapters, mkEdition(true, nil, mkAtom(1, 0, 0, "One\x00x"), mkAtom(2, 1e9, 0, "Two\x00")))
	src := buildMatroskaCh("matroska", "Ti\x00tle", chapters, tags)
	doc := mustParseBytes(t, src)
	for key, want := range map[tag.Key][]string{tag.Title: {"Ti"}, tag.Artist: {"Alpha", "Gamma"}, "GEN": {"Rock"}, tag.Comment: {"padded"}} {
		if got := tagValues(doc, key); !slices.Equal(got, want) {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if chs := doc.Chapters(); len(chs) != 2 || chs[0].Title != "One" || chs[1].Title != "Two" {
		t.Errorf("chapters = %+v, want One and Two", chs)
	}
	var keys []tag.Key
	var messages []string
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnMalformedTagEntry {
			keys, messages = append(keys, w.Keys...), append(messages, w.Message)
		}
	}
	slices.Sort(keys)
	if want := []tag.Key{tag.Artist, "GEN", tag.Title}; !slices.Equal(keys, want) || len(messages) != 3 {
		t.Errorf("malformed-tag-entry warnings %q with keys %v, want three with keys %v", messages, keys, want)
	}
	if text := strings.Join(messages, "\n"); !strings.Contains(text, "4 SimpleTag name(s) or value(s)") || !strings.Contains(text, "1 chapter title(s)") {
		t.Errorf("warnings %q: want the four SimpleTag texts and the one chapter title counted", messages)
	}

	out, got := saveMatroska(t, src, doc.Edit().Set(tag.Album, "X"))
	if !bytes.Contains(out, artist) {
		t.Error("an edit of ALBUM did not keep the ARTIST SimpleTag as it was")
	}
	if v := tagValues(got, tag.Artist); !slices.Equal(v, []string{"Alpha", "Gamma"}) {
		t.Errorf("ARTIST read back as %q", v)
	}

	// Each kind of text is reported by itself.
	for _, tc := range []struct {
		name, want string
		src        []byte
		keys       []tag.Key
	}{
		{"the title", "the Info.Title", testfile.Matroska("matroska", "Ti\x00tle"), []tag.Key{tag.Title}},
		{"an attachment description", "1 attachment name(s), type(s) or description(s)", testfile.Matroska("matroska", "T", testfile.El(idAttachments,
			testfile.El(idAttached, concat(mkStr(idFileName, "cover.png"), mkStr(idFileMime, "image/png"), mkStr(0x467E, "De\x00sc"), testfile.El(idFileData, testfile.PNG()))))), nil},
	} {
		var found []wl.Warning
		for _, w := range mustParseBytes(t, tc.src).Warnings() {
			if w.Code == wl.WarnMalformedTagEntry {
				found = append(found, w)
			}
		}
		if len(found) != 1 || !strings.HasPrefix(found[0].Message, tc.want+":") || !slices.Equal(found[0].Keys, tc.keys) {
			t.Errorf("%s cut at a NUL: warnings %+v, want one for %q with keys %v", tc.name, found, tc.want, tc.keys)
		}
	}
}

// voided is data with the n-byte element at off turned into a Void of the same length.
func voided(t *testing.T, data []byte, off, n int) []byte {
	t.Helper()
	out := slices.Clone(data)
	copy(out[off:off+n], append([]byte{0xEC, 0x80 | byte(n-2)}, make([]byte, n-2)...))
	return out
}

// A FLAC track whose Audio element lacks its bit depth, sampling frequency or channel count takes
// them from the STREAMINFO its CodecPrivate holds, where ffprobe reads them. The essence digest
// goes on naming what the Audio element says.
func TestMatroskaFLACGeometryFromStreamInfo(t *testing.T) {
	whole := readFixture(t, sampleMKA)
	if !bytes.Equal(whole[366:368], []byte{0x9F, 0x81}) || !bytes.Equal(whole[369:371], []byte{0xB5, 0x88}) || !bytes.Equal(whole[379:382], []byte{0x62, 0x64, 0x81}) {
		t.Fatalf("sample.mka: no Channels, SamplingFrequency and BitDepth at 366: % x", whole[366:383])
	}
	ctx := context.Background()
	digest := func(data []byte) wl.AudioDigest {
		d, err := mustParseBytes(t, data).HashAudioEssence(ctx, wl.WithHashSource(wl.BytesSource(data)))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, tc := range []struct {
		name string
		src  []byte
	}{
		{"no BitDepth", voided(t, whole, 379, 4)},
		{"no SamplingFrequency", voided(t, whole, 369, 10)},
		{"no Channels", voided(t, whole, 366, 3)},
		{"an empty Audio element", voided(t, voided(t, voided(t, whole, 379, 4), 369, 10), 366, 3)},
		{"a BitDepth that runs past the Audio element", func() []byte { b := slices.Clone(whole); b[381] = 0x82; return b }()},
	} {
		track := mustParseBytes(t, tc.src).Properties().First()
		if track.BitsPerSample != 16 || track.SampleRate != 44100 || track.Channels != 2 {
			t.Errorf("%s: %d-bit, %d Hz, %d ch; want 16-bit, 44100 Hz, 2 ch", tc.name, track.BitsPerSample, track.SampleRate, track.Channels)
		}
		if digest(tc.src).Equal(digest(whole)) {
			t.Errorf("%s: the essence digest is the one of the file that states its geometry", tc.name)
		}
		// ffprobe states no sample width for FLAC.
		if _, err := exec.LookPath("ffprobe"); err == nil || ffmpegRequired() {
			requireTool(t, "ffprobe")
			if rate, channels, _ := ffprobeAudio(t, writeTempFile(t, "voided.mka", tc.src)); rate != track.SampleRate || channels != track.Channels {
				t.Errorf("%s: ffprobe reads %d Hz, %d ch", tc.name, rate, channels)
			}
		}
	}
}

// A plan that creates chapters and a cover reports one size however often it is prepared: the
// UIDs it draws all take the same room.
func TestMatroskaPlannedSizeDoesNotTurnOnTheUIDsDrawn(t *testing.T) {
	doc := mustParseFile(t, notagsMKA)
	sizes := map[int64]bool{}
	for range 300 {
		plan, err := doc.Edit().SetChapters(wl.Chapter{Start: 0, Title: "A"}, wl.Chapter{Start: ms(300), Title: "B"}, wl.Chapter{Start: ms(600), Title: "C"}).
			AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		sizes[plan.Report().BytesAfter] = true
	}
	if len(sizes) != 1 {
		t.Errorf("300 plans of one edit report %d sizes: %v", len(sizes), sizes)
	}
}

// A second Chapters element is reported and left alone until a chapter edit, which drops it and
// says so.
func TestMatroskaDuplicateChaptersElement(t *testing.T) {
	first := testfile.El(idChapters, mkEdition(true, nil, mkAtom(1, 0, 0, "A"), mkAtom(2, 1e9, 0, "B")))
	second := testfile.El(idChapters, mkEdition(true, nil, mkAtom(1, 0, 0, "A"), mkAtom(3, 2e9, 0, "C")), mkEdition(false, nil, mkAtom(4, 0, 0, "Alt")))
	countChapters := func(data []byte) int { return countSegChildren(t, data, idChapters) }

	src := buildMatroskaCh("matroska", "T", concat(first, second), nil)
	doc := mustParseBytes(t, src)
	if chs := doc.Chapters(); len(chs) != 2 || chs[1].Title != "B" {
		t.Errorf("chapters = %+v, want the first element's A and B", chs)
	}
	if _, ok := docWarning(doc, wl.WarnDuplicateTagBlock); !ok {
		t.Errorf("no duplicate-tag-block warning in %v", doc.Warnings())
	}
	titled, err := doc.Edit().Set(tag.Title, "A longer title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, src, titled); planHasWarning(titled, wl.WarnDuplicateTagBlockDropped) || countChapters(out) != 2 {
		t.Errorf("a title edit: warnings %v, %d Chapters elements; want both kept without a word", titled.Report().Warnings, countChapters(out))
	}

	plan, err := doc.Edit().SetChapters(append(doc.Chapters(), wl.Chapter{Start: 3 * time.Second, Title: "D"})...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnDuplicateTagBlockDropped); !ok || !strings.Contains(w.Message, "1 Chapters element(s) behind the first") {
		t.Errorf("chapter edit warning = %q (found %v), want duplicate-tag-block-dropped for the one element", w.Message, ok)
	}
	out := applyToBytes(t, src, plan)
	got := mustParseBytes(t, out)
	if countChapters(out) != 1 || len(got.Chapters()) != 3 {
		t.Errorf("%d Chapters elements holding %d chapters, want one holding three", countChapters(out), len(got.Chapters()))
	}
	if w, ok := docWarning(got, wl.WarnDuplicateTagBlock); ok {
		t.Errorf("the written file still warns: %s", w.Message)
	}
}

// tailSeekHead is a file whose SeekHead stands behind its Cluster, behind a CRC-32 naming Info,
// Tracks and the Cluster through positions width bytes wide, with a Void an edit of the title can
// shrink into.
func tailSeekHead(width int) []byte {
	info := testfile.El(testfile.IDInfo, mkStr(testfile.IDSegTitle, "T"))
	tracks := testfile.El(testfile.IDTracks, testfile.El(testfile.IDTrackEntry, testfile.Uint(testfile.IDTrackType, 2), mkStr(testfile.IDCodecID, "A_OPUS")))
	void := append([]byte{0xEC, 0x40, 0xC8}, make([]byte, 200)...)
	seek := func(id uint64, pos int) []byte {
		value := binary.BigEndian.AppendUint64(nil, uint64(pos))[8-width:]
		return testfile.El(idSeek, testfile.El(idSeekID, testfile.IDBytes(id)), testfile.El(idSeekPosition, value))
	}
	seekHead := testfile.El(idSeekHead, mkCRC(concat(seek(testfile.IDInfo, 0), seek(testfile.IDTracks, len(info)), seek(testfile.IDCluster, len(info)+len(tracks)+len(void)))))
	return concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, info, tracks, void, testfile.AudioCluster(0), seekHead))
}

// A SeekHead behind the Cluster follows an edit that moves what it names: one the padding takes
// up patches it where it stands and moves no Cluster. Cut by the end of the file it is patched
// as far as the file holds it, and written whole, with the Segment no longer short, once a
// position outgrows its bytes.
func TestMatroskaSeekHeadBehindTheCluster(t *testing.T) {
	lead := len(testfile.EBMLHeader("matroska")) + 12
	retitle := func(src []byte, title string) []byte {
		t.Helper()
		plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, title).Prepare()
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		out := applyToBytes(t, src, plan)
		if got := mustParseBytes(t, out).Fields().Title; got != title {
			t.Errorf("title read back as %q", got)
		}
		return out
	}
	whole := tailSeekHead(2)
	assertSeekEntriesResolve(t, whole)
	out := retitle(whole, "Longer")
	assertSeekEntriesResolve(t, out)
	checkCRCs(t, out, 0, len(out), 0)
	cluster := func(data []byte) int {
		at, _, _, _ := elemRange(data, lead, len(data), testfile.IDCluster, nil)
		return at
	}
	if len(out) != len(whole) || cluster(out) != cluster(whole) {
		t.Errorf("an edit the padding takes up: %d bytes for %d, the Cluster at %d for %d", len(out), len(whole), cluster(out), cluster(whole))
	}
	crcOf := func(data []byte) []byte {
		_, ds, _, _ := elemRange(data, lead, len(data), idSeekHead, nil)
		return data[ds+2 : ds+6]
	}

	// The file ends one byte into the Cluster's two-byte position.
	cut := whole[:len(whole)-1]
	out = retitle(cut, "Longer")
	seekEntriesResolve(t, out, true)
	if cluster(out) != cluster(cut) {
		t.Errorf("an edit the padding takes up, of the cut file: the Cluster at %d for %d", cluster(out), cluster(cut))
	}
	if !bytes.HasSuffix(out, cut[len(cut)-8:]) || segmentSize(t, out) != len(out)-lead+1 {
		t.Errorf("a cut SeekHead patched in place: ends % x, the Segment declaring %d bytes of %d held; want the cut entry as it was and one byte short",
			out[len(out)-8:], segmentSize(t, out), len(out)-lead)
	}
	if !bytes.Equal(crcOf(out), crcOf(cut)) {
		t.Errorf("a cut SeekHead patched in place: CRC-32 % x, want the % x it had, which covers bytes the file lacks", crcOf(out), crcOf(cut))
	}

	// The same cut file under an edit that moves the Cluster.
	out = retitle(cut, strings.Repeat("A long title. ", 40))
	seekEntriesResolve(t, out, true)
	if cluster(out) == cluster(cut) || !bytes.HasSuffix(out, cut[len(cut)-8:]) || segmentSize(t, out) != len(out)-lead+1 || !bytes.Equal(crcOf(out), crcOf(cut)) {
		t.Errorf("a cut SeekHead patched behind a Cluster that moved: ends % x, the Segment declaring %d bytes of %d held, CRC-32 % x",
			out[len(out)-8:], segmentSize(t, out), len(out)-lead, crcOf(out))
	}

	// One-byte positions: the Tracks' outgrows its byte, so the SeekHead is written again.
	narrow := tailSeekHead(1)
	cut = narrow[:len(narrow)-1]
	out = retitle(cut, strings.Repeat("A long title. ", 40))
	assertSeekEntriesResolve(t, out)
	if segmentSize(t, out) != len(out)-lead {
		t.Errorf("a cut SeekHead written again: the Segment declares %d bytes, want the %d it holds", segmentSize(t, out), len(out)-lead)
	}
	if w, ok := docWarning(mustParseBytes(t, out), wl.WarnTruncatedAudio); ok {
		t.Errorf("the written file still warns: %s", w.Message)
	}

	// The same, with a SeekHead that comes out exactly as long as the part of it the file held:
	// three 14-byte entries behind a CRC-32, cut one byte into the third, written again as two
	// entries of which the Tracks' position takes two bytes.
	entry := func(id uint64, pos byte) []byte {
		return concat([]byte{0x4D, 0xBB, 0x8B, 0x53, 0xAB, 0x84}, testfile.IDBytes(id), []byte{0x53, 0xAC, 0x81, pos})
	}
	info := testfile.El(testfile.IDInfo, mkStr(testfile.IDSegTitle, "T"))
	tracks := testfile.El(testfile.IDTracks, testfile.El(testfile.IDTrackEntry, testfile.Uint(testfile.IDTrackType, 2), mkStr(testfile.IDCodecID, "A_OPUS")))
	seekHead := concat(testfile.IDBytes(idSeekHead), []byte{0x80 | 48}, mkCRC(concat(entry(testfile.IDInfo, 0), entry(testfile.IDTracks, byte(len(info))), entry(testfile.IDCluster, 0x7F))))
	file := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, info, tracks, testfile.AudioCluster(0), seekHead))
	cut = file[:len(file)-13]
	out = retitle(cut, strings.Repeat("A long title. ", 40))
	assertSeekEntriesResolve(t, out)
	start, _, end, _ := elemRange(out, lead, len(out), idSeekHead, nil)
	if held := len(seekHead) - 13; end-start != held || end != len(out) || segmentSize(t, out) != len(out)-lead {
		t.Errorf("a cut SeekHead written again at its held length: %d bytes for %d held, the Segment declaring %d of %d", end-start, held, segmentSize(t, out), len(out)-lead)
	}
}

// Cues the end of the file cuts are patched where they stand. An edit that moves the Cluster past
// what a whole position's bytes hold would have to write them again, which is refused: the cut
// CuePoint cannot be rendered.
func TestMatroskaCutCuesAreNotWrittenAgain(t *testing.T) {
	whole := readFixture(t, chaptersMKA)
	cover := wl.Picture{Type: wl.PicFrontCover, Data: append(testfile.PNG(), make([]byte, 70000)...)}
	for cut := 1; cut <= 27; cut++ {
		src := whole[:len(whole)-cut]
		plan, err := mustParseBytes(t, src).Edit().AddPicture(cover).Prepare()
		if cut <= 3 {
			if !errors.Is(err, waxerr.ErrUnsupportedTag) {
				t.Errorf("cut %d: Prepare = %v, want the refusal to write the Cues again", cut, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("cut %d: Prepare: %v", cut, err)
			continue
		}
		out := applyToBytes(t, src, plan)
		if !bytes.HasSuffix(out, src[len(src)-min(8, 28-cut):]) || len(mustParseBytes(t, out).Pictures()) != 1 {
			t.Errorf("cut %d: the written file does not end as the cut file did, or holds no cover", cut)
		}
	}
}

// An Info of unknown size, which ends where the next element of the Segment starts, takes a
// title.
func TestMatroskaTitleEditOfAnUnknownSizeInfo(t *testing.T) {
	info := concat(testfile.IDBytes(testfile.IDInfo), []byte{0xFF}, mkStr(testfile.IDSegTitle, "Old"))
	src := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, info, testfile.AudioCluster(0)))
	doc := mustParseBytes(t, src)
	if got := doc.Fields().Title; got != "Old" {
		t.Fatalf("title = %q, want Old", got)
	}
	_, got := saveMatroska(t, src, doc.Edit().Set(tag.Title, "New"))
	if title := got.Fields().Title; title != "New" {
		t.Errorf("title read back as %q, want New", title)
	}
}

// A file cut inside Tags that stand ahead of its Cues lost the Cues too: an edit writes the Tags
// whole, and the Segment goes on declaring the bytes of the Cues.
func TestMatroskaTagsCutAheadOfTheCues(t *testing.T) {
	whole, lastString := tagsMoved(t, false)
	if doc := mustParseBytes(t, whole); !slices.Equal(warningCodes(doc), warningCodes(mustParseFile(t, chaptersMKA))) {
		t.Fatalf("the uncut file warns %v", doc.Warnings())
	}
	cues, _, _, _ := elemRange(whole, 52, len(whole), testfile.IDCues, nil)
	lost := strconv.Itoa(len(whole)-cues) + " more byte(s) than the file holds"
	src := whole[:lastString+3]
	plan, out := setComposerAndTrack(t, src)
	if _, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); !ok {
		t.Errorf("no malformed-tag-entry-dropped warning in %v", plan.Report().Warnings)
	}
	got := mustParseBytes(t, out)
	if w, ok := docWarning(got, wl.WarnTruncatedAudio); !ok || !strings.Contains(w.Message, lost) || segmentSize(t, out) != len(out)-52+len(whole)-cues {
		t.Errorf("the written file warns %q (found %v) and declares %d bytes for %d held; want it short by the Cues alone", w.Message, ok, segmentSize(t, out), len(out)-52)
	}
	if w, ok := docWarning(got, wl.WarnMalformedTagEntry); ok {
		t.Errorf("the written file still warns: %s", w.Message)
	}
}

// A Title that runs past the Info is no title, and it is reported with what it reaches over. A
// title edit writes the Info without those bytes and says so; another edit leaves them.
func TestMatroskaTitleThatOverrunsTheInfo(t *testing.T) {
	src := readFixture(t, chaptersMKA)
	if !bytes.Equal(src[231:234], []byte{0x7B, 0xA9, 0x8E}) {
		t.Fatalf("chapters.mka: no Title at 231: % x", src[231:234])
	}
	src[233] = 0x1E
	doc := mustParseBytes(t, src)
	if doc.Tags().Has(tag.Title) {
		t.Errorf("TITLE = %q, want none", tagValues(doc, tag.Title))
	}
	if w, ok := docWarning(doc, wl.WarnMalformedTagEntry); !ok || !strings.Contains(w.Message, "77 byte(s) of the Info element") {
		t.Errorf("read warning = %q (found %v), want malformed-tag-entry naming the 77 bytes of the Info", w.Message, ok)
	}
	tagged, err := doc.Edit().Set(tag.Artist, "A").Prepare()
	if err != nil || planHasWarning(tagged, wl.WarnMalformedTagEntryDropped) || !bytes.Contains(applyToBytes(t, src, tagged), src[213:308]) {
		t.Errorf("a tag edit: %v, warnings %v; want the Info kept as it is without a word", err, tagged.Report().Warnings)
	}
	plan, err := doc.Edit().Set(tag.Title, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); !ok || !strings.Contains(w.Message, "77 byte(s) of the Info element") {
		t.Errorf("title edit warning = %q (found %v), want malformed-tag-entry-dropped naming the 77 bytes", w.Message, ok)
	}
	out := applyToBytes(t, src, plan)
	got := mustParseBytes(t, out)
	if w, ok := docWarning(got, wl.WarnMalformedTagEntry); ok || got.Fields().Title != "New" {
		t.Errorf("the written file warns %q (found %v), title %q", w.Message, ok, got.Fields().Title)
	}
	checkCRCs(t, out, 0, len(out), 0)
	requireTool(t, "ffprobe")
	path := writeTempFile(t, "out.mka", out)
	if log, _ := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error", path).CombinedOutput(); len(log) > 0 || lookupCI(ffprobeFormatTags(t, path), "title") != "New" {
		t.Errorf("ffprobe reads title %q with log %q", lookupCI(ffprobeFormatTags(t, path), "title"), log)
	}
}

// An Info behind the Cluster that the end of the file cuts takes a title: it is written whole,
// without the child that was cut, and the file is no longer short.
func TestMatroskaInfoCutByTheEndOfTheFile(t *testing.T) {
	info := testfile.El(testfile.IDInfo, mkStr(testfile.IDSegTitle, "T"), mkStr(0x4D80, "the muxing app"))
	whole := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, testfile.AudioCluster(0), info))
	lead := len(testfile.EBMLHeader("matroska")) + 12
	app := len(whole) - len(mkStr(0x4D80, "the muxing app"))
	for _, into := range []int{5, 12} { // inside the size of the MuxingApp, and inside its text
		src := whole[:app+into]
		doc := mustParseBytes(t, src)
		for _, code := range []wl.WarningCode{wl.WarnMalformedTagEntry, wl.WarnTruncatedAudio} {
			if _, ok := docWarning(doc, code); !ok {
				t.Errorf("cut %d: no %s warning in %v", into, code, doc.Warnings())
			}
		}
		plan, err := doc.Edit().Set(tag.Title, "A new title").Prepare()
		if err != nil {
			t.Fatalf("cut %d: Prepare: %v", into, err)
		}
		if _, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); !ok {
			t.Errorf("cut %d: no malformed-tag-entry-dropped warning in %v", into, plan.Report().Warnings)
		}
		out := applyToBytes(t, src, plan)
		got := mustParseBytes(t, out)
		if len(got.Warnings()) != 0 || got.Fields().Title != "A new title" || segmentSize(t, out) != len(out)-lead || bytes.Contains(out, []byte{0x4D, 0x80}) {
			t.Errorf("cut %d: warnings %v, title %q, the Segment declaring %d of %d bytes; want a whole file without the MuxingApp",
				into, got.Warnings(), got.Fields().Title, segmentSize(t, out), len(out)-lead)
		}
	}
}

// A Tag that declares more than its Tags element holds, every child whole, is read, reported, and
// given its size by a rewrite, which has nothing to drop.
func TestMatroskaTagThatOverrunsTheTags(t *testing.T) {
	tags := testfile.El(testfile.IDTags, testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "A")), past(testfile.TagGroup(7, testfile.SimpleTag("ENCODER", "enc")), 2))
	src := testfile.Matroska("matroska", "T", tags)
	doc := mustParseBytes(t, src)
	if w, ok := docWarning(doc, wl.WarnMalformedTagEntry); !ok || !strings.Contains(w.Message, "runs past its parent") {
		t.Errorf("read warning = %q (found %v), want the Tag that runs past its parent", w.Message, ok)
	}
	if a, e := tagValues(doc, tag.Artist), tagValues(doc, tag.Encoder); !slices.Equal(a, []string{"A"}) || !slices.Equal(e, []string{"enc"}) {
		t.Errorf("ARTIST %q, ENCODER %q; want both read", a, e)
	}
	plan, err := doc.Edit().Set(tag.Album, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); ok {
		t.Errorf("the edit warns of a drop: %s", w.Message)
	}
	got := mustParseBytes(t, applyToBytes(t, src, plan))
	if w, ok := docWarning(got, wl.WarnMalformedTagEntry); ok || !slices.Equal(tagValues(got, tag.Encoder), []string{"enc"}) {
		t.Errorf("the written file warns %q (found %v), ENCODER %q", w.Message, ok, tagValues(got, tag.Encoder))
	}
}

// A CodecPrivate that holds a FLAC header says nothing of a track of another codec.
func TestMatroskaFLACHeaderInAnotherCodecsPrivateData(t *testing.T) {
	whole := readFixture(t, sampleMKA)
	_, ds, de, ok := elemRange(whole, 0, len(whole), idCodecPrivate, map[uint64]bool{testfile.IDSegment: true, testfile.IDTracks: true, testfile.IDTrackEntry: true})
	if !ok || string(whole[ds:ds+4]) != "fLaC" {
		t.Fatal("sample.mka: no FLAC CodecPrivate")
	}
	track := mustParseBytes(t, mkAudioTrackFile("A_PCM/INT/LIT", whole[ds:de])).Properties().First()
	if track.SampleRate != 8000 || track.Channels != 1 || track.BitsPerSample != 0 {
		t.Errorf("%d Hz, %d ch, %d-bit; want the defaults of a track that states nothing", track.SampleRate, track.Channels, track.BitsPerSample)
	}
}

// A last child that declares more than the Segment does, itself short of the file, accounts for
// no more than the Segment lacks: rendered whole, it leaves a Segment of exactly its body.
func TestMatroskaLastChildDeclaringPastTheSegment(t *testing.T) {
	tags := past(testfile.El(testfile.IDTags, testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "A"))), 4)
	src := testfile.Matroska("matroska", "T", tags)
	lead := len(testfile.EBMLHeader("matroska")) + 12
	src[lead-1] += 10 // the Segment declares ten bytes the file lacks, its Tags 256
	doc := mustParseBytes(t, src)
	if w, ok := docWarning(doc, wl.WarnTruncatedAudio); !ok || !strings.Contains(w.Message, "10 more byte(s)") {
		t.Fatalf("read warning = %q (found %v), want truncated-audio naming the 10 bytes", w.Message, ok)
	}
	plan, err := doc.Edit().Set(tag.Artist, "A longer artist").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if got := segmentSize(t, out); got != len(out)-lead {
		t.Errorf("the Segment declares %d bytes, want the %d it holds", got, len(out)-lead)
	}
	if got := mustParseBytes(t, out); len(got.Warnings()) != 0 || !slices.Equal(tagValues(got, tag.Artist), []string{"A longer artist"}) {
		t.Errorf("the written file warns %v, ARTIST %q", got.Warnings(), tagValues(got, tag.Artist))
	}
}

// An element ID is four bytes at most. Bytes that would read as a longer one are no element: an
// EditionFlagDefault of no size leaves its value byte, 0x01, where an ID starts.
func TestMatroskaElementIDLongerThanFourBytes(t *testing.T) {
	src := readFixture(t, chaptersMKA)
	if !bytes.Equal(src[455:459], []byte{0x45, 0xDB, 0x81, 0x01}) {
		t.Fatalf("chapters.mka: no EditionFlagDefault at 455: % x", src[455:459])
	}
	src[457] = 0x80
	doc := mustParseBytes(t, src)
	if w, ok := docWarning(doc, wl.WarnMalformedTagEntry); !ok || !strings.Contains(w.Message, "Chapters element") || len(doc.Chapters()) != 0 {
		t.Errorf("%d chapters read with warnings %v; want none, and malformed-tag-entry for the Chapters", len(doc.Chapters()), doc.Warnings())
	}
	plan, err := doc.Edit().SetChapters(wl.Chapter{Start: ms(50), Title: "New"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnMalformedTagEntryDropped); !ok {
		t.Errorf("no malformed-tag-entry-dropped warning in %v", plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)
	requireTool(t, "ffprobe")
	chs, log := ffprobeChapters(t, writeTempFile(t, "out.mka", out))
	if len(chs) != 1 || chs[0].Title != "New" || log != "" {
		t.Errorf("ffprobe reads chapters %+v with log %q, want the one new chapter and no errors", chs, log)
	}

	// The same byte in a Tag: its Targets is no element, so nothing of the Tag is read.
	src = readFixture(t, chaptersMKA)
	if !bytes.Equal(src[632:634], []byte{0x63, 0xC0}) {
		t.Fatalf("chapters.mka: no track Targets at 632: % x", src[632:634])
	}
	src[632] = 0x08
	doc = mustParseBytes(t, src)
	if _, ok := docWarning(doc, wl.WarnMalformedTagEntry); !ok || !slices.Equal(tagValues(doc, tag.Encoder), []string{"Lavf61.7.100"}) {
		t.Errorf("ENCODER %q with warnings %v; want the album's alone, and malformed-tag-entry", tagValues(doc, tag.Encoder), doc.Warnings())
	}
	_, out = setComposerAndTrack(t, src)
	ffprobeReadsComposerAndTrack(t, out)
}

// A Segment that declares nearly the largest size its eight bytes hold keeps a size an edit that
// grows the file can still write: the largest, never none.
func TestMatroskaSegmentSizeAtTheTopOfItsRange(t *testing.T) {
	for _, last := range []byte{0xFE, 0x7F} {
		src := readFixture(t, sampleMKA)
		copy(src[44:52], []byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, last})
		doc := mustParseBytes(t, src)
		if _, ok := docWarning(doc, wl.WarnTruncatedAudio); !ok {
			t.Fatalf("size ...%02X: no truncated-audio warning in %v", last, doc.Warnings())
		}
		plan, err := doc.Edit().Set(tag.Comment, strings.Repeat("c", 200)).Prepare()
		if err != nil {
			t.Fatalf("size ...%02X: Prepare: %v", last, err)
		}
		out := applyToBytes(t, src, plan)
		if got, _, _ := readVint(out, 44, false); got != 1<<56-2 {
			t.Errorf("size ...%02X: the Segment declares %#x, want the largest size, %#x", last, got, uint64(1<<56-2))
		}
		assertSeekEntriesResolve(t, out)
		essenceUnchanged(t, src, out)
		if got := mustParseBytes(t, out); !slices.Equal(tagValues(got, tag.Comment), []string{strings.Repeat("c", 200)}) {
			t.Errorf("size ...%02X: COMMENT read back as %q", last, tagValues(got, tag.Comment))
		}
	}
}

// zerosAhead is data with n zero bytes put ahead of the element at off: the Segment declares
// them, and a two-byte SeekHead position of what stands at off or behind it follows.
func zerosAhead(t *testing.T, data []byte, off, n int) []byte {
	t.Helper()
	seg, segData, _, _ := elemRange(data, 0, len(data), testfile.IDSegment, nil)
	size, _, _ := readVint(data, seg+4, false)
	out := slices.Concat(data[:off], make([]byte, n), data[off:])
	binary.BigEndian.PutUint64(out[seg+4:], 1<<56|(size+uint64(n)))
	seek, seekData, seekEnd, ok := elemRange(out, segData, off, idSeekHead, nil)
	if !ok {
		t.Fatal("no SeekHead ahead of the zeros")
	}
	for at := seekData; ; {
		i := bytes.Index(out[at:seekEnd], []byte{0x53, 0xAC, 0x82})
		if i < 0 {
			break
		}
		at += i + 3
		if pos := int(binary.BigEndian.Uint16(out[at:])); segData+pos >= off {
			binary.BigEndian.PutUint16(out[at:], uint16(pos+n))
		}
	}
	refreshCRC(t, out, seek)
	return out
}

// Bytes between two elements of the Segment that are no element are stepped over: what stands
// behind them is read and indexed as ever, and they stay where they are through an edit.
func TestMatroskaBytesBetweenElementsAreSteppedOver(t *testing.T) {
	whole := readFixture(t, chaptersMKA)
	segData := 52
	cues, _, _, _ := elemRange(whole, segData, len(whole), testfile.IDCues, nil)
	src := zerosAhead(t, whole, cues, 16)
	doc := mustParseBytes(t, src)
	const want = "16 byte(s) inside the Segment are no element; preserved verbatim"
	if w, ok := docWarning(doc, wl.WarnTrailingBytes); !ok || w.Message != want {
		t.Errorf("read warning = %q (found %v), want %q", w.Message, ok, want)
	}
	plan, err := doc.Edit().Set(tag.Comment, strings.Repeat("c", 300)).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	assertSeekEntriesResolve(t, out)
	cluster, _, _, _ := elemRange(out, segData, len(out), testfile.IDCluster, nil)
	at := bytes.LastIndex(out, testfile.IDBytes(testfile.IDCues))
	pos := bytes.Index(out[at:], []byte{idCueClusterPos, 0x82})
	if at < 16 || !bytes.Equal(out[at-16:at], make([]byte, 16)) || pos < 0 || segData+int(binary.BigEndian.Uint16(out[at+pos+2:])) != cluster {
		t.Errorf("the Cues at %d do not stand behind the 16 zero bytes with a position of the Cluster at %d", at, cluster-segData)
	}
	essenceUnchanged(t, src, out)
	if w, ok := docWarning(mustParseBytes(t, out), wl.WarnTrailingBytes); !ok || w.Message != want {
		t.Errorf("the written file warns %q (found %v), want %q", w.Message, ok, want)
	}

	// Tags behind such bytes are read, and an edit of them leaves one Tags element.
	last, _ := tagsLast(t)
	tags := bytes.LastIndex(last, testfile.IDBytes(testfile.IDTags))
	src = zerosAhead(t, last, tags, 16)
	doc = mustParseBytes(t, src)
	if got := tagValues(doc, tag.Artist); !slices.Equal(got, []string{"Test Artist"}) {
		t.Errorf("ARTIST behind the zero bytes = %q, want [Test Artist]", got)
	}
	_, out = setComposerAndTrack(t, src)
	cluster, _, _, _ = elemRange(out, segData, len(out), testfile.IDCluster, nil)
	if n := bytes.Count(out[cluster:], testfile.IDBytes(testfile.IDTags)); n != 1 {
		t.Errorf("%d Tags elements behind the Cluster of the written file, want 1", n)
	}
	ffprobeReadsComposerAndTrack(t, out)
}

// More bytes that are no element than a resync looks through end the read: what follows is not
// read, the warning says so, and an edit is refused, as nothing can say what it would move.
func TestMatroskaTooManyBytesThatAreNoElement(t *testing.T) {
	whole := readFixture(t, chaptersMKA)
	cues, _, _, _ := elemRange(whole, 52, len(whole), testfile.IDCues, nil)
	src := zerosAhead(t, whole, cues, 70<<10)
	doc := mustParseBytes(t, src)
	if w, ok := docWarning(doc, wl.WarnTrailingBytes); !ok || !strings.Contains(w.Message, "the read stops at offset "+strconv.Itoa(cues)) {
		t.Errorf("read warning = %q (found %v), want trailing-bytes naming offset %d where the read stops", w.Message, ok, cues)
	}
	if len(doc.Chapters()) != 3 || !slices.Equal(tagValues(doc, tag.Artist), []string{"Test Artist"}) {
		t.Errorf("%d chapters, ARTIST %q; want what stands ahead of the bytes read", len(doc.Chapters()), tagValues(doc, tag.Artist))
	}
	for name, edit := range map[string]func(*wl.Editor) *wl.Editor{
		"a title that fits the padding": func(e *wl.Editor) *wl.Editor { return e.Set(tag.Title, "T") },
		"a comment that moves the tail": func(e *wl.Editor) *wl.Editor { return e.Set(tag.Comment, strings.Repeat("c", 300)) },
	} {
		if _, err := edit(doc.Edit()).Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("%s: Prepare = %v, want the refusal to rewrite the file", name, err)
		}
	}
	if plan, err := doc.Edit().Prepare(); err != nil || !plan.IsNoOp() {
		t.Errorf("an edit of nothing: %v, want a plan that writes nothing", err)
	}
}

// A second Chapters element is not read, so whatever it holds costs the parse nothing, and the
// chapter edit that drops it always says so.
func TestMatroskaDuplicateChaptersElementIsNotRead(t *testing.T) {
	first := testfile.El(idChapters, mkEdition(true, nil, mkAtom(1, 0, 0, "A")))
	var atoms [][]byte
	for i := range 50 {
		atoms = append(atoms, mkAtom(uint64(i+2), uint64(i)*1e9, 0, "x"))
	}
	crowded := testfile.El(idChapters, mkEdition(true, nil, atoms...))
	alone := buildMatroskaCh("matroska", "T", first, nil)
	limits := wl.Limits{}
	for n := 1; ; n++ {
		limits.MaxElements = n
		if _, err := wl.Parse(context.Background(), wl.BytesSource(alone), wl.WithLimits(limits)); err == nil {
			break
		}
	}
	limits.MaxElements++ // the second element itself
	src := buildMatroskaCh("matroska", "T", concat(first, crowded), nil)
	doc, err := wl.Parse(context.Background(), wl.BytesSource(src), wl.WithLimits(limits))
	if err != nil {
		t.Fatalf("under an element limit of %d, one more than the file with one Chapters element needs: %v", limits.MaxElements, err)
	}
	if _, ok := docWarning(doc, wl.WarnDuplicateTagBlock); !ok || len(doc.Chapters()) != 1 {
		t.Errorf("%d chapters with warnings %v; want the first element's one, and duplicate-tag-block", len(doc.Chapters()), doc.Warnings())
	}

	// One whose only atom runs past its edition holds no chapter a reader lists.
	damaged := testfile.El(idChapters, mkEdition(true, nil, past(mkAtom(9, 5e9, 0, "Z"), 1)))
	src = buildMatroskaCh("matroska", "T", concat(first, damaged), nil)
	doc = mustParseBytes(t, src)
	plan, err := doc.Edit().SetChapters(append(doc.Chapters(), wl.Chapter{Start: time.Second, Title: "B"})...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnDuplicateTagBlockDropped); !ok || !strings.Contains(w.Message, "1 Chapters element(s)") {
		t.Errorf("chapter edit warning = %q (found %v), want duplicate-tag-block-dropped for the one element", w.Message, ok)
	}
	if out := applyToBytes(t, src, plan); countSegChildren(t, out, idChapters) != 1 {
		t.Errorf("%d Chapters elements written, want 1", countSegChildren(t, out, idChapters))
	}
}

// A resync takes no element that would run past a Segment whose end is its own, takes one a cut
// file ends in, and looks through 64 KiB and no further.
func TestMatroskaResyncCandidates(t *testing.T) {
	tags := testfile.El(testfile.IDTags, testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "A")))
	segment := func(body ...[]byte) []byte {
		return concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, testfile.AudioCluster(0), concat(body...)))
	}
	artist := func(src []byte) []string { return tagValues(mustParseBytes(t, src), tag.Artist) }

	if got := artist(segment([]byte{0}, tags)); !slices.Equal(got, []string{"A"}) {
		t.Errorf("ARTIST behind one zero byte = %q, want [A]", got)
	}

	// Bytes that would be Cues of 4095 bytes, more than the Segment holds, are no element.
	fake := concat([]byte{0, 0}, testfile.IDBytes(testfile.IDCues), []byte{0x4F, 0xFF, 0, 0})
	if got := artist(segment(fake, tags)); !slices.Equal(got, []string{"A"}) {
		t.Errorf("ARTIST behind an ID with a size past the Segment = %q, want [A]", got)
	}

	// A file that ends inside the Tags behind such bytes: the Tags are read as far as they go.
	two := testfile.El(testfile.IDTags, testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "A"), testfile.SimpleTag("GENRE", "G")))
	cut := segment(make([]byte, 16), two)
	cut = cut[:len(cut)-3]
	doc := mustParseBytes(t, cut)
	if got := tagValues(doc, tag.Artist); !slices.Equal(got, []string{"A"}) || doc.Tags().Has(tag.Genre) {
		t.Errorf("a cut file: ARTIST %q, GENRE %q; want the tag ahead of the cut alone", got, tagValues(doc, tag.Genre))
	}
	if w, ok := docWarning(doc, wl.WarnTrailingBytes); !ok || !strings.Contains(w.Message, "16 byte(s)") {
		t.Errorf("a cut file: trailing-bytes warning %q (found %v), want the 16 bytes ahead of the Tags", w.Message, ok)
	}

	for _, tc := range []struct {
		zeros int
		read  bool
	}{{64 << 10, true}, {64<<10 + 1, false}} {
		doc := mustParseBytes(t, segment(make([]byte, tc.zeros), tags))
		w, _ := docWarning(doc, wl.WarnTrailingBytes)
		if got := doc.Tags().Has(tag.Artist); got != tc.read || strings.Contains(w.Message, "the read stops") == tc.read {
			t.Errorf("%d zero bytes: ARTIST read %v with warning %q, want read %v", tc.zeros, got, w.Message, tc.read)
		}
	}

	// Each run of such bytes counts toward the element limit.
	var runs [][]byte
	for range 40 {
		runs = append(runs, []byte{0}, testfile.AudioCluster(0))
	}
	if _, err := wl.Parse(context.Background(), wl.BytesSource(segment(runs...)), wl.WithLimits(wl.Limits{MaxElements: 20})); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("40 runs under a limit of 20 elements: %v, want size-too-large", err)
	}
}
