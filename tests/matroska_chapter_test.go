package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// Chapter synth helpers; element IDs live in the shared block in matroska_test.go.

// mkAtom builds a ChapterAtom with a UID, start/end (ns), and an optional title.
func mkAtom(uid, startNs, endNs uint64, title string) []byte {
	body := concat(testfile.Uint(idChapterUID, uid), testfile.Uint(idChapTimeStart, startNs))
	if endNs > 0 {
		body = concat(body, testfile.Uint(idChapTimeEnd, endNs))
	}
	if title != "" {
		body = concat(body, testfile.El(idChapDisplay, mkStr(idChapString, title)))
	}
	return testfile.El(idChapterAtom, body)
}

// mkEdition builds an EditionEntry, optionally marked default, with a prefix
// (e.g. an EditionUID) and the given atoms.
func mkEdition(def bool, prefix []byte, atoms ...[]byte) []byte {
	var body []byte
	body = append(body, prefix...)
	if def {
		body = append(body, testfile.Uint(idEditionFlagDf, 1)...)
	}
	body = append(body, concat(atoms...)...)
	return testfile.El(idEditionEntry, body)
}

// buildMatroskaCh assembles a minimal file with an Info title, a Chapters element, and optional
// Tags.
func buildMatroskaCh(docType, title string, chapters, tags []byte) []byte {
	return testfile.Matroska(docType, title, concat(chapters, tags))
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// The ffmpeg-written chapter fixture: three chapters with absolute-nanosecond Start/End and
// ChapterDisplay titles.
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

// A chapter edit on the fixture (the absorption path, since the file has a Void) reads back
// exactly and leaves the cluster essence untouched.
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
	essenceUnchanged(t, src, out)
	// Other metadata survives the chapter edit.
	if re.Fields().Title != "Chapter Sample" {
		t.Errorf("Info.Title lost: %q", re.Fields().Title)
	}
}

// Chapter edits on both write paths recompute every CRC-32 in the output, including the
// re-rendered Chapters master, which a strict reader checks.
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

// ffprobe reads edited chapters back with their End times, proving the Chapters tree is valid,
// and the FLAC audio stream stays intact.
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

// adds chapters to a file that had none (sample.mka), confirming the new Chapters element
// round-trips and the audio is untouched.
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
	// The WebM subset has no EditionFlagDefault.
	if ids := chapterElementIDs(t, out); ids[idEditionFlagDf] {
		t.Errorf("the WebM edition holds an EditionFlagDefault (elements %v)", ids)
	}
}

// Chapter flag IDs: ChapterFlagHidden and ChapterFlagEnabled.
const (
	idChapFlagHidden  = 0x98
	idChapFlagEnabled = 0x4598
)

// chapterElementIDs returns the IDs of the elements a file's first EditionEntry and its
// ChapterAtoms hold.
func chapterElementIDs(t *testing.T, data []byte) map[uint64]bool {
	t.Helper()
	_, start, end, ok := elemRange(data, 0, len(data), idEditionEntry, map[uint64]bool{testfile.IDSegment: true, idChapters: true})
	if !ok {
		t.Fatal("no EditionEntry in the output")
	}
	ids := map[uint64]bool{}
	eachChildField(data, start, end, func(id uint64, ds, de int) {
		ids[id] = true
		if id == idChapterAtom {
			eachChildField(data, ds, de, func(id uint64, _, _ int) { ids[id] = true })
		}
	})
	return ids
}

// hiddenAndDisabled is one chapter of each flag the WebM subset cannot store.
var hiddenAndDisabled = []wl.Chapter{
	{Start: 0, End: 200 * time.Millisecond, Title: "Hidden", Hidden: true},
	{Start: 200 * time.Millisecond, End: 400 * time.Millisecond, Title: "Disabled", Disabled: true},
}

