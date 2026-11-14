package waxlabel_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

func findingCodes(fs []wl.Finding) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Code] = true
	}
	return m
}

func TestLintEncoderNoise(t *testing.T) {
	// sample.flac carries ffmpeg's "encoder=Lavf..." stamp. lint reuses the canonical
	// parse-warning code (inherited-encoder), so dump and lint name it identically.
	codes := findingCodes(mustParseFile(t, sampleFLAC).Lint())
	if !codes["inherited-encoder"] {
		t.Errorf("expected inherited-encoder finding; got %v", codes)
	}
}

func TestLintMalformedDate(t *testing.T) {
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.RecordingDate, "not-a-date")
	}))
	codes := findingCodes(doc.Lint())
	if !codes["malformed-date"] {
		t.Errorf("expected malformed-date finding; got %v", codes)
	}
}

func TestLintAcceptsValidDates(t *testing.T) {
	for _, d := range []string{"2021", "2021-06", "2021-06-15"} {
		doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
			e.Set(tag.RecordingDate, d)
		}))
		for _, f := range doc.Lint() {
			if f.Code == "malformed-date" {
				t.Errorf("valid date %q flagged as malformed", d)
			}
		}
	}
}

func TestLintCalendarDates(t *testing.T) {
	lintDate := func(d string) map[string]bool {
		doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
			e.Set(tag.RecordingDate, d)
		}))
		return findingCodes(doc.Lint())
	}

	// Calendar-invalid dates are flagged, including non-leap Feb 29 and non-zero-padded forms.
	for _, d := range []string{"2021-02-31", "2021-13-01", "2021-00-10", "2021-06-00", "2021-2-3", "99999"} {
		if !lintDate(d)["malformed-date"] {
			t.Errorf("date %q should be flagged as malformed", d)
		}
	}
	// Real calendar dates, including a leap-day, are accepted.
	for _, d := range []string{"2020-02-29", "2021-12-31", "2021-06", "2021"} {
		if lintDate(d)["malformed-date"] {
			t.Errorf("valid date %q wrongly flagged", d)
		}
	}
	// Feb 29 in a non-leap year is invalid.
	if !lintDate("2021-02-29")["malformed-date"] {
		t.Error("2021-02-29 (non-leap) should be flagged")
	}
}

func TestLintDuplicatePicture(t *testing.T) {
	png := testfile.PNG()
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: png})
		e.AddPicture(wl.Picture{Type: wl.PicBackCover, Data: png}) // same bytes, different role
	}))
	codes := findingCodes(doc.Lint())
	if !codes["duplicate-picture"] {
		t.Errorf("expected duplicate-picture finding; got %v", codes)
	}
}

// The editor's edit-scope warning and the linter's whole-set finding must produce the same
// duplicate-picture message when identical bytes appear under different roles (front + back).
// Naming only one occurrence's role would make them differ by iteration order.
func TestDuplicatePictureMessageAgrees(t *testing.T) {
	png := testfile.PNG()
	plan, err := mustParseBytes(t, readFixture(t, "../testdata/notags.flac")).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: png}).
		AddPicture(wl.Picture{Type: wl.PicBackCover, Data: png}). // same bytes, different role
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var editorMsg string
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnDuplicatePicture {
			editorMsg = w.Message
		}
	}
	if editorMsg == "" {
		t.Fatal("editor did not warn duplicate-picture on identical front+back covers")
	}
	var lintMsg string
	for _, f := range mustParseBytes(t, applyToBytes(t, readFixture(t, "../testdata/notags.flac"), plan)).Lint() {
		if f.Code == "duplicate-picture" {
			lintMsg = f.Message
		}
	}
	if lintMsg == "" {
		t.Fatal("linter did not flag duplicate-picture on the written file")
	}
	if editorMsg != lintMsg {
		t.Errorf("duplicate-picture messages diverge:\n  editor: %q\n  linter: %q", editorMsg, lintMsg)
	}
	for _, role := range []string{"Front cover", "Back cover"} {
		if !strings.Contains(lintMsg, role) {
			t.Errorf("message %q should name the %q role (all roles the bytes appear under)", lintMsg, role)
		}
	}
}

