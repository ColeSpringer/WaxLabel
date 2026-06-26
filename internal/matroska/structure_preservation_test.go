package matroska

import (
	"bytes"
	"context"
	"testing"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestMatroskaProjectSanitizesInvalidUTF8 is a QA-review regression: the Matroska reader
// stores text as raw bytes, so a non-conformant file can hold invalid UTF-8 in a TagString or
// the Info.Title. project must sanitize the values entering the canonical model (like the
// ID3/MP4/Vorbis readers) so a copy of such a value is not spuriously rejected by the
// write-time UTF-8 guard, and --json never emits raw invalid bytes.
func TestMatroskaProjectSanitizesInvalidUTF8(t *testing.T) {
	artist := encElement(idSimpleTag, cat(
		stringElement(idTagName, "ARTIST"),
		stringElement(idTagString, "bad\xff\xfeval"),
	))
	tags := encElement(idTags, encElement(idTag, cat(encElement(idTargets, uintElement(idTgtTypeVal, 50)), artist)))
	info := encElement(idInfo, stringElement(idSegTitle, "ti\xfftle"))
	m := parseMKA(t, segBytes(cat(info, tags, emptyCluster())))

	if v, _ := m.Tags.First(tag.Artist); !utf8.ValidString(v) {
		t.Errorf("project left invalid UTF-8 in ARTIST: %q", v)
	}
	if v, _ := m.Tags.First(tag.Title); !utf8.ValidString(v) {
		t.Errorf("project left invalid UTF-8 in TITLE: %q", v)
	}
}

// renderPlan materializes a write plan's segments against src into output bytes, the same
// way the engine does, so a structure-preservation test can re-parse the real output.
func renderPlan(t *testing.T, src []byte, plan *core.WritePlan) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := bits.Write(context.Background(), &buf, core.BytesSource(src), plan.Segments, nil); err != nil {
		t.Fatalf("bits.Write: %v", err)
	}
	return buf.Bytes()
}

// structuredTagsMKA builds a non-flat Matroska file (the kind ffmpeg cannot emit and the
// QA suite lacked): an album-scope Tag group whose ARTIST carries a secondary TagLanguage,
// plus a binary SimpleTag and a SimpleTag with a nested sub-tag - exactly the structure a
// flat re-emit drops. It returns the file bytes.
func structuredTagsMKA() []byte {
	targets := encElement(idTargets, uintElement(idTgtTypeVal, 50)) // album scope
	artist := encElement(idSimpleTag, cat(
		stringElement(idTagName, "ARTIST"),
		stringElement(idTagString, "Sterk"),
		stringElement(idTagLang, "fre"), // a meaningful secondary language
	))
	binTag := encElement(idSimpleTag, cat(
		stringElement(idTagName, "COVER_HASH"),
		encElement(idTagBinary, []byte{0xDE, 0xAD, 0xBE, 0xEF}),
	))
	nested := encElement(idSimpleTag, cat(
		stringElement(idTagName, "PARENT"),
		stringElement(idTagString, "p"),
		encElement(idSimpleTag, cat(
			stringElement(idTagName, "CHILD"),
			stringElement(idTagString, "c"),
		)),
	))
	tags := encElement(idTags, encElement(idTag, cat(targets, artist, binTag, nested)))
	return segBytes(cat(mkInfo("Title"), tags, emptyCluster()))
}