// The WebM subset has no ChapterFlagHidden or ChapterFlagEnabled: a hidden or disabled chapter is
// written without them, reads back visible and enabled, and the plan says so. Matroska keeps both.
func TestMatroskaWebMChapterFlagsAreNotWritten(t *testing.T) {
	plain := make([]wl.Chapter, len(hiddenAndDisabled))
	for i, c := range hiddenAndDisabled {
		c.Hidden, c.Disabled = false, false
		plain[i] = c
	}
	for _, c := range []struct {
		fixture string
		flags   bool
	}{{sampleWebM, false}, {notagsMKA, true}} {
		src := readFixture(t, c.fixture)
		plan, err := mustParseBytes(t, src).Edit().SetChapters(hiddenAndDisabled...).Prepare()
		if err != nil {
			t.Fatalf("%s: %v", c.fixture, err)
		}
		if got := planWarns(t, plan, wl.WarnChapterMetadataDropped); got == c.flags {
			t.Errorf("%s: chapter-metadata-dropped warned %v, want %v", c.fixture, got, !c.flags)
		}
		out := applyToBytes(t, src, plan)
		want := plain
		if c.flags {
			want = hiddenAndDisabled
		}
		if got := mustParseBytes(t, out).Chapters(); !equalChapterLists(got, want) {
			t.Errorf("%s: chapters read back = %+v, want %+v", c.fixture, got, want)
		}
		ids := chapterElementIDs(t, out)
		hidden, disabled := ids[idChapFlagHidden], ids[idChapFlagEnabled]
		if hidden != c.flags || disabled != c.flags {
			t.Errorf("%s: ChapterFlagHidden written %v, ChapterFlagEnabled written %v; want %v", c.fixture, hidden, disabled, c.flags)
		}
	}
}

// A copy of a hidden chapter into a WebM file is graded lossy with the reason, and the chapter
// reads back visible.
func TestMatroskaCopyOfHiddenChapterIntoWebMIsLossy(t *testing.T) {
	mka := readFixture(t, notagsMKA)
	srcBytes, _ := saveMatroska(t, mka, mustParseBytes(t, mka).Edit().SetChapters(hiddenAndDisabled[0]))
	dstBytes := readFixture(t, sampleWebM)
	plan, report, err := mustParseBytes(t, srcBytes).Transfer().Prepare(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatal(err)
	}
	graded := false
	for _, it := range report.Items {
		if it.Kind == wl.TransferChapter {
			graded = true
			if it.Disposition != wl.Lossy || !strings.Contains(it.Reason, "hidden") {
				t.Errorf("chapter item = %v %q, want lossy with a reason naming the hidden flag", it.Disposition, it.Reason)
			}
		}
	}
	if !graded {
		t.Error("the transfer report grades no chapter")
	}
	got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters()
	if len(got) != 1 || got[0].Hidden || got[0].Title != "Hidden" {
		t.Errorf("chapters read back = %+v, want the one chapter, visible", got)
	}
}

