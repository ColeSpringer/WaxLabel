package waxlabel_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// MP4's Nero chpl truncates a chapter title past 255 bytes, so copying a chapter whose title
// exceeds that to MP4 must grade the chapter set Lossy (with the truncation reason).
func TestCopyChapterTitleTooLongReportsLossy(t *testing.T) {
	longTitle := strings.Repeat("x", 300)
	// A FLAC source carrying one chapter with a 300-byte title (FLAC has no title cap).
	srcBase := readFixture(t, "../testdata/notags.flac")
	plan0, err := mustParseBytes(t, srcBase).Edit().SetChapters(wl.Chapter{Start: 0, Title: longTitle}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	src := mustParseBytes(t, applyToBytes(t, srcBase, plan0))
	if chs := src.Chapters(); len(chs) != 1 || len(chs[0].Title) != 300 {
		t.Fatalf("setup: source should carry one 300-byte-title chapter, got %v", chs)
	}

	// Onto an MP4 destination the title exceeds the 255-byte chpl cap -> Lossy.
	dstMP4 := mustParseBytes(t, readFixture(t, "../testdata/notags.m4a"))
	_, reportMP4, err := src.PrepareTransfer(dstMP4)
	if err != nil {
		t.Fatalf("PrepareTransfer to MP4: %v", err)
	}
	if it := chapterItem(t, reportMP4); it.Disposition != wl.Lossy || !strings.Contains(it.Reason, "too long") {
		t.Errorf("MP4 chapter carry = %s (%q), want Lossy with the truncation reason", it.Disposition, it.Reason)
	}

	// FLAC has no title cap, so the same title stays Carried.
	dstFLAC := mustParseBytes(t, readFixture(t, "../testdata/notags.flac"))
	_, reportFLAC, err := src.PrepareTransfer(dstFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if it := chapterItem(t, reportFLAC); it.Disposition != wl.Carried {
		t.Errorf("FLAC chapter carry = %s, want Carried (no title byte cap)", it.Disposition)
	}
}

// chapterItem returns the single chapter transfer item from a report, failing if absent.
func chapterItem(t *testing.T, r wl.TransferReport) wl.TransferItem {
	t.Helper()
	for _, it := range r.Items {
		if it.Kind == wl.TransferChapter {
			return it
		}
	}
	t.Fatal("no chapter transfer item in report")
	return wl.TransferItem{}
}

// copy path: a source whose MEDIATYPE comment carries surrounding whitespace (stored raw here,
// bypassing the writer's own trim) transfers the trimmed single-token value to the destination,
// matching the set path so set, lint, and copy agree on the stored form.
func TestCopyTrimsMediaType(t *testing.T) {
	srcBytes := flacWithCommentBlock([]vorbis.Comment{
		{Name: "MEDIATYPE", Value: " 2 "},
		{Name: "REPLAYGAIN_TRACK_GAIN", Value: " -7.30 dB "},
	})
	src := mustParseBytes(t, srcBytes)
	dstBytes := readFixture(t, "../testdata/notags.flac")
	dst := mustParseBytes(t, dstBytes)

	plan, _, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	if got, _ := result.Get(tag.MediaType); len(got) != 1 || got[0] != "2" {
		t.Errorf("copied MEDIATYPE = %v, want [\"2\"] (surrounding whitespace trimmed)", got)
	}
	if got, _ := result.Get(tag.ReplayGainTrackGain); len(got) != 0 {
		t.Errorf("REPLAYGAIN_TRACK_GAIN should not be carried by copy (own-audio), got %v", got)
	}
}

// MP4 drops a literal 0 in a trkn/disk slot on read, so the transfer grading must report
// TRACKNUMBER=0 (even paired with a total) as dropped, not carried, matching what the writer
// stores and reads back.
func TestPrepareTransferMP4ZeroTrackDropped(t *testing.T) {
	srcBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.TrackNumber, "0").Set(tag.TrackTotal, "12")
	})
	src := mustParseBytes(t, srcBytes)
	dst := mustParseBytes(t, readFixture(t, "../testdata/notags.m4a"))
	_, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatal(err)
	}
	track, total := wl.Carried, wl.Carried
	sawTrack, sawTotal := false, false
	for _, it := range report.Items {
		if it.Kind != wl.TransferField {
			continue
		}
		switch it.Key {
		case tag.TrackNumber:
			track, sawTrack = it.Disposition, true
		case tag.TrackTotal:
			total, sawTotal = it.Disposition, true
		}
	}
	if !sawTrack || track != wl.Dropped {
		t.Errorf("TRACKNUMBER=0 to MP4: disposition = %v (seen=%v), want dropped (MP4 drops a 0 slot on read)", track, sawTrack)
	}
	if sawTotal && total != wl.Carried {
		t.Errorf("TRACKTOTAL=12 to MP4: disposition = %v, want carried (12 stores fine)", total)
	}
}

// simulates copying an M4B (tags + chapters) into a FLAC. Tags carry, and the MP4 start+title
// chapters also carry because FLAC writes the same chapter subset through VorbisComment CHAPTERxxx.
func TestPlanTransferReportsLosses(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	report, err := src.PlanTransfer(wl.FormatFLAC)
	if err != nil {
		t.Fatalf("PlanTransfer: %v", err)
	}
	if report.Source != wl.FormatMP4 || report.Dest != wl.FormatFLAC {
		t.Errorf("report formats = %s -> %s, want MP4 -> FLAC", report.Source, report.Dest)
	}

	var sawChapter bool
	for _, it := range report.Items {
		switch it.Kind {
		case wl.TransferField:
			if it.Key == tag.Encoder {
				// ENCODER describes the source audio, so metadata copies exclude it.
				if it.Disposition != wl.Excluded {
					t.Errorf("ENCODER = %s, want excluded", it.Disposition)
				}
				continue
			}
			if it.Disposition != wl.Carried {
				t.Errorf("field %s = %s, want carried (FLAC writes all fields)", it.Key, it.Disposition)
			}
		case wl.TransferChapter:
			sawChapter = true
			if it.Disposition != wl.Carried {
				t.Errorf("chapters = %s, want carried (FLAC stores start+title chapters)", it.Disposition)
			}
		}
	}
	if !sawChapter {
		t.Error("expected a chapter item (the M4B has chapters)")
	}
	if !report.Lossless() {
		t.Errorf("MP4 start+title chapters into FLAC should be lossless, report: %+v", report.Items)
	}
}

// simulating a transfer to a format with no codec is an error, while a writable destination
// (Matroska) carries the canonical set.
func TestPlanTransferUnsupportedDest(t *testing.T) {
	src := mustParseFile(t, sampleFLAC)

	if _, err := src.PlanTransfer(wl.FormatUnknown); !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("PlanTransfer(Unknown) err = %v, want ErrUnsupportedFormat", err)
	}

	report, err := src.PlanTransfer(wl.FormatMatroska)
	if err != nil {
		t.Fatalf("PlanTransfer(Matroska): %v", err)
	}
	carried, _, dropped := report.Counts()
	if carried == 0 || dropped != 0 {
		t.Errorf("writable Matroska should carry the canonical fields, got %+v", report)
	}
}