func TestLintSingleValuedMulti(t *testing.T) {
	// A single-valued key (ENCODER) carrying two values is flagged as a warning. It is the
	// read-side symptom of a transcoded or multi-scope file.
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Encoder, "Lavf", "Lavc")
		e.Set(tag.Artist, "A", "B")
	}))
	var found *wl.Finding
	findings := doc.Lint()
	for i := range findings {
		if findings[i].Code == "single-valued-multi" {
			found = &findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected single-valued-multi finding; got %v", findingCodes(findings))
	}
	if found.Key != tag.Encoder {
		t.Errorf("single-valued-multi Key = %q, want ENCODER (ARTIST is multivalued and exempt)", found.Key)
	}
}

func TestLintCustomKeyMultiValueNotFlagged(t *testing.T) {
	// A custom key with several values gets only the info-level custom-key finding, never
	// single-valued-multi: that warning covers the typed projection's first-only read of known
	// keys, and a custom key has no typed accessor to lose data.
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Key("MY_CUSTOM_FIELD"), "a", "b")
	}))
	codes := findingCodes(doc.Lint())
	if codes["single-valued-multi"] {
		t.Error("a custom multi-valued key was wrongly flagged single-valued-multi")
	}
	if !codes["custom-key"] {
		t.Errorf("a custom key should still get the custom-key info finding; got %v", codes)
	}
}

func TestLintCustomKeyIsInfo(t *testing.T) {
	// A custom (non-vocabulary) key is reported at info severity, so it never flips a clean file
	// to a non-zero exit.
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Key("MY_CUSTOM_FIELD"), "x")
	}))
	var found *wl.Finding
	findings := doc.Lint()
	for i := range findings {
		if findings[i].Code == "custom-key" {
			found = &findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected custom-key finding; got %v", findingCodes(findings))
	}
	if found.Severity != wl.LintInfo {
		t.Errorf("custom-key severity = %v, want info", found.Severity)
	}
	if found.Key != tag.Key("MY_CUSTOM_FIELD") {
		t.Errorf("custom-key Key = %q, want MY_CUSTOM_FIELD", found.Key)
	}
}

func TestLintNegativeNumeric(t *testing.T) {
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.TrackNumber, "-1")
	}))
	findings := doc.Lint()
	var found *wl.Finding
	for i := range findings {
		if findings[i].Code == "negative-numeric" {
			found = &findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected negative-numeric finding for TRACKNUMBER=-1; got %v", findingCodes(findings))
	}
	if found.Severity != wl.LintInfo {
		t.Errorf("negative-numeric severity = %v, want info", found.Severity)
	}
	if found.Key != tag.TrackNumber {
		t.Errorf("negative-numeric Key = %q, want TRACKNUMBER", found.Key)
	}
}

func TestLintClean(t *testing.T) {
	// A freshly written file with one good date and no legacy noise should be clean.
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.Set(tag.Title, "Clean").Set(tag.RecordingDate, "2022-01-01")
	}))
	if fs := doc.Lint(); len(fs) != 0 {
		t.Errorf("expected no findings, got %v", fs)
	}
}

// writeBack applies edits to a fixture in memory and returns the written bytes.
func writeBack(t *testing.T, fixture string, edit func(*wl.Editor)) []byte {
	t.Helper()
	src := readFixture(t, fixture)
	doc, err := wl.Parse(context.Background(), wl.BytesSource(src))
	if err != nil {
		t.Fatal(err)
	}
	ed := doc.Edit()
	edit(ed)
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var buf writerTo
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(src))); err != nil {
		t.Fatal(err)
	}
	return buf.b
}

type writerTo struct{ b []byte }

func (w *writerTo) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

// A numeric genre reference resolved on read is an info-level lint finding, as README promises:
// dump and lint both report numeric-genre.
func TestLintNumericGenre(t *testing.T) {
	base := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
	plan, err := mustParseBytes(t, base).Edit().Set(tag.Genre, "Rock").Prepare(wl.WithNumericGenre())
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, base, plan)

	findings := mustParseBytes(t, out).Lint()
	var found *wl.Finding
	for i := range findings {
		if findings[i].Code == "numeric-genre" {
			found = &findings[i]
		}
	}
	if found == nil {
		t.Fatalf("expected numeric-genre finding; got %v", findingCodes(findings))
	}
	if found.Severity != wl.LintInfo {
		t.Errorf("numeric-genre severity = %v, want info", found.Severity)
	}
}