// chapter edit reuses each chapter's original ChapterUID by position, so chapter-scoped SimpleTags
// that reference a UID stay valid.
func TestMatroskaChapterUIDsPreserved(t *testing.T) {
	chapters := testfile.El(idChapters, mkEdition(true, nil,
		mkAtom(42, 0, uint64(ms(200)), "First"),
		mkAtom(99, uint64(ms(200)), uint64(ms(400)), "Second"),
	))
	// A chapter-scoped tag referencing ChapterUID 42 (preserved verbatim).
	tags := testfile.El(testfile.IDTags, testfile.El(testfile.IDTag, concat(
		testfile.El(testfile.IDTargets, concat(testfile.Uint(testfile.IDTgtTypeVal, 30), testfile.Uint(idTagChapUID, 42))),
		testfile.SimpleTag("ARTIST", "Chapter Performer"))))
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

// only the default edition is re-rendered on an edit; a non-default edition is preserved verbatim.
func TestMatroskaMultiEditionPreserved(t *testing.T) {
	chapters := testfile.El(idChapters, concat(
		mkEdition(true, nil, mkAtom(1, 0, uint64(ms(300)), "DefaultChap")),
		mkEdition(false, testfile.Uint(idEditionUID, 7777), mkAtom(2, 0, uint64(ms(300)), "NonDefaultChap")),
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

// Clearing chapters on a multi-edition file removes the whole Chapters element. Dropping only
// the default edition would promote a hidden non-default edition into view.
func TestMatroskaChapterClearMultiEdition(t *testing.T) {
	chapters := testfile.El(idChapters, concat(
		mkEdition(true, nil, mkAtom(1, 0, uint64(ms(300)), "DefaultChap")),
		mkEdition(false, testfile.Uint(idEditionUID, 7777), mkAtom(2, 0, uint64(ms(300)), "HiddenChap")),
	))
	data := buildMatroskaCh("matroska", "T", chapters, nil)
	out, outDoc := saveMatroska(t, data, mustParseBytes(t, data).Edit().ClearChapters())

	// The plan says the other edition goes too.
	plan, err := mustParseBytes(t, data).Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnChapterMetadataDropped); !ok || !strings.Contains(w.Message, "1 other edition") || len(w.Keys) != 0 {
		t.Errorf("warning = %q keys %v (found %v), want a keyless chapter-metadata-dropped naming the 1 other edition", w.Message, w.Keys, ok)
	}
	if ops := plan.Report().Operations; !slices.Contains(ops, "Chapters: 1 other edition removed") {
		t.Errorf("operations = %q, want a line for the other edition", ops)
	}

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

// Clearing the chapters of a file with one edition, and editing those of a file with several,
// removes no other edition and reports none.
func TestMatroskaChapterEditReportsNoOtherEdition(t *testing.T) {
	one := buildMatroskaCh("matroska", "T", testfile.El(idChapters, mkEdition(true, nil, mkAtom(1, 0, uint64(ms(300)), "Only"))), nil)
	two := buildMatroskaCh("matroska", "T", testfile.El(idChapters, concat(
		mkEdition(true, nil, mkAtom(1, 0, uint64(ms(300)), "DefaultChap")),
		mkEdition(false, testfile.Uint(idEditionUID, 7777), mkAtom(2, 0, uint64(ms(300)), "HiddenChap")),
	)), nil)
	for name, plan := range map[string]*wl.Plan{
		"clear of one edition":      prepareWith(t, one, func(e *wl.Editor) { e.ClearChapters() }),
		"edit beside other edition": prepareWith(t, two, func(e *wl.Editor) { e.SetChapters(wl.Chapter{Title: "New"}) }),
	} {
		if planWarns(t, plan, wl.WarnChapterMetadataDropped) {
			t.Errorf("%s: warnings = %v, want no chapter-metadata-dropped", name, plan.Report().Warnings)
		}
		for _, op := range plan.Report().Operations {
			if strings.Contains(op, "edition") {
				t.Errorf("%s: operations name an edition: %q", name, op)
			}
		}
	}
}

// file whose ChapterAtoms are stored out of start order projects sorted by start (so re-setting it
// is a no-op) and keeps each chapter aligned with its own ChapterUID on a re-render.
func TestMatroskaChapterOutOfOrderSorted(t *testing.T) {
	chapters := testfile.El(idChapters, mkEdition(true, nil,
		mkAtom(200, uint64(ms(200)), uint64(ms(400)), "Second"), // stored first, starts later
		mkAtom(100, 0, uint64(ms(200)), "First"),                // stored second, starts at 0
	))
	data := buildMatroskaCh("matroska", "T", chapters, nil)

	doc := mustParseBytes(t, data)
	chs := doc.Chapters()
	if len(chs) != 2 || chs[0].Title != "First" || chs[1].Title != "Second" {
		t.Fatalf("projection not sorted by start: %+v", chs)
	}
	// Re-setting the already-sorted projection is a no-op.
	plan, err := doc.Edit().SetChapters(chs...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Report().NoOp {
		t.Errorf("SetChapters(doc.Chapters()...) on an out-of-order file should be a no-op, ops=%v", plan.Report().Operations)
	}
	// An edit keeps each chapter's own UID: the start=0 chapter must reuse UID 100 (0x64) and the
	// later one UID 200 (0xC8), not the swapped file-order pair.
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

// editing a default edition that carries a nested sub-chapter or a second (other-language) display
// drops that structure, surfaced as a plan-time WarnChaptersFlattened.
func TestMatroskaChapterFlattenWarns(t *testing.T) {
	nested := testfile.El(idChapterAtom, concat(
		testfile.Uint(idChapterUID, 1), testfile.Uint(idChapTimeStart, 0),
		testfile.El(idChapDisplay, mkStr(idChapString, "Top")),
		testfile.El(idChapterAtom, concat( // a nested sub-chapter the flat model cannot hold
			testfile.Uint(idChapterUID, 9), testfile.Uint(idChapTimeStart, uint64(ms(100))),
			testfile.El(idChapDisplay, mkStr(idChapString, "Sub")))),
	))
	data := buildMatroskaCh("matroska", "T", testfile.El(idChapters, mkEdition(true, nil, nested)), nil)

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
	flat := buildMatroskaCh("matroska", "T", testfile.El(idChapters, mkEdition(true, nil,
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

// tag edit leaves the chapters untouched (the Chapters element is copied verbatim by byte range).
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

// A chapter edit on the Document a chapter write returned round-trips: the writer takes its own
// output as a rewrite base.
func TestMatroskaChapterReEditOfReturnedDocument(t *testing.T) {
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

// Cross-format chapter transfer: an M4B's chapters project onto a Matroska destination through
// PlanTransfer's chapter path.
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

	// Apply onto a Matroska destination and check the chapters.
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

// A copy into a Matroska or WebM file carries the last chapter's end as the source gives it, even
// where that is the source's own duration: the destination stores an end only where one is given,
// so opening it would lose it.
func TestMatroskaCopyCarriesAChapterEndAtTheDuration(t *testing.T) {
	src := mustParseFile(t, "../testdata/chapters.mka")
	chs := src.Chapters()
	if n := len(chs); n == 0 || chs[n-1].End != ms(600) || src.Properties().Duration().Truncate(time.Millisecond) != ms(600) {
		t.Fatalf("setup: chapters %+v of a %v file, want the last to end at the 600 ms duration", chs, src.Properties().Duration())
	}
	for _, fixture := range []string{notagsMKA, sampleWebM} {
		dstBytes := readFixture(t, fixture)
		plan, _, err := src.Transfer().Prepare(mustParseBytes(t, dstBytes))
		if err != nil {
			t.Fatalf("%s: %v", fixture, err)
		}
		if got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters(); !equalChapterLists(got, chs) {
			t.Errorf("%s: copied chapters = %+v, want %+v", fixture, got, chs)
		}
	}
}

// Setting the chapters a WebM file already holds, a hidden one among them, writes nothing and
// loses nothing, so nothing is reported lost.
func TestMatroskaWebMUnchangedHiddenChapterDoesNotWarn(t *testing.T) {
	hidden := testfile.El(idChapterAtom, testfile.Uint(idChapterUID, 1), testfile.Uint(idChapTimeStart, 0),
		testfile.Uint(idChapFlagHidden, 1), testfile.El(idChapDisplay, mkStr(idChapString, "Hidden")))
	src := buildMatroskaCh("webm", "T", testfile.El(idChapters, mkEdition(true, nil, hidden)), nil)
	doc := mustParseBytes(t, src)
	chs := doc.Chapters()
	if len(chs) != 1 || !chs[0].Hidden {
		t.Fatalf("setup: chapters = %+v, want one hidden chapter", chs)
	}
	plan, err := doc.Edit().SetChapters(chs...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Errorf("the plan writes: %v", plan.Report().Operations)
	}
	if planWarns(t, plan, wl.WarnChapterMetadataDropped) {
		t.Errorf("warnings = %v, want no chapter-metadata-dropped for chapters that stay as they are", plan.Report().Warnings)
	}
}

// A last chapter that runs to the end of its source ends at the end of a shorter destination,
// not past it: there the end is written open, as it is for every other format.
func TestMatroskaCopyOpensAChapterEndPastTheDestination(t *testing.T) {
	src := mustParseFile(t, "../testdata/chapters.mka")
	chs := src.Chapters()
	// A destination 500 ms long: TimestampScale 1 ms, Duration 500.
	info := testfile.El(testfile.IDInfo, testfile.Uint(idTimestampScale, 1000000), mkFloat(testfile.IDDuration, 500))
	tracks := testfile.El(testfile.IDTracks, mkTrack(1, 1, 2, "A_PCM/INT/LIT"))
	dstBytes := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, info, tracks, testfile.AudioCluster(0)))
	dst := mustParseBytes(t, dstBytes)
	if d := dst.Properties().Duration(); d != ms(500) {
		t.Fatalf("setup: destination duration = %v, want 500ms", d)
	}
	plan, _, err := src.Transfer().Prepare(dst)
	if err != nil {
		t.Fatal(err)
	}
	got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters()
	want := slices.Clone(chs)
	want[len(want)-1].End = 0
	if !equalChapterLists(got, want) {
		t.Errorf("copied chapters = %+v, want the last end open: %+v", got, want)
	}
}

// A copy of a file onto itself changes nothing, even where its last chapter ends past its own
// duration: the end is opened only for a destination shorter than the source.
func TestMatroskaSelfCopyKeepsAChapterEndPastTheDuration(t *testing.T) {
	info := testfile.El(testfile.IDInfo, testfile.Uint(idTimestampScale, 1000000), mkFloat(testfile.IDDuration, 500))
	tracks := testfile.El(testfile.IDTracks, mkTrack(1, 1, 2, "A_PCM/INT/LIT"))
	chapters := testfile.El(idChapters, mkEdition(true, nil,
		mkAtom(1, 0, uint64(ms(300)), "One"), mkAtom(2, uint64(ms(300)), uint64(ms(600)), "Two")))
	src := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, info, tracks, testfile.AudioCluster(0), chapters))
	doc := mustParseBytes(t, src)
	if chs := doc.Chapters(); len(chs) != 2 || chs[1].End != ms(600) || doc.Properties().Duration() != ms(500) {
		t.Fatalf("setup: chapters %+v of a %v file, want the last to end at 600 ms in a 500 ms file", chs, doc.Properties().Duration())
	}
	plan, _, err := doc.Transfer().Prepare(mustParseBytes(t, src))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Errorf("a copy of the file onto itself plans a write: %v", plan.Report().Operations)
	}
}

// chapteredFile is a Matroska file of the given duration in milliseconds (none when 0) whose two
// chapters end at 300 and 600 ms.
func chapteredFile(durationMs float64, chapters bool) []byte {
	info := testfile.El(testfile.IDInfo, testfile.Uint(idTimestampScale, 1000000))
	if durationMs > 0 {
		info = testfile.El(testfile.IDInfo, testfile.Uint(idTimestampScale, 1000000), mkFloat(testfile.IDDuration, durationMs))
	}
	tracks := testfile.El(testfile.IDTracks, mkTrack(1, 1, 2, "A_PCM/INT/LIT"))
	var chs []byte
	if chapters {
		chs = testfile.El(idChapters, mkEdition(true, nil,
			mkAtom(1, 0, uint64(ms(300)), "One"), mkAtom(2, uint64(ms(300)), uint64(ms(600)), "Two")))
	}
	return concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, info, tracks, testfile.AudioCluster(0), chs))
}

// The last chapter's end is opened only for a destination known to be shorter than the source by
// a millisecond or more: one of unknown length, or shorter by less, takes the end as given.
func TestMatroskaCopyKeepsAChapterEndUnlessTheDestinationIsShorter(t *testing.T) {
	src := mustParseBytes(t, chapteredFile(600.7, true))
	for _, c := range []struct {
		name   string
		dst    float64
		wantMs int
	}{
		{"a destination of unknown length", 0, 600},
		{"one shorter by less than a millisecond", 600.2, 600},
		{"one as long", 600.7, 600},
		{"a longer one", 900, 600},
		{"one shorter by a millisecond", 599.7, 0},
	} {
		dstBytes := chapteredFile(c.dst, false)
		plan, _, err := src.Transfer().Prepare(mustParseBytes(t, dstBytes))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Chapters()
		if len(got) != 2 || got[1].End != ms(c.wantMs) {
			t.Errorf("%s: copied chapters = %+v, want the last end at %d ms (0 is open)", c.name, got, c.wantMs)
		}
	}
}