// PrepareTransfer's loss report matches the result of executing its plan: carried fields land with
// the source's values, and dropped chapters do not appear.
func TestPrepareTransferReportMatchesResult(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	dstBytes := readFixture(t, "../testdata/notags.flac") // a blank canvas
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))

	carriedKeys := map[tag.Key]bool{}
	for _, it := range report.Items {
		switch it.Kind {
		case wl.TransferField:
			srcVals, _ := src.Get(it.Key)
			gotVals, present := result.Get(it.Key)
			switch it.Disposition {
			case wl.Carried:
				carriedKeys[it.Key] = true
				if !present || !slices.Equal(gotVals, srcVals) {
					t.Errorf("carried %s = %v (present=%v), want source values %v", it.Key, gotVals, present, srcVals)
				}
			case wl.Dropped:
				if present {
					t.Errorf("dropped %s should not have been written, got %v", it.Key, gotVals)
				}
			}
		case wl.TransferChapter:
			if it.Disposition == wl.Dropped && len(result.Chapters()) != 0 {
				t.Errorf("chapters reported dropped but result has %d", len(result.Chapters()))
			}
		}
	}
	// The blank destination had no tags, so the result's keys are exactly the carried set; the report
	// and the write cannot disagree on membership.
	for _, k := range result.Tags().Keys() {
		if !carriedKeys[k] {
			t.Errorf("result has key %s the report did not mark carried", k)
		}
	}
}

func TestPrepareTransferOverlayKeepsDestKeys(t *testing.T) {
	src := mustParseBytes(t, readFixture(t, sampleFLAC)) // TITLE/ARTIST/ALBUM/ENCODER
	// A destination FLAC carrying a key the source lacks.
	dstBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set("CATALOGNUMBER", "KEPT-001")
	})
	dst := mustParseBytes(t, dstBytes)

	plan, _, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))

	if got, _ := result.Get("CATALOGNUMBER"); len(got) != 1 || got[0] != "KEPT-001" {
		t.Errorf("CATALOGNUMBER = %v, want it preserved from the destination", got)
	}
	if got, _ := result.Get("TITLE"); len(got) != 1 || got[0] != "Original Title" {
		t.Errorf("TITLE = %v, want overlaid from the source", got)
	}
}

// source front cover carries into a picture-capable destination and matches byte-for-byte.
func TestPrepareTransferCarriesPictures(t *testing.T) {
	png := testfile.PNG()
	srcBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set("TITLE", "Cover Test")
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: png})
	})
	src := mustParseBytes(t, srcBytes)

	dstBytes := readFixture(t, "../testdata/notags.m4a")
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	var pic wl.TransferItem
	for _, it := range report.Items {
		if it.Kind == wl.TransferPicture {
			pic = it
		}
	}
	if pic.Disposition != wl.Carried || pic.Count != 1 {
		t.Fatalf("picture item = %+v, want 1 carried (a plain front cover round-trips on MP4)", pic)
	}

	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	pics := result.Pictures()
	if len(pics) != 1 || !slices.Equal(pics[0].Data, png) {
		t.Errorf("destination picture not carried byte-for-byte (got %d pictures)", len(pics))
	}
}

// format-only simulation and the project-onto-a-document path agree, because both consult the same
// decision function with the same destination capabilities.
func TestPlanTransferMatchesPrepareTransfer(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	dst := mustParseBytes(t, readFixture(t, "../testdata/notags.flac"))
	assertPlanMatchesPrepare(t, src.Transfer(), dst)
}

// With no file to consult, the simulation still says what a write to a blank file of the format
// stores: every loss a writer makes is one the format's capabilities state.
func TestPlanTransferMatchesPrepareOnBlankDestinations(t *testing.T) {
	forEachTransfer(t, blankTransferDestinations, func(t *testing.T, s transferSource, srcBytes, dstBytes []byte) {
		assertPlanMatchesPrepare(t, mustParseBytes(t, srcBytes).Transfer(), mustParseBytes(t, dstBytes), s.opts...)
	})
}

// assertPlanMatchesPrepare holds the report of tr simulated for dst's format to the one prepared
// onto dst itself.
func assertPlanMatchesPrepare(t *testing.T, tr *wl.Transfer, dst *wl.Document, opts ...wl.WriteOption) {
	t.Helper()
	sim, err := tr.Plan(dst.Format(), opts...)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	_, applied, err := tr.Prepare(dst, opts...)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !slices.Equal(sim.Items, applied.Items) {
		t.Errorf("the simulation and the prepared transfer differ:\n   Plan %+v\nPrepare %+v", sim.Items, applied.Items)
	}
}

// MP4 covr atom can only label JPEG/PNG/BMP, so a cover in another format must fail at Prepare
// instead of being stored mislabeled as JPEG, which a cross-format copy would then claim "carried
// losslessly". A supported format still writes.
func TestMP4RejectsUnstorableCover(t *testing.T) {
	doc := mustParseBytes(t, readFixture(t, "../testdata/notags.m4a"))
	_, err := doc.Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/webp", Data: []byte("RIFF....WEBP")}).
		Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("err = %v, want ErrUnsupportedTag for a WebP cover on MP4", err)
	}
	if _, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare(); err != nil {
		t.Errorf("a PNG cover should be accepted: %v", err)
	}
}

// GIF cover MP4 cannot store is dropped and reported without failing the whole copy.
func TestPrepareTransferDropsUnrepresentableCover(t *testing.T) {
	srcBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set("TITLE", "GIF Cover")
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.GIF()})
	})
	src := mustParseBytes(t, srcBytes)
	if pics := src.Pictures(); len(pics) != 1 || pics[0].MIME != "image/gif" {
		t.Fatalf("setup: source cover = %+v, want one image/gif", pics)
	}

	dstBytes := readFixture(t, "../testdata/notags.m4a")
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer must not fail on an unrepresentable cover: %v", err)
	}
	carried, _, dropped := report.Counts()
	if dropped != 1 {
		t.Errorf("dropped count = %d, want 1 (the GIF cover)", dropped)
	}
	if carried == 0 {
		t.Error("expected the TITLE field carried")
	}
	var pic wl.TransferItem
	for _, it := range report.Items {
		if it.Kind == wl.TransferPicture {
			pic = it
		}
	}
	if pic.Disposition != wl.Dropped || !strings.Contains(pic.Reason, "image/gif") {
		t.Errorf("picture item = %+v, want Dropped naming image/gif", pic)
	}

	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	if v, _ := result.Get(tag.Title); len(v) == 0 || v[0] != "GIF Cover" {
		t.Errorf("destination TITLE = %v, want [GIF Cover] (tags carried despite the dropped cover)", v)
	}
	if n := len(result.Pictures()); n != 0 {
		t.Errorf("destination has %d pictures, want 0 (GIF dropped, dest had none)", n)
	}
}

func TestPrepareTransferUnrepresentableCoverKeepsDestCover(t *testing.T) {
	// Source carries only a GIF cover an MP4 covr atom cannot store.
	srcBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.GIF()})
	})
	src := mustParseBytes(t, srcBytes)

	// Destination MP4 already holds a storable PNG cover.
	dstBytes := writeBack(t, "../testdata/notags.m4a", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()})
	})
	if n := len(mustParseBytes(t, dstBytes).Pictures()); n != 1 {
		t.Fatalf("setup: dest cover count = %d, want 1", n)
	}

	plan, _, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	if n := len(result.Pictures()); n != 1 {
		t.Errorf("dest ends with %d covers, want 1 (its own PNG preserved; no source cover is representable)", n)
	}
}