// TestF3PreservesAlbumTagStructureThroughUnrelatedEdit is the F3 regression on a non-flat
// fixture: an edit that touches an UNRELATED key must keep every album-scope SimpleTag's
// language, binary value, and nested sub-tags byte-for-byte, not re-emit them flat.
func TestF3PreservesAlbumTagStructureThroughUnrelatedEdit(t *testing.T) {
	src := structuredTagsMKA()
	base := parseMKA(t, src)
	// Sanity: the structured tags parsed.
	if v, _ := base.Tags.First(tag.Artist); v != "Sterk" {
		t.Fatalf("setup: ARTIST = %q, want Sterk", v)
	}

	edited := base.Clone()
	edited.Tags.Set(tag.Album, "New Album") // unrelated to ARTIST/binary/nested

	plan, err := Codec{}.Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out := renderPlan(t, src, plan)

	// The structure survives in the output bytes.
	for _, want := range []struct {
		name  string
		bytes []byte
	}{
		{"ARTIST TagLanguage", []byte("fre")},
		{"binary value", []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{"nested sub-tag name", []byte("CHILD")},
	} {
		if !bytes.Contains(out, want.bytes) {
			t.Errorf("output dropped the %s (a flat re-emit); structure not preserved", want.name)
		}
	}
	// And the unrelated edit landed.
	re := parseMKA(t, out)
	if v, _ := re.Tags.First(tag.Album); v != "New Album" {
		t.Errorf("reparsed ALBUM = %q, want New Album", v)
	}
	if v, _ := re.Tags.First(tag.Artist); v != "Sterk" {
		t.Errorf("reparsed ARTIST = %q, want Sterk (preserved)", v)
	}
	// No spurious tag-structure-dropped warning: nothing structured was edited.
	for _, w := range plan.Report.Warnings {
		if w.Code == core.WarnTagStructureDropped {
			t.Errorf("unrelated edit wrongly warned tag-structure-dropped: %v", w)
		}
	}
}

// TestF3WarnsWhenEditedTagDropsStructure: changing the value of a structured album tag
// cannot keep its old bytes, so the structure is genuinely lost - this must warn (keyed).
func TestF3WarnsWhenEditedTagDropsStructure(t *testing.T) {
	src := structuredTagsMKA()
	base := parseMKA(t, src)

	edited := base.Clone()
	edited.Tags.Set(tag.Artist, "Changed") // edits the tag that carried the language

	plan, err := Codec{}.Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	warned := false
	for _, w := range plan.Report.Warnings {
		if w.Code == core.WarnTagStructureDropped {
			warned = true
			if len(w.Keys) != 1 || w.Keys[0] != tag.Artist {
				t.Errorf("tag-structure-dropped keys = %v, want [ARTIST]", w.Keys)
			}
		}
	}
	if !warned {
		t.Errorf("editing a structured (language-carrying) album tag must warn tag-structure-dropped; got %v", plan.Report.Warnings)
	}
}

// structuredChaptersMKA builds a Matroska file whose default-edition chapter carries the
// full structure modern mkvmerge writes: a ChapLanguage, a ChapLanguageIETF, and explicit
// hidden/disabled flags. allModeled controls whether the chapter has only modeled children
// (so it is not lossy) - the realistic mkvmerge shape.
func structuredChaptersMKA() []byte {
	disp := cat(
		stringElement(idChapString, "Intro"),
		stringElement(idChapLang, "eng"),
		stringElement(idChapLangIETF, "en-US"),
	)
	atom := encElement(idChapterAtom, cat(
		uintElement(idChapterUID, 0x1234),
		uintElement(idChapTimeStart, 0),
		uintElement(idChapFlagHidden, 1),  // hidden
		uintElement(idChapFlagEnabled, 0), // disabled
		encElement(idChapDisplay, disp),
	))
	chapters := encElement(idChapters, encElement(idEditionEntry, atom))
	return segBytes(cat(mkInfo("Title"), chapters, emptyCluster()))
}

// TestF4PreservesChapterStructureThroughReRender is the F4 regression on a non-flat fixture:
// a chapter edit re-renders the default edition, which must keep each chapter's language,
// IETF language, and hidden/disabled flags instead of stripping them to a bare "und" atom.
func TestF4PreservesChapterStructureThroughReRender(t *testing.T) {
	src := structuredChaptersMKA()
	base := parseMKA(t, src)
	if len(base.Chapters) != 1 {
		t.Fatalf("setup: %d chapters, want 1", len(base.Chapters))
	}
	c0 := base.Chapters[0]
	if c0.Language != "eng" || c0.LanguageIETF != "en-US" || !c0.Hidden || !c0.Disabled {
		t.Fatalf("setup parse: chapter = %+v, want eng/en-US/hidden/disabled", c0)
	}

	// A chapter edit (append a new chapter) re-renders the whole default edition.
	edited := base.Clone()
	edited.Chapters = append(core.CloneChapters(base.Chapters), core.Chapter{Start: 5_000_000_000, Title: "New"})

	plan, err := Codec{}.Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out := renderPlan(t, src, plan)

	if !bytes.Contains(out, []byte("en-US")) {
		t.Error("re-render dropped ChapLanguageIETF (en-US); chapter structure not preserved")
	}
	re := parseMKA(t, out)
	if len(re.Chapters) != 2 {
		t.Fatalf("reparsed %d chapters, want 2", len(re.Chapters))
	}
	got := re.Chapters[0]
	if got.Language != "eng" || got.LanguageIETF != "en-US" || !got.Hidden || !got.Disabled {
		t.Errorf("reparsed chapter[0] = %+v, want eng/en-US/hidden/disabled preserved", got)
	}
	// The fully-modeled chapter is not lossy, so the re-render must not warn flatten.
	for _, w := range plan.Report.Warnings {
		if w.Code == core.WarnChaptersFlattened {
			t.Errorf("a fully-modeled mkvmerge-style chapter wrongly warned chapters-flattened: %v", w)
		}
	}
	// A brand-new chapter carries no language: it renders the spec "und" default, which a
	// re-parse normalizes back to "" (so dump shows no spurious "lang: und").
	if l := re.Chapters[1].Language; l != "" {
		t.Errorf("fresh chapter language = %q, want \"\" (und normalized away)", l)
	}
}

// TestF3WarnsOnNarrowerScopeStructureDropped: editing a key whose structured (language-
// carrying) SimpleTag lives at a NON-album scope drops the structure on re-render too, so the
// tag-structure-dropped warning must cover it - not just album scope.
func TestF3WarnsOnNarrowerScopeStructureDropped(t *testing.T) {
	targets := encElement(idTargets, uintElement(idTgtTypeVal, 30)) // track scope
	artist := encElement(idSimpleTag, cat(
		stringElement(idTagName, "ARTIST"),
		stringElement(idTagString, "Sterk"),
		stringElement(idTagLang, "fre"),
	))
	src := segBytes(cat(mkInfo("Title"), encElement(idTags, encElement(idTag, cat(targets, artist))), emptyCluster()))
	base := parseMKA(t, src)

	edited := base.Clone()
	edited.Tags.Set(tag.Artist, "Changed")
	plan, err := Codec{}.Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	warned := false
	for _, w := range plan.Report.Warnings {
		if w.Code == core.WarnTagStructureDropped {
			warned = true
		}
	}
	if !warned {
		t.Errorf("editing a structured non-album tag must warn tag-structure-dropped; got %v", plan.Report.Warnings)
	}
}

// TestF4ChapterLanguagePreservedWithEmptyTitle: a chapter carrying a language but an empty
// title (the case an invalid-UTF-8 title sanitized to "" produces) must keep its language
// through a re-render - the render must emit a ChapterDisplay for the language even with no
// title, or the language is silently lost.
func TestF4ChapterLanguagePreservedWithEmptyTitle(t *testing.T) {
	disp := encElement(idChapDisplay, cat(
		stringElement(idChapString, ""), // empty (sanitized) title
		stringElement(idChapLang, "eng"),
	))
	atom := encElement(idChapterAtom, cat(uintElement(idChapterUID, 0x55), uintElement(idChapTimeStart, 0), disp))
	src := segBytes(cat(mkInfo("Title"), encElement(idChapters, encElement(idEditionEntry, atom)), emptyCluster()))
	base := parseMKA(t, src)
	if c := base.Chapters[0]; c.Title != "" || c.Language != "eng" {
		t.Fatalf("setup: chapter = %+v, want empty title + lang eng", c)
	}

	out := renderPlan(t, src, editAddsChapterPlan(t, src))
	re := parseMKA(t, out)
	if re.Chapters[0].Language != "eng" {
		t.Errorf("chapter language dropped on re-render with an empty title: %q", re.Chapters[0].Language)
	}
}

// TestF4ChapterIETFUndNormalized: an "und" ChapLanguageIETF (mkvmerge's default on nearly
// every chapter) carries no information and must normalize to "" like ChapLanguage, so the
// text listing shows no spurious "[lang: und]" and --json omits the field.
func TestF4ChapterIETFUndNormalized(t *testing.T) {
	disp := encElement(idChapDisplay, cat(
		stringElement(idChapString, "Intro"),
		stringElement(idChapLang, "und"),
		stringElement(idChapLangIETF, "und"),
	))
	atom := encElement(idChapterAtom, cat(uintElement(idChapterUID, 0x66), uintElement(idChapTimeStart, 0), disp))
	src := segBytes(cat(mkInfo("Title"), encElement(idChapters, encElement(idEditionEntry, atom)), emptyCluster()))
	c := parseMKA(t, src).Chapters[0]
	if c.Language != "" || c.LanguageIETF != "" {
		t.Errorf("und languages not normalized: Language=%q LanguageIETF=%q", c.Language, c.LanguageIETF)
	}
}

// TestF3TitleSimpleTagMigratesWithoutDuplicate is a QA-review regression: a file whose title
// lives only in an album TITLE SimpleTag (no Info.Title) migrates that title into Info.Title
// on any edit. The preservation loop must NOT also keep the stale TITLE SimpleTag, or the
// output carries the title twice (Info.Title plus a redundant SimpleTag).
func TestF3TitleSimpleTagMigratesWithoutDuplicate(t *testing.T) {
	// An Info element with no Title child (title lives only in a SimpleTag), plus an album
	// group carrying TITLE + ARTIST.
	info := encElement(idInfo, uintElement(idTimestampScl, 1000000))
	targets := encElement(idTargets, uintElement(idTgtTypeVal, 50))
	titleTag := encElement(idSimpleTag, cat(stringElement(idTagName, "TITLE"), stringElement(idTagString, "MyTitle")))
	artist := encElement(idSimpleTag, cat(stringElement(idTagName, "ARTIST"), stringElement(idTagString, "A")))
	tags := encElement(idTags, encElement(idTag, cat(targets, titleTag, artist)))
	src := segBytes(cat(info, tags, emptyCluster()))

	base := parseMKA(t, src)
	if v, _ := base.Tags.First(tag.Title); v != "MyTitle" {
		t.Fatalf("setup: TITLE = %q, want MyTitle", v)
	}

	edited := base.Clone()
	edited.Tags.Set(tag.Artist, "Changed") // unrelated edit forces the title migration

	plan, err := Codec{}.Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out := renderPlan(t, src, plan)

	// The TITLE SimpleTag is gone (the title lives in Info.Title now), not duplicated.
	if bytes.Contains(out, []byte("TITLE")) {
		t.Error("output still carries a TITLE SimpleTag after migration to Info.Title (duplicate title)")
	}
	if n := bytes.Count(out, []byte("MyTitle")); n != 1 {
		t.Errorf("title value appears %d times, want 1 (no duplicate storage)", n)
	}
	if v, _ := parseMKA(t, out).Tags.First(tag.Title); v != "MyTitle" {
		t.Errorf("reparsed TITLE = %q, want MyTitle (migrated to Info.Title)", v)
	}
}

// TestMatroskaAttachmentDescriptionSanitized is the picture-description half of the parsed-
// text sanitization: a cover attachment's FileDescription is stored as raw bytes, so a
// non-conformant file can hold invalid UTF-8 that a transfer would otherwise re-add and the
// write-time guard reject. The parser sanitizes it into the canonical picture.
func TestMatroskaAttachmentDescriptionSanitized(t *testing.T) {
	att := encElement(idAttachments, encElement(idAttached, cat(
		stringElement(idFileName, "cover.png"),
		stringElement(idFileMime, "image/png"),
		stringElement(idFileDesc, "bad\xff\xfedesc"),
		encElement(idFileData, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}),
		uintElement(idFileUID, 1),
	)))
	m := parseMKA(t, segBytes(cat(mkInfo("Title"), att, emptyCluster())))
	if len(m.Pictures) != 1 {
		t.Fatalf("got %d pictures, want 1", len(m.Pictures))
	}
	if !utf8.ValidString(m.Pictures[0].Description) {
		t.Errorf("attachment description not sanitized: %q", m.Pictures[0].Description)
	}
}