func TestLintTruncatedAudio(t *testing.T) {
	// A WAV whose data chunk declares far more than the file holds surfaces the
	// parse-time truncated-audio warning as a lint finding.
	dataHdr := append([]byte("data"), testfile.LE32(100000)...)
	data := testfile.WAV(testfile.WAVFmtPCM(), append(dataHdr, make([]byte, 200)...))
	codes := findingCodes(mustParseBytes(t, data).Lint())
	if !codes["truncated-audio"] {
		t.Errorf("expected truncated-audio finding; got %v", codes)
	}
}

func TestLintFindingFixable(t *testing.T) {
	stamped := mustParseBytes(t, testfile.TaggedFLAC("Lavf61.7.100"))
	var sawEncoder bool
	for _, f := range stamped.Lint() {
		if f.Code == "inherited-encoder" {
			sawEncoder = true
			if !f.Fixable {
				t.Error("inherited-encoder should be fixable")
			}
		}
	}
	if !sawEncoder {
		t.Fatal("fixture should lint inherited-encoder")
	}
	// A legacy container holding the only copy of a value is preserved, not fixable.
	legacyOnly := mustParseBytes(t, mp3WithLegacyOnlyID3v1(t))
	for _, f := range legacyOnly.Lint() {
		if f.Code == "trailing-id3v1" && f.Fixable {
			t.Error("a legacy-only trailing-id3v1 must not be fixable")
		}
	}
	fix := legacyOnly.PlanLintFix()
	if len(fix.Options) != 0 {
		t.Errorf("PlanLintFix must agree with the marker: %+v", fix)
	}
}

// The fix clears a stamp from ENCODER and neutralizes a container vendor string. It cannot reach
// a stamp stored under ENCODEDBY (ID3's TENC frame), so that finding must not advertise a repair
// that lint --fix would decline.
func TestLintEncoderFixableOnlyWhereTheFixReaches(t *testing.T) {
	tencOnly := mustParseBytes(t, mp3WithFrames(t, testfile.ID3Frame(4, "TENC", append([]byte{0}, "Lavf61.7.100"...))))
	var saw bool
	for _, f := range tencOnly.Lint() {
		if f.Code != "inherited-encoder" {
			continue
		}
		saw = true
		if f.Fixable {
			t.Error("a stamp only under ENCODEDBY is out of the fix's reach")
		}
	}
	if !saw {
		t.Fatal("a stamped TENC should lint inherited-encoder")
	}
	if fix := tencOnly.PlanLintFix(); len(fix.Patch.Keys()) != 0 {
		t.Errorf("the fix has nothing to patch: %+v", fix.Patch)
	}
	// Beside a stamp under ENCODER the fix clears, the ENCODEDBY one is still out of reach.
	text := func(id, v string) []byte { return testfile.ID3Frame(4, id, append([]byte{0}, v...)) }
	both := mustParseBytes(t, mp3WithFrames(t, text("TSSE", "Lavf61.7.100"), text("TENC", "Lavc61.19.101")))
	marks := map[bool]bool{}
	for _, f := range both.Lint() {
		if f.Code == "inherited-encoder" {
			reached := strings.Contains(f.Message, "Lavf61.7.100")
			marks[reached] = true
			if f.Fixable != reached {
				t.Errorf("%q: fixable %v, want %v", f.Message, f.Fixable, reached)
			}
		}
	}
	if len(marks) != 2 {
		t.Fatalf("want a finding per stamped frame, got %v", both.Lint())
	}
	// A stamp the fix does reach still reads fixable.
	stamped := mustParseBytes(t, testfile.TaggedFLAC("Lavf61.7.100"))
	for _, f := range stamped.Lint() {
		if f.Code == "inherited-encoder" && !f.Fixable {
			t.Error("a vendor-string stamp is reachable and should read fixable")
		}
	}
}