// checks transfer grading for chapter metadata. Matroska chapters with gapped ends or any
// per-chapter language (uniform or varying) are lossy when copied to MP4's start+title storage,
// which stores no language; only language-free chapters are carried, and a Matroska destination
// carries the richer metadata.
func TestPlanTransferMatroskaToMP4ChapterLoss(t *testing.T) {
	src := readFixture(t, sampleMKA)
	withChapters := func(chs ...wl.Chapter) *wl.Document {
		out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().SetChapters(chs...))
		return mustParseBytes(t, out)
	}
	chapterItem := func(doc *wl.Document, dst wl.Format) wl.TransferItem {
		report, err := doc.PlanTransfer(dst)
		if err != nil {
			t.Fatalf("PlanTransfer: %v", err)
		}
		for _, it := range report.Items {
			if it.Kind == wl.TransferChapter {
				return it
			}
		}
		t.Fatal("no chapter item in report")
		return wl.TransferItem{}
	}

	// Gapped end (chapter 1 ends at 200ms, chapter 2 starts at 450ms) + varying language.
	rich := withChapters(
		wl.Chapter{Start: 0, End: ms(200), Title: "Intro", LanguageIETF: "en-US"},
		wl.Chapter{Start: ms(450), Title: "Main", LanguageIETF: "fr-FR"},
	)
	if it := chapterItem(rich, wl.FormatMP4); it.Disposition != wl.Lossy || it.Reason == "" {
		t.Errorf("rich chapters -> MP4 = %s/%q, want lossy with a reason", it.Disposition, it.Reason)
	}
	// A Matroska destination loses nothing, so the same chapters carry (control).
	if it := chapterItem(rich, wl.FormatMatroska); it.Disposition != wl.Carried {
		t.Errorf("rich chapters -> Matroska = %s, want carried", it.Disposition)
	}
	// A uniform per-chapter language: MP4's start+title storage holds no language field, so
	// even a uniform language is a lossy carry (these formats store none at all).
	uniformLang := withChapters(
		wl.Chapter{Start: 0, Title: "One", LanguageIETF: "en-US"},
		wl.Chapter{Start: ms(300), Title: "Two", LanguageIETF: "en-US"},
	)
	if it := chapterItem(uniformLang, wl.FormatMP4); it.Disposition != wl.Lossy || it.Reason == "" {
		t.Errorf("uniform-language chapters -> MP4 = %s/%q, want lossy with a reason", it.Disposition, it.Reason)
	}
	// Language-free chapters with contiguous ends: nothing MP4 cannot represent -> carried.
	languageFree := withChapters(
		wl.Chapter{Start: 0, Title: "One"},
		wl.Chapter{Start: ms(300), Title: "Two"},
	)
	if it := chapterItem(languageFree, wl.FormatMP4); it.Disposition != wl.Carried {
		t.Errorf("language-free chapters -> MP4 = %s, want carried", it.Disposition)
	}
	// Only the final chapter carries an explicit end (no interior gap), which MP4's QuickTime text
	// track stores, so the end is fine; the uniform language is still dropped, so the copy is lossy.
	lastEndOnly := withChapters(
		wl.Chapter{Start: 0, Title: "One", LanguageIETF: "en-US"},
		wl.Chapter{Start: ms(300), End: ms(600), Title: "Two", LanguageIETF: "en-US"},
	)
	if it := chapterItem(lastEndOnly, wl.FormatMP4); it.Disposition != wl.Lossy || it.Reason == "" {
		t.Errorf("last-end-only uniform-language chapters -> MP4 = %s/%q, want lossy for the language (QuickTime keeps the final end)", it.Disposition, it.Reason)
	}
}

// WebM destination with an existing cover attachment can still receive transferable tags.
func TestPrepareTransferToWebMWithExistingCover(t *testing.T) {
	src := mustParseBytes(t, coverBearingFLAC(t, "WebM Cover Src"))

	webmWithCover := func(name, mime string, data []byte) []byte {
		return testfile.Matroska("webm", "WebM Dst", testfile.El(idAttachments, testfile.El(idAttached, concat(
			mkStr(idFileName, name), mkStr(idFileMime, mime), testfile.El(idFileData, data)))))
	}
	pictureItem := func(report wl.TransferReport) (pic wl.TransferItem) {
		for _, it := range report.Items {
			if it.Kind == wl.TransferPicture {
				pic = it
			}
		}
		return pic
	}
	dstBytes := webmWithCover("cover.jpg", "image/jpeg", testfile.JPEG())
	if n := len(mustParseBytes(t, dstBytes).Pictures()); n != 1 {
		t.Fatalf("setup: the WebM destination should surface its 1 cover attachment, got %d", n)
	}

	plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("PrepareTransfer onto a WebM with an existing cover must not error: %v", err)
	}
	if pic := pictureItem(report); pic.Disposition != wl.Dropped {
		t.Errorf("source cover = %s, want dropped for a WebM destination", pic.Disposition)
	}
	out := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	if got, _ := out.Get(tag.Title); len(got) == 0 || got[0] != "WebM Cover Src" {
		t.Errorf("carried TITLE = %v, want [WebM Cover Src] (tags carry despite the WebM cover)", got)
	}
	if pics := out.Pictures(); len(pics) != 1 || !bytes.Equal(pics[0].Data, testfile.JPEG()) {
		t.Errorf("the destination's own cover should stay, got %d pictures", len(pics))
	}

	// A WebM that already holds the source's cover loses nothing: there is nothing to write.
	same := mustParseBytes(t, webmWithCover("cover.png", "image/png", testfile.PNG()))
	if _, report, err = src.PrepareTransfer(same); err != nil {
		t.Fatal(err)
	}
	if pic := pictureItem(report); pic.Disposition != wl.Carried || pic.Count != 1 {
		t.Errorf("a cover the destination already holds = %+v, want 1 carried", pic)
	}
}

// mixed cover set carries the storable JPEG and drops only the GIF.
func TestPrepareTransferMixedCoversSplit(t *testing.T) {
	jpeg := testfile.JPEG()
	srcBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: jpeg})
		e.AddPicture(wl.Picture{Type: wl.PicBackCover, Data: testfile.GIF()})
	})
	src := mustParseBytes(t, srcBytes)

	dstBytes := readFixture(t, "../testdata/notags.m4a")
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	var carried, dropped wl.TransferItem
	for _, it := range report.Items {
		if it.Kind != wl.TransferPicture {
			continue
		}
		if it.Disposition == wl.Dropped {
			dropped = it
		} else {
			carried = it
		}
	}
	if carried.Disposition != wl.Carried || carried.Count != 1 {
		t.Errorf("carried picture item = %+v, want one Carried (the JPEG)", carried)
	}
	if dropped.Count != 1 || !strings.Contains(dropped.Reason, "image/gif") || strings.Contains(dropped.Reason, "image/jpeg") {
		t.Errorf("dropped picture item = %+v, want count 1 naming only image/gif", dropped)
	}

	// The destination must hold exactly the JPEG, byte-for-byte: written plus dropped
	// equals the source set.
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	pics := result.Pictures()
	if len(pics) != 1 || pics[0].MIME != "image/jpeg" || !slices.Equal(pics[0].Data, jpeg) {
		t.Fatalf("destination pictures = %d (want 1 JPEG byte-equal); the GIF must be withheld, not stored", len(pics))
	}
}

