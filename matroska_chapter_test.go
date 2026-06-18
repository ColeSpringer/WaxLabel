package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// Chapter synth helpers; element IDs live in the shared block in matroska_test.go.

// mkAtom builds a ChapterAtom with a UID, start/end (ns), and an optional title.
func mkAtom(uid, startNs, endNs uint64, title string) []byte {
	body := concat(mkUint(idChapterUID, uid), mkUint(idChapTimeStart, startNs))
	if endNs > 0 {
		body = concat(body, mkUint(idChapTimeEnd, endNs))
	}
	if title != "" {
		body = concat(body, mkEl(idChapDisplay, mkStr(idChapString, title)))
	}
	return mkEl(idChapterAtom, body)
}

// mkEdition builds an EditionEntry, optionally marked default, with a prefix
// (e.g. an EditionUID) and the given atoms.
func mkEdition(def bool, prefix []byte, atoms ...[]byte) []byte {
	var body []byte
	body = append(body, prefix...)
	if def {
		body = append(body, mkUint(idEditionFlagDf, 1)...)
	}
	body = append(body, concat(atoms...)...)
	return mkEl(idEditionEntry, body)
}

// buildMatroskaCh assembles a minimal file with an Info title, a Chapters element,
// and optional Tags. It delegates to buildMatroska (which appends its tags arg
// after Info), passing chapters++tags so the envelope lives in one place.
func buildMatroskaCh(docType, title string, chapters, tags []byte) []byte {
	return buildMatroska(docType, title, concat(chapters, tags))
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// TestMatroskaReadChapters reads the committed real-ffmpeg chapter fixture: three
// chapters with absolute-nanosecond Start/End and ChapterDisplay titles.
func TestMatroskaReadChapters(t *testing.T) {
	doc := mustParseFile(t, chaptersMKA)
	chs := doc.Chapters()
	if len(chs) != 3 {
		t.Fatalf("got %d chapters, want 3: %+v", len(chs), chs)
	}
	want := []struct {
		start, end time.Duration
		title      string
	}{
		{0, ms(200), "Intro"},
		{ms(200), ms(400), "Middle"},
		{ms(400), ms(600), "Finale"},
	}
	for i, w := range want {
		if chs[i].Start != w.start || chs[i].End != w.end || chs[i].Title != w.title {
			t.Errorf("chapter %d = {%v,%v,%q}, want {%v,%v,%q}",
				i, chs[i].Start, chs[i].End, chs[i].Title, w.start, w.end, w.title)
		}
	}
	// The native view notes the chapters.
	found := false
	for _, e := range doc.Native().Describe() {
		if e.Kind == "Chapters" {
			found = true
		}
	}
	if !found {
		t.Error("native view should note the Chapters element")
	}
}

// TestMatroskaChapterRoundTripFixture edits a title on the real fixture and
// confirms the chapters reparse identically (absorption path on a file with a Void)
// and the cluster essence is untouched.
func TestMatroskaChapterRoundTripFixture(t *testing.T) {
	src := readFixture(t, chaptersMKA)
	newChaps := []wl.Chapter{
		{Start: 0, End: ms(150), Title: "Opening"},
		{Start: ms(150), End: ms(450), Title: "Body"},
		{Start: ms(450), End: ms(600), Title: "Closing"},
	}
	out, outDoc := saveMatroska(t, src, mustParseBytes(t, src).Edit().SetChapters(newChaps...))

	if !equalChapterLists(outDoc.Chapters(), newChaps) {
		t.Errorf("returned doc chapters = %+v, want %+v", outDoc.Chapters(), newChaps)
	}
	re := mustParseBytes(t, out)
	if !equalChapterLists(re.Chapters(), newChaps) {
		t.Errorf("reparsed chapters = %+v, want %+v", re.Chapters(), newChaps)
	}
	// The result document equals a fresh parse.
	if !equalChapterLists(outDoc.Chapters(), re.Chapters()) {
		t.Errorf("result %+v != reparse %+v", outDoc.Chapters(), re.Chapters())
	}
	essenceUnchanged(t, src, out)
	// Other metadata survives the chapter edit.
	if re.Fields().Title != "Chapter Sample" {
		t.Errorf("Info.Title lost: %q", re.Fields().Title)
	}
}

// TestMatroskaChapterCRCsValid edits chapters across both write paths and confirms
// every CRC-32 in the output - including the re-rendered Chapters master - is
// recomputed correctly (the integrity check a strict reader performs).
func TestMatroskaChapterCRCsValid(t *testing.T) {
	src := readFixture(t, chaptersMKA)
	for _, e := range []*wl.Editor{
		mustParseBytes(t, src).Edit().SetChapters(wl.Chapter{Start: 0, End: ms(600), Title: "One"}), // smaller: absorb
		mustParseBytes(t, src).Edit().SetChapters( // larger: shift
			wl.Chapter{Start: 0, End: ms(200), Title: "A Considerably Longer Chapter Title One"},
			wl.Chapter{Start: ms(200), End: ms(400), Title: "A Considerably Longer Chapter Title Two"},
			wl.Chapter{Start: ms(400), End: ms(600), Title: "A Considerably Longer Chapter Title Three"},
		),
	} {
		out, _ := saveMatroska(t, src, e)
		checkCRCs(t, out, 0, len(out), 0)
	}
}

// TestMatroskaChapterDifferentialFFprobe writes chapters and confirms ffprobe -
// the authority - reads them back with their End times (proving the Chapters tree
// is valid) while the FLAC audio stream stays intact.
func TestMatroskaChapterDifferentialFFprobe(t *testing.T) {
	requireTool(t, "ffprobe")
	path := copyToTemp(t, chaptersMKA)
	plan, err := mustParseFile(t, path).Edit().SetChapters(
		wl.Chapter{Start: 0, End: ms(250), Title: "Differential One"},
		wl.Chapter{Start: ms(250), End: ms(600), Title: "Differential Two"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_chapters", "-show_entries", "stream=codec_name", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var probe struct {
		Streams []struct {
			Codec string `json:"codec_name"`
		} `json:"streams"`
		Chapters []struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
			Tags  struct {
				Title string `json:"title"`
			} `json:"tags"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("parse ffprobe json: %v\n%s", err, out)
	}
	if len(probe.Chapters) != 2 {
		t.Fatalf("ffprobe read %d chapters, want 2: %s", len(probe.Chapters), out)
	}
	// ffprobe reports chapter times in nanoseconds (time_base 1/1e9).
	if probe.Chapters[0].Start != 0 || probe.Chapters[0].End != int64(ms(250)) ||
		probe.Chapters[0].Tags.Title != "Differential One" {
		t.Errorf("chapter 0 = %+v", probe.Chapters[0])
	}
	if probe.Chapters[1].End != int64(ms(600)) || probe.Chapters[1].Tags.Title != "Differential Two" {
		t.Errorf("chapter 1 = %+v", probe.Chapters[1])
	}
	if len(probe.Streams) == 0 || probe.Streams[0].Codec != "flac" {
		t.Errorf("audio stream not intact: %+v", probe.Streams)
	}
}

// TestMatroskaChapterCreate adds chapters to a file that had none (sample.mka),
// confirming the new Chapters element round-trips and the audio is untouched.
func TestMatroskaChapterCreate(t *testing.T) {
	src := readFixture(t, sampleMKA)
	if len(mustParseBytes(t, src).Chapters()) != 0 {
		t.Skip("sample.mka unexpectedly has chapters")
	}
	chs := []wl.Chapter{
		{Start: 0, End: time.Second, Title: "Part One"},
		{Start: time.Second, Title: "Part Two"}, // open end (last chapter)
	}
	out, outDoc := saveMatroska(t, src, mustParseBytes(t, src).Edit().SetChapters(chs...))

	if !equalChapterLists(outDoc.Chapters(), chs) {
		t.Errorf("result chapters = %+v, want %+v", outDoc.Chapters(), chs)
	}
	re := mustParseBytes(t, out)
	if !equalChapterLists(re.Chapters(), chs) {
		t.Errorf("reparsed chapters = %+v, want %+v", re.Chapters(), chs)
	}
	if n := countSegChildren(t, out, idChapters); n != 1 {
		t.Errorf("output has %d Chapters elements, want 1", n)
	}
	essenceUnchanged(t, src, out)
	// The existing tags survive the chapter create.
	if re.Fields().Album != "Sample Album" {
		t.Errorf("existing tag lost on chapter create: Album=%q", re.Fields().Album)
	}
}

// TestMatroskaChapterClear removes all chapters, dropping the Chapters element.
func TestMatroskaChapterClear(t *testing.T) {
	src := readFixture(t, chaptersMKA)
	out, outDoc := saveMatroska(t, src, mustParseBytes(t, src).Edit().ClearChapters())

	if len(outDoc.Chapters()) != 0 {
		t.Errorf("result still has %d chapters after clear", len(outDoc.Chapters()))
	}
	re := mustParseBytes(t, out)
	if len(re.Chapters()) != 0 {
		t.Errorf("reparsed still has %d chapters after clear", len(re.Chapters()))
	}
	if n := countSegChildren(t, out, idChapters); n != 0 {
		t.Errorf("Chapters element not dropped: %d remain", n)
	}
	essenceUnchanged(t, src, out)
	// The title (a different element) is untouched by the chapter clear.
	if re.Fields().Title != "Chapter Sample" {
		t.Errorf("title lost on chapter clear: %q", re.Fields().Title)
	}
}

// TestMatroskaChapterNoOp: re-setting the identical chapter list writes nothing.
func TestMatroskaChapterNoOp(t *testing.T) {
	src := readFixture(t, chaptersMKA)
	cur := mustParseBytes(t, src).Chapters()
	plan, err := mustParseBytes(t, src).Edit().SetChapters(cur...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Report().NoOp {
		t.Errorf("re-setting identical chapters should be a no-op, ops=%v", plan.Report().Operations)
	}
}

// TestMatroskaChapterWebMAllowed: unlike cover attachments, the Chapters element is
// in the WebM subset, so a chapter write to a .webm is allowed (not refused).
func TestMatroskaChapterWebMAllowed(t *testing.T) {
	src := readFixture(t, sampleWebM)
	chs := []wl.Chapter{{Start: 0, End: time.Second, Title: "WebM Chapter"}}
	out, outDoc := saveMatroska(t, src, mustParseBytes(t, src).Edit().SetChapters(chs...))
	if !equalChapterLists(outDoc.Chapters(), chs) {
		t.Errorf("result chapters = %+v, want %+v", outDoc.Chapters(), chs)
	}
	if !equalChapterLists(mustParseBytes(t, out).Chapters(), chs) {
		t.Errorf("reparsed webm chapters = %+v, want %+v", mustParseBytes(t, out).Chapters(), chs)
	}
	essenceUnchanged(t, src, out)
}

// TestMatroskaChapterUIDsPreserved: a chapter edit reuses each chapter's original
// ChapterUID by position, so chapter-scoped SimpleTags that reference a UID stay
// valid. The synth file has no Void, so this also exercises the shift path.
func TestMatroskaChapterUIDsPreserved(t *testing.T) {
	chapters := mkEl(idChapters, mkEdition(true, nil,
		mkAtom(42, 0, uint64(ms(200)), "First"),
		mkAtom(99, uint64(ms(200)), uint64(ms(400)), "Second"),
	))
	// A chapter-scoped tag referencing ChapterUID 42 (preserved verbatim).
	tags := mkEl(idTags, mkEl(idTag, concat(
		mkEl(idTargets, concat(mkUint(idTgtTypeVal, 30), mkUint(idTagChapUID, 42))),
		mkSimple("ARTIST", "Chapter Performer"))))
	data := buildMatroskaCh("matroska", "T", chapters, tags)

	if got := mustParseBytes(t, data).Chapters(); len(got) != 2 || got[0].Title != "First" {
		t.Fatalf("setup parse wrong: %+v", got)
	}
	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, End: ms(200), Title: "First Renamed"},
		wl.Chapter{Start: ms(200), End: ms(400), Title: "Second Renamed"},
	))

	// Both original UIDs reappear, minimal-width encoded (ChapterUID 0x73C4):
	// 42 -> 73 C4 81 2A, 99 -> 73 C4 81 63.
	if !bytes.Contains(out, []byte{0x73, 0xC4, 0x81, 0x2A}) {
		t.Error("ChapterUID 42 not preserved across the edit")
	}
	if !bytes.Contains(out, []byte{0x73, 0xC4, 0x81, 0x63}) {
		t.Error("ChapterUID 99 not preserved across the edit")
	}
	re := mustParseBytes(t, out)
	if got := re.Chapters(); len(got) != 2 || got[0].Title != "First Renamed" {
		t.Errorf("titles not edited: %+v", got)
	}
}

// TestMatroskaMultiEditionPreserved: only the default edition is re-rendered on an
// edit; a non-default edition is preserved verbatim.
func TestMatroskaMultiEditionPreserved(t *testing.T) {
	chapters := mkEl(idChapters, concat(
		mkEdition(true, nil, mkAtom(1, 0, uint64(ms(300)), "DefaultChap")),
		mkEdition(false, mkUint(idEditionUID, 7777), mkAtom(2, 0, uint64(ms(300)), "NonDefaultChap")),
	))
	data := buildMatroskaCh("matroska", "T", chapters, nil)

	// The projected chapters come from the default edition only.
	if got := mustParseBytes(t, data).Chapters(); len(got) != 1 || got[0].Title != "DefaultChap" {
		t.Fatalf("default-edition projection wrong: %+v", got)
	}
	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, End: ms(300), Title: "EditedDefault"}))

	// The non-default edition (and its UID) survive verbatim.
	if !bytes.Contains(out, []byte("NonDefaultChap")) {
		t.Error("non-default edition not preserved across the edit")
	}
	re := mustParseBytes(t, out)
	if got := re.Chapters(); len(got) != 1 || got[0].Title != "EditedDefault" {
		t.Errorf("default edition not edited correctly: %+v", got)
	}
}

// TestMatroskaChapterClearMultiEdition: clearing chapters on a multi-edition file
// removes the whole Chapters element - it must not drop only the default edition
// and silently promote a previously-hidden non-default edition into view.
func TestMatroskaChapterClearMultiEdition(t *testing.T) {
	chapters := mkEl(idChapters, concat(
		mkEdition(true, nil, mkAtom(1, 0, uint64(ms(300)), "DefaultChap")),
		mkEdition(false, mkUint(idEditionUID, 7777), mkAtom(2, 0, uint64(ms(300)), "HiddenChap")),
	))
	data := buildMatroskaCh("matroska", "T", chapters, nil)
	out, outDoc := saveMatroska(t, data, mustParseBytes(t, data).Edit().ClearChapters())

	if len(outDoc.Chapters()) != 0 {
		t.Errorf("result still has %d chapters after clear: %+v", len(outDoc.Chapters()), outDoc.Chapters())
	}
	if got := mustParseBytes(t, out).Chapters(); len(got) != 0 {
		t.Errorf("reparsed still has %d chapters after clear: %+v", len(got), got)
	}
	if n := countSegChildren(t, out, idChapters); n != 0 {
		t.Errorf("Chapters element not dropped on a multi-edition clear: %d remain", n)
	}
	if bytes.Contains(out, []byte("HiddenChap")) {
		t.Error("a hidden non-default edition survived a clear")
	}
}

// TestMatroskaChapterOutOfOrderSorted: a file whose ChapterAtoms are stored out of
// start order projects sorted by start (so re-setting it is a no-op) and keeps each
// chapter aligned with its own ChapterUID on a re-render.
func TestMatroskaChapterOutOfOrderSorted(t *testing.T) {
	chapters := mkEl(idChapters, mkEdition(true, nil,
		mkAtom(200, uint64(ms(200)), uint64(ms(400)), "Second"), // stored first, starts later
		mkAtom(100, 0, uint64(ms(200)), "First"),                // stored second, starts at 0
	))
	data := buildMatroskaCh("matroska", "T", chapters, nil)

	doc := mustParseBytes(t, data)
	chs := doc.Chapters()
	if len(chs) != 2 || chs[0].Title != "First" || chs[1].Title != "Second" {
		t.Fatalf("projection not sorted by start: %+v", chs)
	}
	// Re-setting the (already-sorted) projection is a true no-op.
	plan, err := doc.Edit().SetChapters(chs...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Report().NoOp {
		t.Errorf("SetChapters(doc.Chapters()...) on an out-of-order file should be a no-op, ops=%v", plan.Report().Operations)
	}
	// A real edit keeps each chapter's own UID - the start=0 chapter must reuse UID
	// 100 (0x64) and the later one UID 200 (0xC8), not the swapped file-order pair.
	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, End: ms(200), Title: "First Renamed"},
		wl.Chapter{Start: ms(200), End: ms(400), Title: "Second Renamed"},
	))
	if !bytes.Contains(out, []byte{0x73, 0xC4, 0x81, 0x64}) {
		t.Error("the start=0 chapter lost its UID 100 (UID/chapter misaligned after sort)")
	}
	if !bytes.Contains(out, []byte{0x73, 0xC4, 0x81, 0xC8}) {
		t.Error("the later chapter lost its UID 200")
	}
}

// TestMatroskaChapterFlattenWarns: editing a default edition that carries a nested
// sub-chapter or a second (other-language) display drops that structure, surfaced
// as a plan-time WarnChaptersFlattened. A clear (a removal, not a flatten) does not.
func TestMatroskaChapterFlattenWarns(t *testing.T) {
	nested := mkEl(idChapterAtom, concat(
		mkUint(idChapterUID, 1), mkUint(idChapTimeStart, 0),
		mkEl(idChapDisplay, mkStr(idChapString, "Top")),
		mkEl(idChapterAtom, concat( // a nested sub-chapter the flat model cannot hold
			mkUint(idChapterUID, 9), mkUint(idChapTimeStart, uint64(ms(100))),
			mkEl(idChapDisplay, mkStr(idChapString, "Sub")))),
	))
	data := buildMatroskaCh("matroska", "T", mkEl(idChapters, mkEdition(true, nil, nested)), nil)

	plan, err := mustParseBytes(t, data).Edit().SetChapters(wl.Chapter{Start: 0, Title: "Edited"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !hasReportWarning(plan, wl.WarnChaptersFlattened) {
		t.Errorf("expected WarnChaptersFlattened, got %v", plan.Report().Warnings)
	}

	clear, err := mustParseBytes(t, data).Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if hasReportWarning(clear, wl.WarnChaptersFlattened) {
		t.Error("a clear (removal) should not warn about flattening")
	}

	// A flat file (no nested atoms, single display) edits without the warning.
	flat := buildMatroskaCh("matroska", "T", mkEl(idChapters, mkEdition(true, nil,
		mkAtom(1, 0, uint64(ms(200)), "Solo"))), nil)
	plain, err := mustParseBytes(t, flat).Edit().SetChapters(wl.Chapter{Start: 0, Title: "X"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if hasReportWarning(plain, wl.WarnChaptersFlattened) {
		t.Error("a flat chapter edit should not warn about flattening")
	}
}

// hasReportWarning reports whether a plan's write report carries the given warning.
func hasReportWarning(plan *wl.Plan, code wl.WarningCode) bool {
	for _, w := range plan.Report().Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// TestMatroskaChapterPreservedAcrossTagEdit: a tag edit leaves the chapters
// untouched (the Chapters element is copied verbatim by byte range).
func TestMatroskaChapterPreservedAcrossTagEdit(t *testing.T) {
	src := readFixture(t, chaptersMKA)
	before := mustParseBytes(t, src).Chapters()
	out, outDoc := saveMatroska(t, src, mustParseBytes(t, src).Edit().Set(tag.Album, "Edited Album"))

	if !equalChapterLists(outDoc.Chapters(), before) {
		t.Errorf("result chapters changed by a tag edit: %+v want %+v", outDoc.Chapters(), before)
	}
	if !equalChapterLists(mustParseBytes(t, out).Chapters(), before) {
		t.Errorf("reparsed chapters changed by a tag edit")
	}
	if mustParseBytes(t, out).Fields().Album != "Edited Album" {
		t.Error("tag edit did not land")
	}
}

// TestMatroskaChapterReEditNoReparse: a chapter edit applied to the returned
// document (without re-parsing) round-trips, proving the result carries a valid
// rewrite base and chapter model forward.
func TestMatroskaChapterReEditNoReparse(t *testing.T) {
	src := readFixture(t, chaptersMKA)
	out1, doc1 := saveMatroska(t, src, mustParseBytes(t, src).Edit().SetChapters(
		wl.Chapter{Start: 0, End: ms(300), Title: "First Pass"}))

	second := []wl.Chapter{
		{Start: 0, End: ms(100), Title: "Second A"},
		{Start: ms(100), End: ms(500), Title: "Second B"},
	}
	out2, doc2 := saveMatroska(t, out1, doc1.Edit().SetChapters(second...))

	if !equalChapterLists(doc2.Chapters(), second) {
		t.Errorf("re-edit result = %+v, want %+v", doc2.Chapters(), second)
	}
	if !equalChapterLists(mustParseBytes(t, out2).Chapters(), second) {
		t.Errorf("re-edit reparse = %+v, want %+v", mustParseBytes(t, out2).Chapters(), second)
	}
}

// TestMatroskaChapterCopyFromM4B is the cross-format chapter transfer: an M4B's
// chapters project onto a Matroska destination (now that Matroska writes chapters),
// the first time PlanTransfer's chapter path runs between two chapter-bearing
// formats.
func TestMatroskaChapterCopyFromM4B(t *testing.T) {
	src := mustParseFile(t, sampleM4B)
	srcChaps := src.Chapters()
	if len(srcChaps) == 0 {
		t.Skip("M4B fixture has no chapters")
	}

	report, err := src.PlanTransfer(wl.FormatMatroska)
	if err != nil {
		t.Fatalf("PlanTransfer: %v", err)
	}
	sawChapterCarry := false
	for _, it := range report.Items {
		if it.Kind == wl.TransferChapter {
			sawChapterCarry = true
			if it.Disposition != wl.Carried {
				t.Errorf("chapters = %s, want carried (Matroska writes chapters)", it.Disposition)
			}
		}
	}
	if !sawChapterCarry {
		t.Error("expected a carried chapter item")
	}

	// Apply onto a real Matroska destination and confirm the chapters land.
	dstBytes := readFixture(t, sampleMKA)
	dst := mustParseBytes(t, dstBytes)
	plan, _, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	result := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
	got := result.Chapters()
	if len(got) != len(srcChaps) {
		t.Fatalf("transferred %d chapters, want %d", len(got), len(srcChaps))
	}
	for i := range srcChaps {
		if got[i].Start != srcChaps[i].Start || got[i].Title != srcChaps[i].Title {
			t.Errorf("chapter %d = {%v,%q}, want {%v,%q}",
				i, got[i].Start, got[i].Title, srcChaps[i].Start, srcChaps[i].Title)
		}
	}
}