// The fix removes stamps and nothing else: a clean encoder name another store of the file holds
// is kept when the stamp that outranked it goes, and the two stores then agree.
func TestLintFixKeepsACleanEncoderOfAnotherStore(t *testing.T) {
	const stamp = "Lavf61.7.100"
	text := func(id, v string) []byte { return testfile.ID3Frame(4, id, append([]byte{0}, v...)) }
	for name, c := range map[string]struct {
		isft, tsse string
		want       []string
	}{
		"a stamp over a clean name":       {"Clean 1.0", stamp, []string{"Clean 1.0"}},
		"a stamp and a name over another": {"Other 3", stamp + "\x00Clean 1.0", []string{"Clean 1.0", "Other 3"}},
		"a stamp over a stamp":            {"Lavc61.19.101", stamp, nil},
		"a stamp over the same name":      {"clean 1.0", stamp + "\x00Clean 1.0", []string{"Clean 1.0"}},
	} {
		src := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"ISFT", c.isft}),
			testfile.WAVChunk("id3 ", testfile.ID3v2(4, text("TSSE", c.tsse))), testfile.WAVData(4096))
		doc := mustParseBytes(t, src)
		fix := doc.PlanLintFix()
		plan, err := doc.Edit().Apply(fix.Patch).Prepare(fix.Options...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var buf writerTo
		saved, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(src)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, _ := saved.Get(tag.Encoder); !slices.Equal(got, c.want) {
			t.Errorf("%s: ENCODER = %q after the fix, want %q", name, got, c.want)
		}
		if codes := findingCodes(saved.Lint()); codes["inherited-encoder"] || codes["conflicting-families"] {
			t.Errorf("%s: the fixed file still lints %v", name, saved.Lint())
		}
	}
}