// projects a FLAC's tags onto a Matroska canvas and confirms the report matches the written result
// (Title goes to Info.Title, the rest to SimpleTags).
func TestPrepareTransferToMatroska(t *testing.T) {
	src := mustParseFile(t, sampleFLAC)
	dstBytes := readFixture(t, "../testdata/notags.mka")
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if carried, _, dropped := report.Counts(); carried == 0 || dropped != 0 {
		t.Fatalf("expected canonical fields carried, got %+v", report)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	// Check the cleanly-mapping core fields land with the source values (TITLE goes to Info.Title, the
	// rest to SimpleTags).
	for _, k := range []tag.Key{tag.Title, tag.Artist, tag.Album, tag.Encoder} {
		srcVals, _ := src.Get(k)
		gotVals, present := result.Get(k)
		if len(srcVals) > 0 && (!present || !slices.Equal(gotVals, srcVals)) {
			t.Errorf("carried %s = %v (present=%v), want %v", k, gotVals, present, srcVals)
		}
	}
}

// coverBearingFLAC returns an in-memory FLAC carrying a title and a front cover,
// the cross-format source for the WebM cover-gating tests.
func coverBearingFLAC(t *testing.T, title string) []byte {
	t.Helper()
	return writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set("TITLE", title)
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()})
	})
}

// format-only PlanTransfer has no destination file, so Matroska answers file-agnostically and still
// reports a cover writable. The WebM cover refusal is a per-file constraint that only
// PrepareTransfer/copy can see.
func TestPlanTransferMatroskaCoverWritable(t *testing.T) {
	src := mustParseBytes(t, coverBearingFLAC(t, "Cover Test"))
	report, err := src.PlanTransfer(wl.FormatMatroska)
	if err != nil {
		t.Fatalf("PlanTransfer(Matroska): %v", err)
	}
	var pic wl.TransferItem
	for _, it := range report.Items {
		if it.Kind == wl.TransferPicture {
			pic = it
		}
	}
	if pic.Disposition != wl.Carried {
		t.Errorf("cover = %s, want carried (no file means the WebM constraint is invisible)", pic.Disposition)
	}
}

// file-dependent capability: projecting a cover-bearing source onto WebM reports the cover dropped
// (Attachments is outside the WebM subset), and the executed plan carries no picture while the tags
// still land.
func TestPrepareTransferCoverToWebMDropsCover(t *testing.T) {
	src := mustParseBytes(t, coverBearingFLAC(t, "WebM Transfer"))

	dstBytes := readFixture(t, sampleWebM)
	dst := mustParseBytes(t, dstBytes)

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer onto WebM: %v", err)
	}
	var pic wl.TransferItem
	for _, it := range report.Items {
		if it.Kind == wl.TransferPicture {
			pic = it
		}
	}
	if pic.Disposition != wl.Dropped {
		t.Fatalf("cover = %s, want dropped for a WebM destination", pic.Disposition)
	}
	if pic.Reason == "" {
		t.Error("a dropped cover must carry a reason")
	}

	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	if got := result.Pictures(); len(got) != 0 {
		t.Errorf("WebM result has %d pictures, want 0 (the cover was dropped)", len(got))
	}
	// The tags still transfer; only the cover is gated by the WebM subset.
	if got, ok := result.Get(tag.Title); !ok || !slices.Equal(got, []string{"WebM Transfer"}) {
		t.Errorf("carried TITLE = %v (present=%v), want [WebM Transfer]", got, ok)
	}
}

// picture set can be lossy even when the image bytes carry losslessly.
func TestTransferPictureDisposition(t *testing.T) {
	png := testfile.PNG()
	flacWith := func(p wl.Picture) []byte {
		return writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
			e.Set("TITLE", "X")
			e.AddPicture(p)
		})
	}
	front := wl.Picture{Type: wl.PicFrontCover, Data: png}
	describedFront := wl.Picture{Type: wl.PicFrontCover, Description: "liner", Data: png}
	back := wl.Picture{Type: wl.PicBackCover, Data: png}
	other := wl.Picture{Type: wl.PicOther, Data: png}

	disp := func(srcBytes []byte, dst wl.Format) wl.Disposition {
		rep, err := mustParseBytes(t, srcBytes).PlanTransfer(dst)
		if err != nil {
			t.Fatalf("PlanTransfer to %s: %v", dst, err)
		}
		for _, it := range rep.Items {
			if it.Kind == wl.TransferPicture {
				return it.Disposition
			}
		}
		t.Fatalf("no picture item in transfer to %s", dst)
		return wl.Dropped
	}

	cases := []struct {
		name string
		src  []byte
		dst  wl.Format
		want wl.Disposition
	}{
		{"plain front -> MP4", flacWith(front), wl.FormatMP4, wl.Carried},
		{"back cover -> MP4", flacWith(back), wl.FormatMP4, wl.Lossy},
		{"described front -> MP4", flacWith(describedFront), wl.FormatMP4, wl.Lossy},
		{"described front -> Matroska", flacWith(describedFront), wl.FormatMatroska, wl.Carried},
		{"back cover -> Matroska", flacWith(back), wl.FormatMatroska, wl.Lossy},
		{"other -> Matroska", flacWith(other), wl.FormatMatroska, wl.Carried}, // PicOther round-trips as Other
		{"other -> MP4", flacWith(other), wl.FormatMP4, wl.Lossy},             // MP4 reads every cover back as front
		{"plain front -> FLAC", flacWith(front), wl.FormatFLAC, wl.Carried},
	}
	for _, c := range cases {
		if got := disp(c.src, c.dst); got != c.want {
			t.Errorf("%s: disposition = %v, want %v", c.name, got, c.want)
		}
	}
}

// under --numeric-genre a recognized GENRE is stored as a numeric reference (ID3 TCON / MP4 gnre)
// and re-read as its canonical name, so the destination mutates the value. The transfer report must
// grade it Lossy, not Carried; without the option the genre carries verbatim.
func TestPlanTransferNumericGenreLossy(t *testing.T) {
	src := mustParseBytes(t, append(testfile.ID3v2(3, testfile.TextFrame(3, "TCON", "Rock"), testfile.TextFrame(3, "TIT2", "T")), mp3Audio(t)...))

	genreDisp := func(dst wl.Format, opts ...wl.WriteOption) wl.Disposition {
		report, err := src.PlanTransfer(dst, opts...)
		if err != nil {
			t.Fatalf("PlanTransfer(%v): %v", dst, err)
		}
		for _, it := range report.Items {
			if it.Kind == wl.TransferField && it.Key == tag.Genre {
				return it.Disposition
			}
		}
		t.Fatalf("transfer report to %v has no GENRE field", dst)
		return wl.Carried
	}

	// AIFF and WAV join AAC/MP3/MP4 under --numeric-genre.
	for _, dst := range []wl.Format{wl.FormatAAC, wl.FormatMP3, wl.FormatMP4, wl.FormatAIFF, wl.FormatWAV} {
		if d := genreDisp(dst); d != wl.Carried {
			t.Errorf("GENRE transfer to %v without numeric-genre = %s, want carried", dst, d)
		}
		if d := genreDisp(dst, wl.WithNumericGenre()); d != wl.Lossy {
			t.Errorf("GENRE transfer to %v under numeric-genre = %s, want lossy (numeric resolution mutates the value)", dst, d)
		}
	}
}