// editAddsChapterPlan re-renders the default edition by appending a chapter, returning the
// plan whose warnings a lossy-detection test inspects.
func editAddsChapterPlan(t *testing.T, src []byte) *core.WritePlan {
	t.Helper()
	base := parseMKA(t, src)
	edited := base.Clone()
	edited.Chapters = append(core.CloneChapters(base.Chapters), core.Chapter{Start: 5_000_000_000, Title: "New"})
	plan, err := Codec{}.Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return plan
}

func warnsFlattened(plan *core.WritePlan) bool {
	for _, w := range plan.Report.Warnings {
		if w.Code == core.WarnChaptersFlattened {
			return true
		}
	}
	return false
}

// TestF4MultipleDisplaysAreLossy: a chapter with a second ChapterDisplay (an other-language
// title) the flat model cannot carry must trip the flatten warning on a chapter edit. The
// second display is skipped inside the atom callback; the lossy flag is set after the loop
// from displays > 1, so this guards that post-loop accounting.
func TestF4MultipleDisplaysAreLossy(t *testing.T) {
	disp1 := encElement(idChapDisplay, cat(stringElement(idChapString, "Intro"), stringElement(idChapLang, "eng")))
	disp2 := encElement(idChapDisplay, cat(stringElement(idChapString, "Anfang"), stringElement(idChapLang, "ger")))
	atom := encElement(idChapterAtom, cat(uintElement(idChapterUID, 0x33), uintElement(idChapTimeStart, 0), disp1, disp2))
	src := segBytes(cat(mkInfo("Title"), encElement(idChapters, encElement(idEditionEntry, atom)), emptyCluster()))
	if !warnsFlattened(editAddsChapterPlan(t, src)) {
		t.Error("a chapter with a second ChapterDisplay must trip chapters-flattened")
	}
}