// A finding reads fixable only where the fix can be written: a read-only format or stream and a
// file with no audio refuse every write, the fix's included.
func TestLintFixableIsFalseWhereTheWriteIsRefused(t *testing.T) {
	fragmented := append(testfile.MP4AssembleExtra(nil, mp4Mvex(),
		testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9too", "Lavf61.7.100")))),
		testfile.MP4Atom("moof", make([]byte, 16))...)
	// No audio, two stamps under ENCODER, and a legacy tag at odds over the title that the fix
	// would strip from a file that had audio.
	text := func(id, v string) []byte { return testfile.ID3Frame(4, id, append([]byte{0}, v...)) }
	noAudio := slices.Concat(testfile.ID3v2(4, text("TSSE", "Lavf61.7.100\x00Lavc61.19.101"), text("TIT2", "A")),
		testfile.ID3v1("B", "", "", "", "", 255))
	for name, doc := range map[string]*wl.Document{
		"WMA":            mustParseFile(t, "../testdata/sample.wma"),
		"fragmented MP4": mustParseBytes(t, fragmented),
		"chained Ogg":    mustParseBytes(t, append(slices.Clone(readFixture(t, sampleOgg)), readFixture(t, notagsOgg)...)),
		"no audio":       mustParseBytes(t, noAudio),
	} {
		saw := false
		for _, f := range doc.Lint() {
			saw = saw || f.Code == "inherited-encoder"
			if f.Fixable {
				t.Errorf("%s: %s reads fixable, and the fix cannot be written", name, f.Code)
			}
		}
		if !saw {
			t.Errorf("%s: the case should lint inherited-encoder, got %v", name, doc.Lint())
		}
	}
	for _, code := range []string{"trailing-id3v1", "single-valued-multi", "conflicting-families"} {
		if !findingCodes(mustParseBytes(t, noAudio).Lint())[code] {
			t.Errorf("the no-audio case should lint %s", code)
		}
	}

	// The fix is still planned for such a file, so that preparing it reports the refusal: a
	// chained stream whose one finding the fix is for is a redundant leading tag.
	title := testfile.ID3Frame(4, "TIT2", append([]byte{0}, "Sample Title"...))
	chained := mustParseBytes(t, slices.Concat(testfile.ID3v2(4, title), readFixture(t, notagsOgg), readFixture(t, notagsOgg)))
	if codes := findingCodes(chained.Lint()); !codes["stray-leading-id3"] {
		t.Fatalf("the chained case should lint stray-leading-id3, got %v", chained.Lint())
	}
	if slices.ContainsFunc(chained.Lint(), func(f wl.Finding) bool { return f.Fixable }) {
		t.Errorf("a chained stream marks a finding fixable: %v", chained.Lint())
	}
	fix := chained.PlanLintFix()
	if _, err := chained.Edit().Apply(fix.Patch).Prepare(fix.Options...); !errors.Is(err, waxerr.ErrChainedStream) {
		t.Errorf("preparing the fix of a chained stream: %v, want the refusal", err)
	}
}

// An inherited-encoder warning names the field that holds the stamp, where a tag holds it.
func TestInheritedEncoderWarningNamesItsField(t *testing.T) {
	text := func(id, v string) []byte { return testfile.ID3Frame(4, id, append([]byte{0}, v...)) }
	keysOf := func(doc *wl.Document) (keys []tag.Key) {
		for _, w := range doc.Warnings() {
			if w.Code == wl.WarnInheritedEncoder {
				keys = append(keys, w.Keys...)
			}
		}
		return keys
	}
	id3 := mustParseBytes(t, mp3WithFrames(t, text("TSSE", "Lavf61.7.100"), text("TENC", "Lavc61.19.101")))
	if got := keysOf(id3); !slices.Equal(got, []tag.Key{tag.Encoder, tag.EncodedBy}) {
		t.Errorf("ID3: keys %v", got)
	}
	if got := keysOf(mustParseFile(t, "../testdata/sample.wma")); !slices.Equal(got, []tag.Key{tag.Encoder}) {
		t.Errorf("ASF: keys %v", got)
	}
	// APEv2: the editor writes ENCODER and ENCODEDBY as the items the reader maps back.
	ape := mustParseBytes(t, writeBack(t, "../testdata/notags.wv", func(e *wl.Editor) {
		e.Set(tag.Encoder, "Lavf61.7.100").Set(tag.EncodedBy, "Lavc61.19.101")
	}))
	if got := keysOf(ape); !slices.Equal(got, []tag.Key{tag.Encoder, tag.EncodedBy}) {
		t.Errorf("APEv2: keys %v", got)
	}
	// A vendor string is no tag: no key.
	if got := keysOf(mustParseBytes(t, testfile.TaggedFLAC("Lavf61.7.100"))); len(got) != 0 {
		t.Errorf("FLAC vendor: keys %v, want none", got)
	}
}

// The encoder fix clears ENCODER, and with it the findings about the two values WebM held there.
func TestLintEncoderFindingsFixableWithTheStamp(t *testing.T) {
	saw := map[string]bool{}
	for _, f := range mustParseFile(t, "../testdata/sample.webm").Lint() {
		switch f.Code {
		case "inherited-encoder", "conflicting-families", "single-valued-multi":
			saw[f.Code] = true
			if !f.Fixable {
				t.Errorf("%s (%s) should read fixable", f.Code, f.Key)
			}
		}
	}
	if len(saw) != 3 {
		t.Fatalf("the fixture should hold all three findings, saw %v", saw)
	}
}

// What lint marks fixable is what the fix removes. Over every fixture, as it is and with stamped
// and clean values mixed under ENCODER and ENCODEDBY, and over files whose ENCODER lives in two
// stores at once: a finding marked fixable is gone from the file the fix writes, a finding of a
// kind the marker rules on (a stamp, a legacy container, a conflict, anything of ENCODER) left
// unmarked is still there, and where the fix is refused nothing is marked.
func TestLintFixableMatchesTheFix(t *testing.T) {
	ctx := context.Background()
	paths, err := filepath.Glob("../testdata/*")
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob testdata: %v (%d files)", err, len(paths))
	}
	const stamp, stamp2 = "Lavf61.7.100", "Lavc61.19.101 libopus"
	variants := []struct{ encoder, encodedBy []string }{
		{},
		{encoder: []string{stamp}},
		{encoder: []string{stamp, stamp2}},
		{encoder: []string{stamp, "Clean 1.0"}},
		{encoder: []string{"Clean 1.0", stamp, "Other 2"}},
		{encoder: []string{"Clean 1.0", "Other 2"}},
		{encodedBy: []string{stamp}},
		{encoder: []string{stamp}, encodedBy: []string{stamp2}},
		{encoder: []string{stamp, "Clean 1.0"}, encodedBy: []string{stamp2, "Someone"}},
	}
	type id struct {
		code string
		key  tag.Key
		msg  string
	}
	// A keyed finding is its code and key (its message counts values); a keyless one, its text.
	// Two stamps of one text are one identity, so the findings are counted.
	count := func(fs []wl.Finding, only func(wl.Finding) bool) map[id]int {
		m := map[id]int{}
		for _, f := range fs {
			if !only(f) {
				continue
			}
			if f.Key != "" {
				m[id{f.Code, f.Key, ""}]++
			} else {
				m[id{f.Code, "", f.Message}]++
			}
		}
		return m
	}
	all := func(wl.Finding) bool { return true }
	ruledOn := map[string]bool{"inherited-encoder": true, "conflicting-families": true,
		"stray-leading-id3": true, "trailing-id3v1": true, "legacy-ape": true}
	fixed, refused := 0, 0
	check := func(name string, src []byte) {
		doc := mustParseBytes(t, src)
		before := doc.Lint()
		fix := doc.PlanLintFix()
		plan, err := doc.Edit().Apply(fix.Patch).Prepare(fix.Options...)
		if err != nil {
			refused++
			for _, f := range before {
				if f.Fixable {
					t.Errorf("%s: %s reads fixable, and the fix is refused: %v", name, f.Code, err)
				}
			}
			return
		}
		var buf writerTo
		saved, _, err := plan.Execute(ctx, wl.WriteTo(&buf, wl.BytesSource(src)))
		if err != nil {
			t.Fatalf("%s: fix: %v", name, err)
		}
		fixed++
		had, left := count(before, all), count(saved.Lint(), all)
		marked := count(before, func(f wl.Finding) bool { return f.Fixable })
		for k, n := range had {
			// A rewrite can take other findings with it; these kinds are the marker's own.
			gone, own := max(n-left[k], 0), ruledOn[k.code] || k.key == tag.Encoder
			if marked[k] > gone || own && marked[k] < gone {
				t.Errorf("%s: %d of %s %s %q read fixable and the fix removed %d", name, marked[k], k.code, k.key, k.msg, gone)
			}
		}
		// The fix removes stamps and a legacy container's copies of what the tags hold, and
		// nothing else: every other value a store held is still in the file.
		after := saved.Families()
		for _, f := range doc.Families() {
			if f.Targeted || f.Legacy && f.Selected {
				continue
			}
			for _, v := range f.Values {
				kept := slices.ContainsFunc(after, func(a wl.FamilyValue) bool {
					return a.Key == f.Key && slices.ContainsFunc(a.Values, func(av string) bool { return strings.EqualFold(av, v) })
				})
				if !kept && !(f.Key == tag.Encoder && core.IsTranscoderStamp(v)) {
					t.Errorf("%s: the fix lost %s %q, held by the %s store", name, f.Key, v, f.Family)
				}
			}
		}
	}
	for _, path := range paths {
		for vi, v := range variants {
			name := fmt.Sprintf("%s #%d", filepath.Base(path), vi)
			src := readFixture(t, path)
			if v.encoder != nil || v.encodedBy != nil {
				ed := mustParseBytes(t, src).Edit()
				if v.encoder != nil {
					ed.Set(tag.Encoder, v.encoder...)
				}
				if v.encodedBy != nil {
					ed.Set(tag.EncodedBy, v.encodedBy...)
				}
				plan, err := ed.Prepare()
				if err != nil {
					continue // read-only or no audio: the fixture as it is covers it
				}
				var buf writerTo
				if _, _, err := plan.Execute(ctx, wl.WriteTo(&buf, wl.BytesSource(src))); err != nil {
					t.Fatalf("%s: seed: %v", name, err)
				}
				src = buf.b
			}
			check(name, src)
		}
	}
	for name, src := range crossFamilyShapes(t) {
		check(name, src)
	}
	if fixed < 200 || refused < 3 {
		t.Errorf("the sweep wrote %d fixes and met %d refusals; it should reach every format", fixed, refused)
	}
}