// AIFF has no native genre slot, so a --numeric-genre write stores the genre as a numeric ID3 TCON,
// re-read as its canonical name; mutating "rock" -> "Rock".
func TestAiffNumericGenreReducedWarns(t *testing.T) {
	data := testfile.AIFF("AIFF", stdCOMM(), aiffSSND(400), aiffID3(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "T"))))

	num, err := mustParseBytes(t, data).Edit().Set(tag.Genre, "rock").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatalf("prepare numeric: %v", err)
	}
	if !hasKeyedValueReduced(num.Report().Warnings, tag.Genre) {
		t.Errorf("AIFF --numeric-genre GENRE must warn value-reduced; got %v", num.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, data, num)).Tags().First(tag.Genre); v != "Rock" {
		t.Errorf("AIFF --numeric-genre GENRE round-trip = %q, want the mutated canonical Rock", v)
	}

	plain := prepareWith(t, data, func(e *wl.Editor) { e.Set(tag.Genre, "rock") })
	if hasKeyedValueReduced(plain.Report().Warnings, tag.Genre) {
		t.Errorf("a plain (text) GENRE write must not warn value-reduced; got %v", plain.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, data, plain)).Tags().First(tag.Genre); v != "rock" {
		t.Errorf("plain GENRE round-trip = %q, want rock verbatim", v)
	}
}

// WAV genre uses native LIST/INFO IGNR text unless an id3 chunk is present, in which case the id3
// value wins read precedence. With a preserved id3 chunk a --numeric-genre write's numeric TCON
// becomes authoritative and mutates "rock" -> "Rock", so it must warn; a bare WAV keeps IGNR text
// losslessly and must not warn.
func TestWavNumericGenreReducedWithID3(t *testing.T) {
	withID3 := testfile.WAV(testfile.WAVFmtPCM(), wavID3(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "T"))), testfile.WAVData(400))
	wplan, err := mustParseBytes(t, withID3).Edit().Set(tag.Genre, "rock").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatalf("prepare withID3: %v", err)
	}
	if !hasKeyedValueReduced(wplan.Report().Warnings, tag.Genre) {
		t.Errorf("WAV with an id3 chunk: --numeric-genre GENRE must warn value-reduced; got %v", wplan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, withID3, wplan)).Tags().First(tag.Genre); v != "Rock" {
		t.Errorf("WAV+id3 --numeric-genre GENRE round-trip = %q, want the mutated Rock", v)
	}

	bare := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVData(400))
	bplan, err := mustParseBytes(t, bare).Edit().Set(tag.Genre, "rock").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatalf("prepare bare: %v", err)
	}
	if hasKeyedValueReduced(bplan.Report().Warnings, tag.Genre) {
		t.Errorf("a bare WAV keeps native IGNR text, so --numeric-genre GENRE must not warn; got %v", bplan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, bare, bplan)).Tags().First(tag.Genre); v != "rock" {
		t.Errorf("bare WAV GENRE round-trip = %q, want rock verbatim (IGNR text)", v)
	}
}

// bare WAV whose edit forces an id3 chunk into existence (here via an unmapped key, which
// LIST/INFO cannot store) routes genre through that chunk too, so under --numeric-genre the
// numeric TCON mutates "rock" -> "Rock" even though the base file had no id3 chunk.
func TestWavForcedID3NumericGenreWarns(t *testing.T) {
	bare := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVData(400))
	plan, err := mustParseBytes(t, bare).Edit().
		Set(tag.Genre, "rock").
		Set(tag.Key("PRIVATE_X"), "x"). // unmapped key -> not INFO-representable -> forces an id3 chunk
		Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !hasKeyedValueReduced(plan.Report().Warnings, tag.Genre) {
		t.Errorf("a forced-id3 bare WAV --numeric-genre GENRE must warn value-reduced; got %v", plan.Report().Warnings)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, bare, plan)).Tags().First(tag.Genre); v != "Rock" {
		t.Errorf("forced-id3 GENRE round-trip = %q, want the mutated Rock", v)
	}
}

// a copy that carries a multi-value field verbatim onto an ID3v2.3 destination (another field
// changes, so the multi-value frame is preserved, not re-rendered) must surface the
// [id3-multi-value] caveat, the same one a direct multi-value set warns.
func TestTransferCarriesV23MultiValueWarning(t *testing.T) {
	base := readFixture(t, sampleMP3) // ID3v2.3
	// A v2.3 MP3 carrying a multi-value ARTIST.
	multi := applyToBytes(t, base, mustPlan(t, mustParseBytes(t, base).Edit().Set(tag.Artist, "A", "B", "C")))

	// Source and destination both hold ARTIST=[A,B,C] but differ in TITLE, so the copy
	// changes only TITLE and carries the multi-value ARTIST verbatim.
	src := applyToBytes(t, multi, mustPlan(t, mustParseBytes(t, multi).Edit().Set(tag.Title, "Source Title")))
	dst := applyToBytes(t, multi, mustPlan(t, mustParseBytes(t, multi).Edit().Set(tag.Title, "Dest Title")))

	plan, _, err := mustParseBytes(t, src).PrepareTransfer(mustParseBytes(t, dst))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if plan.IsNoOp() {
		t.Fatal("expected a real write (TITLE changes), got a no-op")
	}
	warned := false
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnID3MultiValue {
			warned = true
		}
	}
	if !warned {
		t.Errorf("a copy carrying a v2.3 multi-value verbatim must warn id3-multi-value; got %v", plan.Report().Warnings)
	}
	// The carried multi-value still round-trips through WaxLabel's reader.
	if got := mustParseBytes(t, applyToBytes(t, dst, plan)).Fields().Artists; !slices.Equal(got, []string{"A", "B", "C"}) {
		t.Errorf("carried multi-value artists = %v, want [A B C]", got)
	}
}