// TestF4DuplicateChapStringIsLossy: a single ChapterDisplay with two ChapString elements
// keeps only the first - the second is silently dropped unless lossy is flagged, so this
// guards that the duplicate-string case is part of the silent-loss class.
func TestF4DuplicateChapStringIsLossy(t *testing.T) {
	disp := encElement(idChapDisplay, cat(
		stringElement(idChapString, "Intro"),
		stringElement(idChapString, "DUPLICATE"),
		stringElement(idChapLang, "eng"),
	))
	atom := encElement(idChapterAtom, cat(uintElement(idChapterUID, 0x44), uintElement(idChapTimeStart, 0), disp))
	src := segBytes(cat(mkInfo("Title"), encElement(idChapters, encElement(idEditionEntry, atom)), emptyCluster()))
	if !warnsFlattened(editAddsChapterPlan(t, src)) {
		t.Error("a ChapterDisplay with a duplicate ChapString must trip chapters-flattened")
	}
}

// TestF4UnmodeledChapterChildStillWarns: an unmodeled ChapterAtom child the flat model
// cannot carry (here a ChapProcess) still trips the flatten warning on a chapter edit, so
// the broadened lossy detection covers the whole silent-loss class.
func TestF4UnmodeledChapterChildStillWarns(t *testing.T) {
	const idChapProcess = 0x6944
	atom := encElement(idChapterAtom, cat(
		uintElement(idChapterUID, 0x22),
		uintElement(idChapTimeStart, 0),
		encElement(idChapDisplay, stringElement(idChapString, "Intro")),
		encElement(idChapProcess, []byte{0x01}), // an unmodeled child
	))
	src := segBytes(cat(mkInfo("Title"), encElement(idChapters, encElement(idEditionEntry, atom)), emptyCluster()))
	if !warnsFlattened(editAddsChapterPlan(t, src)) {
		t.Error("an unmodeled ChapterAtom child must trip chapters-flattened")
	}
}