// crossFamilyShapes are files that hold ENCODER (one of them TITLE) in two stores at once: the
// format's own tag beside a legacy one, which a rewrite keeps where it alone holds something
// and strips where it does not, and WAV's LIST/INFO beside its id3 chunk.
func crossFamilyShapes(t *testing.T) map[string][]byte {
	t.Helper()
	const stamp, stamp2, clean, other = "Lavf61.7.100", "Lavc61.19.101 libmp3lame", "Clean 1.0", "Other 3"
	text := func(id, v string) []byte { return testfile.ID3Frame(4, id, append([]byte{0}, v...)) }
	own := func(fixture string, encoder []string) []byte {
		return writeBack(t, fixture, func(e *wl.Editor) {
			if encoder == nil {
				e.Clear(tag.Encoder)
			} else {
				e.Set(tag.Encoder, encoder...)
			}
		})
	}
	out := map[string][]byte{}
	for ei, encoder := range [][]string{nil, {stamp}, {clean}, {stamp, clean}} {
		for li, legacy := range []string{"", stamp2, other, clean} {
			for _, alone := range []bool{false, true} {
				name := fmt.Sprintf("own %d, legacy %d, legacy-only key %v", ei, li, alone)
				items, frames := map[string]string{}, [][]byte(nil)
				if legacy != "" {
					items["Encoder"], frames = legacy, append(frames, text("TSSE", legacy))
				}
				if alone {
					items["Conductor"], frames = "X", append(frames, text("TPE3", "X"))
				}
				if len(items) == 0 {
					continue
				}
				out["MP3 and APEv2: "+name] = slices.Concat(own("../testdata/notags.mp3", encoder), apeTag(items))
				lead := testfile.ID3v2(4, frames...)
				out["FLAC and ID3v2: "+name] = slices.Concat(lead, own("../testdata/notags.flac", encoder))
				out["WavPack and ID3v2: "+name] = slices.Concat(lead, own("../testdata/notags.wv", encoder))
			}
		}
	}
	for ii, isft := range []string{"", stamp, clean, "More 4"} {
		for ti, tsse := range [][]string{nil, {stamp}, {clean}, {other}, {stamp, clean}} {
			chunks := [][]byte{testfile.WAVFmtPCM()}
			if isft != "" {
				chunks = append(chunks, testfile.WAVInfo([2]string{"ISFT", isft}))
			}
			if tsse != nil {
				chunks = append(chunks, testfile.WAVChunk("id3 ", testfile.ID3v2(4, text("TSSE", strings.Join(tsse, "\x00")))))
			}
			out[fmt.Sprintf("WAV: ISFT %d, TSSE %d", ii, ti)] = testfile.WAV(append(chunks, testfile.WAVData(4096))...)
		}
	}
	out["MP3 and APEv2: TITLE"] = slices.Concat(testfile.ID3v2(4, text("TIT2", "A")), mp3Audio(t), apeTag(map[string]string{"Title": "B"}))
	// The fix rewrites ENCODER; the TITLE the two tags disagree on is none of its business.
	out["MP3 and APEv2: TITLE beside a stamp"] = slices.Concat(testfile.ID3v2(4, text("TIT2", "A"), text("TSSE", stamp)), mp3Audio(t),
		apeTag(map[string]string{"Title": "B", "Conductor": "X"}))
	// Two stamps of the format's own, and a legacy tag the fix keeps that holds one value or two.
	for name, legacy := range map[string]string{"one": other, "two": other + "\x00More 4"} {
		out["MP3 and APEv2: two stamps, legacy "+name] = slices.Concat(own("../testdata/notags.mp3", []string{stamp, stamp2}),
			apeTag(map[string]string{"Encoder": legacy, "Conductor": "X"}))
	}
	// Two ID3v2 frames that both read as ARTIST disagree with each other, whatever becomes of
	// the ID3v1 tag behind them.
	txxx := testfile.ID3Frame(4, "TXXX", []byte("\x00ARTIST\x00B"))
	out["MP3 and ID3v1: ARTIST twice in the ID3v2 tag"] = slices.Concat(testfile.ID3v2(4, text("TPE1", "A"), txxx), mp3Audio(t),
		testfile.ID3v1("", "C", "", "", "", 255))
	// The fix strips a vendor string's stamp and no more: the stamp in a legacy tag it keeps
	// still disagrees with the clean ENCODER.
	out["FLAC vendor stamp, legacy stamp kept"] = slices.Concat(testfile.ID3v2(4, text("TSSE", stamp2), text("TPE3", "X")),
		own("../testdata/sample.flac", []string{clean}))
	// A title that reads like a stamp is no stamp.
	out["WAV: a stamp for a title"] = testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", stamp}, [2]string{"ISFT", stamp}),
		testfile.WAVChunk("id3 ", testfile.ID3v2(4, text("TIT2", "A"))), testfile.WAVData(4096))
	// A stamp in a frame no finding reports, beside a clean TSSE: the fix has nothing to do.
	out["MP3: a stamp in a TXXX frame named ENCODER"] = mp3WithFrames(t, text("TSSE", clean),
		testfile.ID3Frame(4, "TXXX", []byte("\x00ENCODER\x00"+stamp)))
	// Two legacy containers the fix keeps, each with a value of its own, behind the format's stamp.
	for name, encoder := range map[string][]string{"a stamp": {stamp}, "no ENCODER": nil} {
		out["FLAC between ID3v2 and APEv2: "+name] = slices.Concat(testfile.ID3v2(4, text("TSSE", other), text("TPE3", "X")),
			own("../testdata/notags.flac", encoder), apeTag(map[string]string{"Encoder": "More 4", "Conductor": "Y"}))
	}
	return out
}