// mustPlan prepares an edit, failing the test on error.
func mustPlan(t *testing.T, ed *wl.Editor) *wl.Plan {
	t.Helper()
	p, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// metadata copies leave file-audio values with the destination. Encoder stamps, ReplayGain, and
// fingerprints are reported as excluded, while recording identity and ordinary work metadata still
// transfer.
func TestTransferExcludesOwnAudioKeys(t *testing.T) {
	ownAudio := []tag.Key{
		tag.Encoder, tag.EncodedBy, tag.EncodingHistory, tag.AcoustIDFingerprint,
		tag.ReplayGainTrackGain, tag.ReplayGainTrackPeak, tag.ReplayGainAlbumGain, tag.ReplayGainAlbumPeak,
		"R128_TRACK_GAIN", "R128_ALBUM_GAIN",
	}
	srcBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.ReplayGainTrackGain, "-6.5 dB")
		e.Set(tag.ReplayGainTrackPeak, "0.9")
		e.Set(tag.ReplayGainAlbumGain, "-7.0 dB")
		e.Set(tag.ReplayGainAlbumPeak, "0.95")
		e.Set(tag.EncodedBy, "Src Person")
		e.Set(tag.EncodingHistory, "A=PCM; A=FLAC")
		e.Set(tag.AcoustIDFingerprint, "AQAAfingerprint")
		e.Set(tag.Encoder, "SourceEnc 1.0")
		e.Set("R128_TRACK_GAIN", "-896")
		e.Set("R128_ALBUM_GAIN", "-512")
		e.Set(tag.Title, "Shared Title")
		e.Set(tag.AcoustID, "recording-xyz") // recording ID is portable
	})
	src := mustParseBytes(t, srcBytes)

	dstBytes := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Encoder, "DestEnc 9.9")
	})

	plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}

	disp := map[tag.Key]wl.Disposition{}
	for _, it := range report.Items {
		if it.Kind == wl.TransferField {
			disp[it.Key] = it.Disposition
		}
	}
	for _, k := range ownAudio {
		if disp[k] != wl.Excluded {
			t.Errorf("%s disposition = %s, want excluded", k, disp[k])
		}
	}
	if disp[tag.AcoustID] == wl.Excluded {
		t.Error("ACOUSTID_ID is recording identity and should transfer, not be excluded")
	}
	if !report.Lossless() {
		t.Error("excluding own-audio keys should not make the copy lossy")
	}

	// The destination keeps its own ENCODER; the work metadata (title + recording ID) carries.
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	if v, _ := result.Get(tag.Encoder); len(v) != 1 || v[0] != "DestEnc 9.9" {
		t.Errorf("destination ENCODER = %v, want [DestEnc 9.9] (untouched)", v)
	}
	if v, _ := result.Get(tag.Title); len(v) != 1 || v[0] != "Shared Title" {
		t.Errorf("destination TITLE = %v, want [Shared Title] (carried)", v)
	}
	if v, _ := result.Get(tag.AcoustID); len(v) != 1 || v[0] != "recording-xyz" {
		t.Errorf("destination ACOUSTID_ID = %v, want [recording-xyz] (carried)", v)
	}
}

// transferItemFor returns the report's item for key.
func transferItemFor(t *testing.T, r wl.TransferReport, key tag.Key) wl.TransferItem {
	t.Helper()
	for _, it := range r.Items {
		if it.Key == key {
			return it
		}
	}
	t.Fatalf("no transfer item for %s in %+v", key, r.Items)
	return wl.TransferItem{}
}

// A comment's and a lyric's description and language are carried where the destination stores
// them and graded lossy, naming what goes, where it does not. The eng every ID3 comment holds is
// no loss: an ID3 writer fills it in.
func TestTransferCarriesCommentAndLyricsQualifiers(t *testing.T) {
	src := mustParseBytes(t, describedMP3(t))
	wantComment := wl.Comment{Text: "hello", Description: "My Desc", Language: "eng"}
	wantLyrics := wl.Lyrics{Text: "la la", Description: "Lyr Desc", Language: "deu"}
	cases := []struct {
		dst                         string
		commentDisp, lyricsDisp     wl.Disposition
		commentReason, lyricsReason string
		comment                     wl.Comment
		lyrics                      wl.Lyrics
	}{
		{notagsMP3, wl.Carried, wl.Carried, "", "", wantComment, wantLyrics},
		{notagsFLAC, wl.Lossy, wl.Lossy, "description not carried", "description and language not carried",
			wl.Comment{Text: "hello"}, wl.Lyrics{Text: "la la"}},
		{notagsMKA, wl.Lossy, wl.Lossy, "description not carried", "description not carried",
			wl.Comment{Text: "hello", Language: "eng"}, wl.Lyrics{Text: "la la", Language: "deu"}},
	}
	for _, c := range cases {
		t.Run(filepath.Base(c.dst), func(t *testing.T) {
			data := readFixture(t, c.dst)
			dst := mustParseBytes(t, data)
			plan, report, err := src.PrepareTransfer(dst)
			if err != nil {
				t.Fatal(err)
			}
			comment, lyrics := transferItemFor(t, report, tag.Comment), transferItemFor(t, report, tag.Lyrics)
			if comment.Kind != wl.TransferField || comment.Disposition != c.commentDisp || comment.Reason != c.commentReason {
				t.Errorf("COMMENT graded %+v, want field %s %q", comment, c.commentDisp, c.commentReason)
			}
			if lyrics.Kind != wl.TransferField || lyrics.Disposition != c.lyricsDisp || lyrics.Reason != c.lyricsReason {
				t.Errorf("LYRICS graded %+v, want field %s %q", lyrics, c.lyricsDisp, c.lyricsReason)
			}
			want := map[wl.Disposition]int{}
			want[c.commentDisp]++
			want[c.lyricsDisp]++
			if carried, lossy, dropped := report.Counts(); carried != want[wl.Carried] || lossy != want[wl.Lossy] || dropped != 0 {
				t.Errorf("counts = %d carried, %d lossy, %d dropped, want %v", carried, lossy, dropped, want)
			}
			planned, err := src.PlanTransfer(dst.Format())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(planned.Items, report.Items) {
				t.Errorf("PlanTransfer items = %+v, PrepareTransfer items = %+v", planned.Items, report.Items)
			}
			out := mustParseBytes(t, applyToBytes(t, data, plan))
			if got := out.Comments(); !slices.Equal(got, []wl.Comment{c.comment}) {
				t.Errorf("Comments() = %+v, want %+v", got, c.comment)
			}
			if got := out.Lyrics(); !slices.Equal(got, []wl.Lyrics{c.lyrics}) {
				t.Errorf("Lyrics() = %+v, want %+v", got, c.lyrics)
			}
		})
	}
}

// A source language is written as it is, not reset to the eng a new comment defaults to.
func TestTransferKeepsTheSourceCommentLanguage(t *testing.T) {
	src := mustParseBytes(t, mp3WithFrames(t, commFrame(4, "deu", "", "Kommentar")))
	data := readFixture(t, notagsMP3)
	plan, _, err := src.PrepareTransfer(mustParseBytes(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(applyToBytes(t, data, plan), commFields("deu", "", "Kommentar")) {
		t.Error("the carried comment lost its language")
	}
}

// An ID3 comment in the default language copies losslessly onto a format with no language field,
// as it did before comments carried one; another language is a loss there.
func TestTransferOfTheDefaultLanguageIsLossless(t *testing.T) {
	for _, dst := range []string{notagsFLAC, "../testdata/notags.ogg", "../testdata/notags.m4a", "../testdata/notags.wv"} {
		data := readFixture(t, dst)
		src := mustParseBytes(t, mp3WithFrames(t, commFrame(4, "eng", "", "hello"), usltFrame(4, "eng", "", "words")))
		if _, report, err := src.PrepareTransfer(mustParseBytes(t, data)); err != nil || !report.Lossless() {
			t.Errorf("%s: err %v, report %+v, want lossless", filepath.Base(dst), err, report.Items)
		}
		german := mustParseBytes(t, mp3WithFrames(t, commFrame(4, "deu", "", "hallo")))
		_, report, err := german.PrepareTransfer(mustParseBytes(t, data))
		if err != nil {
			t.Fatal(err)
		}
		if it := transferItemFor(t, report, tag.Comment); it.Disposition != wl.Lossy || it.Reason != "language not carried" {
			t.Errorf("%s: a German comment graded %+v, want lossy", filepath.Base(dst), it)
		}
	}
}

// A copy leaves a destination comment of the same text what the source's format cannot say of
// it: a FLAC source changes nothing, a Matroska source sets the language alone, and an ID3
// source, which holds both qualifiers, sets both.
func TestTransferKeepsDestinationQualifiersTheSourceCannotExpress(t *testing.T) {
	data := mp3WithFrames(t, commFrame(4, "ger", "Ripped by EAC", "same"))
	kept := wl.Comment{Text: "same", Description: "Ripped by EAC", Language: "ger"}
	cases := []struct {
		name string
		src  []byte
		want wl.Comment
		noOp bool
	}{
		{"flac", testfile.TaggedFLAC("ref", "COMMENT=same"), kept, true},
		{"matroska", testfile.Matroska("matroska", "", testfile.El(testfile.IDTags, testfile.TagGroup(0, langSimpleTag("COMMENT", "fra", "same")))),
			wl.Comment{Text: "same", Description: "Ripped by EAC", Language: "fra"}, false},
		{"id3", mp3WithFrames(t, commFrame(4, "fra", "", "same")), wl.Comment{Text: "same", Language: "fra"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, report, err := mustParseBytes(t, c.src).PrepareTransfer(mustParseBytes(t, data))
			if err != nil || !report.Lossless() {
				t.Fatalf("transfer: %v, report %+v", err, report.Items)
			}
			if plan.IsNoOp() != c.noOp {
				t.Errorf("no-op = %v, want %v", plan.IsNoOp(), c.noOp)
			}
			if got := mustParseBytes(t, applyToBytes(t, data, plan)).Comments(); !slices.Equal(got, []wl.Comment{c.want}) {
				t.Errorf("Comments() = %+v, want %+v", got, c.want)
			}
		})
	}
}

// The report a transfer prepares is what the write does: over sources whose values a writer stores
// in another shape, onto every destination a transfer can write.
func TestTransferReportMatchesWrittenResult(t *testing.T) {
	forEachTransfer(t, transferDestinations, func(t *testing.T, s transferSource, srcBytes, dstBytes []byte) {
		src, before := mustParseBytes(t, srcBytes), mustParseBytes(t, dstBytes)
		plan, report, err := src.PrepareTransfer(before, s.opts...)
		if err != nil {
			t.Fatalf("PrepareTransfer: %v\nreport: %+v", err, report.Items)
		}
		after := before
		if !plan.IsNoOp() {
			after = mustParseBytes(t, applyToBytes(t, dstBytes, plan))
		}
		assertTransferMatchesResult(t, src, before, after, plan, report)
	})
}

// A cover or a value past what the destination's store holds is dropped from the copy with the
// limit named, and the rest is written. Items that fit alone and overflow together still refuse
// the write.
func TestTransferDropsOversizeItems(t *testing.T) {
	bigCover := append(testfile.PNG(), make([]byte, 17<<20)...)
	coverSrc := mustParseBytes(t, writeBack(t, notagsMP3, func(e *wl.Editor) {
		e.Set(tag.Title, "Big Cover")
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: bigCover})
		e.AddPicture(wl.Picture{Type: wl.PicBackCover, Data: testfile.JPEG()})
	}))
	for _, c := range []struct{ dst, reason string }{
		{"notags.flac", "the picture is 17.0 MiB, over the 16.0 MiB a FLAC metadata block holds"},
		{"notags.oga", "the picture is 17.0 MiB, over the 16.0 MiB a FLAC metadata block holds"},
		{"notags.wv", "the picture is 17.0 MiB, over the 16.0 MiB an APEv2 tag holds"},
	} {
		t.Run(c.dst, func(t *testing.T) {
			dstBytes := readFixture(t, "../testdata/"+c.dst)
			plan, report, err := coverSrc.PrepareTransfer(mustParseBytes(t, dstBytes))
			if err != nil {
				t.Fatalf("PrepareTransfer: %v", err)
			}
			if carried, lossy, dropped := pictureCounts(report); carried != 1 || lossy != 0 || dropped != 1 {
				t.Errorf("pictures: %d carried, %d lossy, %d dropped, want the small one carried and the large one dropped\n%+v", carried, lossy, dropped, report.Items)
			}
			for _, it := range report.Items {
				if it.Kind == wl.TransferPicture && it.Disposition == wl.Dropped && it.Reason != c.reason {
					t.Errorf("reason %q, want %q", it.Reason, c.reason)
				}
			}
			out := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
			if got, _ := out.Get(tag.Title); !slices.Equal(got, []string{"Big Cover"}) {
				t.Errorf("TITLE = %q, want the rest of the copy written", got)
			}
			if pics := out.Pictures(); len(pics) != 1 || pics[0].Type != wl.PicBackCover {
				t.Errorf("destination holds %d pictures, want the one that fits", len(pics))
			}
		})
	}

	valueSrc := mustParseBytes(t, writeBack(t, "../testdata/notags.ogg", func(e *wl.Editor) {
		e.Set(tag.Title, "Big Comment").Set(tag.Comment, strings.Repeat("c", 17<<20))
	}))
	for _, c := range []struct{ dst, reason string }{
		{"notags.flac", "a value is 17.0 MiB, over the 16.0 MiB a FLAC metadata block holds"},
		{"notags.wv", "a value is 17.0 MiB, over the 16.0 MiB an APEv2 tag holds"},
	} {
		t.Run("value/"+c.dst, func(t *testing.T) {
			dstBytes := readFixture(t, "../testdata/"+c.dst)
			plan, report, err := valueSrc.PrepareTransfer(mustParseBytes(t, dstBytes))
			if err != nil {
				t.Fatalf("PrepareTransfer: %v", err)
			}
			if it := transferItemFor(t, report, tag.Comment); it.Disposition != wl.Dropped || it.Reason != c.reason {
				t.Errorf("COMMENT = %s (%s), want dropped with the limit named", it.Disposition, it.Reason)
			}
			out := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
			if got, _ := out.Get(tag.Title); !slices.Equal(got, []string{"Big Comment"}) {
				t.Errorf("TITLE = %q, want the rest of the copy written", got)
			}
			if out.Tags().Has(tag.Comment) {
				t.Error("the dropped COMMENT was written")
			}
		})
	}

	t.Run("together", func(t *testing.T) {
		src := mustParseBytes(t, writeBack(t, "../testdata/notags.ogg", func(e *wl.Editor) {
			e.Set(tag.Comment, strings.Repeat("c", 9<<20)).Set(tag.Lyrics, strings.Repeat("l", 9<<20))
		}))
		_, report, err := src.PrepareTransfer(mustParseBytes(t, readFixture(t, "../testdata/notags.flac")))
		if !errors.Is(err, waxerr.ErrContentTooLarge) {
			t.Fatalf("err = %v, want content too large: neither value is over the limit alone", err)
		}
		if !report.Lossless() {
			t.Errorf("report = %+v, want both values graded as fitting", report.Items)
		}
	})
}

// A link picture holds a URL, not an image. An APEv2 cover item has no way to say so, so the
// formats that store pictures there drop it instead of writing the URL as a cover.
func TestTransferDropsLinkPictureOntoAPE(t *testing.T) {
	src := mustParseBytes(t, linkPictureFLAC("the cover"))
	for _, d := range []string{"notags.wv", "notags.ape", "chapters.mpc"} {
		dstBytes := readFixture(t, "../testdata/"+d)
		plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		carried, lossy, dropped := pictureCounts(report)
		if carried != 0 || lossy != 0 || dropped != 1 {
			t.Errorf("%s: pictures %d carried, %d lossy, %d dropped, want the link dropped", d, carried, lossy, dropped)
		}
		for _, it := range report.Items {
			if want := mustParseBytes(t, dstBytes).Format().String() + " cannot store a linked (URL) picture"; it.Kind == wl.TransferPicture && it.Reason != want {
				t.Errorf("%s: reason %q, want %q", d, it.Reason, want)
			}
		}
		if out := mustParseBytes(t, applyToBytes(t, dstBytes, plan)); len(out.Pictures()) != 0 {
			t.Errorf("%s: %d pictures written, want none", d, len(out.Pictures()))
		} else if got, _ := out.Get(tag.Title); !slices.Equal(got, []string{"Linked"}) {
			t.Errorf("%s: TITLE = %q, want the rest of the copy written", d, got)
		}
	}
	// A direct edit refuses the link the same way a transfer drops it.
	doc := mustParseBytes(t, readFixture(t, "../testdata/notags.wv"))
	link := src.Pictures()[0]
	if _, err := doc.Edit().AddPicture(link).Prepare(wl.WithUnrecognizedPictures()); !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("adding a link picture to WavPack: err = %v, want an unsupported-tag refusal", err)
	}
}

// A size a FLAC picture block states for an image whose bytes give none (AVIF here) has no field
// in ID3: the copy is lossy for it, and a repeat finds the picture there and writes nothing.
func TestTransferOfADeclaredPictureSizeSettles(t *testing.T) {
	avif := []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf")
	block := slices.Concat(
		testfile.BE32(3), testfile.BE32(len("image/avif")), []byte("image/avif"), testfile.BE32(0),
		testfile.BE32(1920), testfile.BE32(1080), testfile.BE32(24), testfile.BE32(0),
		testfile.BE32(len(avif)), avif)
	src := mustParseBytes(t, testfile.FLAC(testfile.CommentBlock("v", "TITLE=T"), testfile.FLACBlock(6, block)))
	if pics := src.Pictures(); len(pics) != 1 || pics[0].Width != 1920 || pics[0].MIME != "image/avif" {
		t.Fatalf("setup: source pictures = %+v", pics)
	}
	dstBytes := readFixture(t, notagsMP3)
	plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatal(err)
	}
	if carried, lossy, dropped := pictureCounts(report); carried != 0 || lossy != 1 || dropped != 0 {
		t.Errorf("first copy: pictures %d carried, %d lossy, %d dropped, want lossy for the size", carried, lossy, dropped)
	}
	written := applyToBytes(t, dstBytes, plan)
	dst := mustParseBytes(t, written)
	if pics := dst.Pictures(); len(pics) != 1 || pics[0].Width != 0 || !bytes.Equal(pics[0].Data, avif) {
		t.Fatalf("written pictures = %+v, want the image without a size", pics)
	}
	plan, report, err = src.PrepareTransfer(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Errorf("a repeat copy plans a write: %+v", plan.Report().Operations)
	}
	if _, lossy, _ := pictureCounts(report); lossy != 1 {
		t.Errorf("repeat copy: %+v, want the picture still lossy for its size", report.Items)
	}
}

// Where the format has fields for a picture's size, the size a link picture declares is carried.
func TestTransferKeepsADeclaredPictureSizeWhereTheFormatHoldsIt(t *testing.T) {
	src := mustParseBytes(t, linkPictureFLAC(""))
	for _, d := range []string{"notags.flac", "notags.ogg", "notags.opus", "notags.oga"} {
		dstBytes := readFixture(t, "../testdata/"+d)
		plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		if carried, lossy, dropped := pictureCounts(report); carried != 1 || lossy != 0 || dropped != 0 {
			t.Errorf("%s: pictures %d carried, %d lossy, %d dropped, want the link carried", d, carried, lossy, dropped)
		}
		pics := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Pictures()
		if len(pics) != 1 || pics[0].Width != 1 || pics[0].Height != 1 || pics[0].Depth != 24 || pics[0].MIME != wl.LinkMIME {
			t.Errorf("%s: written pictures = %+v, want the link with its 1x1, 24-bit size", d, pics)
		}
	}
}

// A language longer than the three bytes an ID3 frame holds is cut by the write, so the comment
// is lossy though its text is carried, and the format-only simulation says the same.
func TestTransferGradesALanguageCutToTheFrame(t *testing.T) {
	src := mustParseBytes(t, writeBack(t, "../testdata/notags.mka", func(e *wl.Editor) {
		e.SetComments(wl.Comment{Text: "d", Language: "en-us"})
	}))
	if c := src.Comments(); len(c) != 1 || c[0].Language != "en-us" {
		t.Skipf("setup: Matroska reads the comment as %+v, not in en-us", c)
	}
	for _, d := range []string{"notags.mp3", "notags.wav", "notags.aiff", "notags.aac"} {
		dstBytes := readFixture(t, "../testdata/"+d)
		dst := mustParseBytes(t, dstBytes)
		plan, report, err := src.PrepareTransfer(dst)
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		if it := transferItemFor(t, report, tag.Comment); it.Disposition != wl.Lossy || it.Reason != `language "en-us" reads back as "en-"` {
			t.Errorf("%s: COMMENT = %s (%s), want lossy for the cut language", d, it.Disposition, it.Reason)
		}
		if got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Comments(); len(got) != 1 || got[0].Language != "en-" {
			t.Errorf("%s: written comment = %+v, want the language cut to en-", d, got)
		}
		assertPlanMatchesPrepare(t, src.Transfer(), dst)
	}
}

// An MP4 whose tags live in a QuickTime keys store, alone or beside an ilst, stores what an
// iTunes ilst has no form for, an empty number and a name the ilst would read as another key:
// such a destination is graded from its own write, not as an ilst.
func TestTransferOntoAKeysStoreKeepsWhatAnIlstCannot(t *testing.T) {
	src := mustParseBytes(t, testfile.TaggedFLAC("v", "TRACKNUMBER=", "MUSICBRAINZ ALBUM ID=a", "TITLE=T"))
	udta := testfile.MP4Atom("udta", testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "U"))))
	for name, dstBytes := range map[string][]byte{
		"a keys store":                mp4MoovMetaFile(mp4MdtaMeta([]string{"title", "track"}, []string{"Meta Title", "2"})),
		"an ilst beside a keys store": mp4MoovMetaFile(udta, mp4MdtaMeta([]string{"title", "track"}, []string{"M", "2"})),
	} {
		plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if it := transferItemFor(t, report, tag.TrackNumber); it.Disposition != wl.Carried {
			t.Errorf("%s: an empty TRACKNUMBER = %s (%s), want carried", name, it.Disposition, it.Reason)
		}
		after := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
		if got, _ := after.Get(tag.TrackNumber); !slices.Equal(got, []string{""}) {
			t.Errorf("%s: TRACKNUMBER reads back %q, want the empty value", name, got)
		}
	}
	// On the keys store alone the custom name is its own key.
	keys := mp4MoovMetaFile(mp4MdtaMeta([]string{"title"}, []string{"Meta Title"}))
	_, report, err := src.PrepareTransfer(mustParseBytes(t, keys))
	if err != nil {
		t.Fatal(err)
	}
	if it := transferItemFor(t, report, "MUSICBRAINZ ALBUM ID"); it.Disposition != wl.Carried {
		t.Errorf("a custom name on a keys store = %s (%s), want carried", it.Disposition, it.Reason)
	}
}
