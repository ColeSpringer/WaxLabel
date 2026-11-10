package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// usltFrame builds a UTF-8 USLT frame: 3-byte language, NUL-terminated descriptor, then the text.
func usltFrame(version byte, lang, desc, text string) []byte {
	return testfile.ID3Frame(version, "USLT", slices.Concat([]byte{3}, commFields(lang, desc, text)))
}

func commentTexts(cs []wl.Comment) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Text
	}
	return out
}

func lyricsTexts(ls []wl.Lyrics) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Text
	}
	return out
}

// assertTextItemsMatch fails unless doc holds one comment item per COMMENT value and one lyrics
// item per LYRICS value, in value order, each with a description and language an edit accepts,
// so a copy can carry them.
func assertTextItemsMatch(t *testing.T, doc *wl.Document) {
	t.Helper()
	comments, _ := doc.Get(tag.Comment)
	if got := commentTexts(doc.Comments()); !slices.Equal(got, comments) {
		t.Errorf("Comments() texts = %q, COMMENT = %q", got, comments)
	}
	lyrics, _ := doc.Get(tag.Lyrics)
	if got := lyricsTexts(doc.Lyrics()); !slices.Equal(got, lyrics) {
		t.Errorf("Lyrics() texts = %q, LYRICS = %q", got, lyrics)
	}
	writable := func(what, s string) {
		if reason := wl.WritableTextReason(s); reason != "" {
			t.Errorf("%s %q %s", what, s, reason)
		}
	}
	for _, c := range doc.Comments() {
		writable("comment description", c.Description)
		writable("comment language", c.Language)
	}
	for _, l := range doc.Lyrics() {
		writable("lyrics description", l.Description)
		writable("lyrics language", l.Language)
	}
}

// TestTextItemsMatchFlatTags: the comment and lyrics items mirror the COMMENT and LYRICS values
// for every fixture as read, for every writable one after an edit of both keys, and for built
// files whose comments sit in several frames or stores.
func TestTextItemsMatchFlatTags(t *testing.T) {
	paths, err := filepath.Glob("../testdata/*")
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob testdata: %v (%d files)", err, len(paths))
	}
	inputs := map[string][]byte{
		"described.mp3": mp3WithFrames(t,
			commFrame(4, "eng", "", "plain"),
			commFrame(4, "deu", "desc", "described"),
			commFrame(4, "eng", "iTunNORM", "machine data"),
			usltFrame(4, "eng", "", "plain lyrics"),
			usltFrame(4, "deu", "Lyr Desc", "la la")),
		"info-and-id3.wav": testfile.WAV(testfile.WAVFmtPCM(),
			testfile.WAVInfo([2]string{"ICMT", "info comment"}),
			testfile.WAVData(64),
			testfile.WAVChunk("id3 ", testfile.ID3v2(4, commFrame(4, "deu", "desc", "id3 comment")))),
		"languages.mka": languageMKA(),
		// A language field with a NUL inside it reads as the code before the NUL.
		"nul-language.mp3": mp3WithFrames(t, commFrame(4, "e\x00g", "", "c"), usltFrame(4, "\x00de", "", "l")),
		"nul-language.mka": testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags, testfile.TagGroup(0,
			langSimpleTag("COMMENT", "f\x00r", "c")))),
	}
	for _, p := range paths {
		inputs[filepath.Base(p)] = readFixture(t, p)
	}

	written := map[wl.Format]bool{}
	for name, data := range inputs {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, data)
			assertTextItemsMatch(t, doc)

			plan, err := doc.Edit().Set(tag.Comment, "first", "second").Set(tag.Lyrics, "la la").Prepare()
			if errors.Is(err, waxerr.ErrUnsupportedFormat) || hasWarning(doc, wl.WarnNoAudioFrames) {
				return // a read-only format, or a file with no audio
			}
			if err != nil {
				t.Fatal(err)
			}
			out := mustParseBytes(t, applyToBytes(t, data, plan))
			if _, ok := out.Get(tag.Comment); !ok {
				t.Fatal("the edit stored no COMMENT")
			}
			assertTextItemsMatch(t, out)
			written[doc.Format()] = true
		})
	}
	for f := wl.Format(1); f.Implemented(); f++ {
		if f.Writable() && !written[f] {
			t.Errorf("no input exercises a written %s file", f)
		}
	}
}

// describedMP3 holds one described comment and one described lyrics frame, both UTF-8 encoded,
// which the writer would not choose for ASCII text: a re-rendered frame differs in its bytes.
func describedMP3(t *testing.T) []byte {
	t.Helper()
	return mp3WithFrames(t, commFrame(4, "eng", "My Desc", "hello"), usltFrame(4, "deu", "Lyr Desc", "la la"))
}

func prepareAndReparse(t *testing.T, data []byte, edit func(*wl.Editor)) (out []byte, doc *wl.Document) {
	t.Helper()
	ed := mustParseBytes(t, data).Edit()
	edit(ed)
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out = applyToBytes(t, data, plan)
	return out, mustParseBytes(t, out)
}

func TestID3CommentAndLyricsQualifiersAreRead(t *testing.T) {
	doc := mustParseBytes(t, describedMP3(t))
	if got, want := doc.Comments(), []wl.Comment{{Text: "hello", Description: "My Desc", Language: "eng"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
	if got, want := doc.Lyrics(), []wl.Lyrics{{Text: "la la", Description: "Lyr Desc", Language: "deu"}}; !slices.Equal(got, want) {
		t.Errorf("Lyrics() = %+v, want %+v", got, want)
	}
}

// The ID3 "XXX" language and a blank one both mean no language.
func TestID3UndefinedLanguageReadsEmpty(t *testing.T) {
	for _, lang := range []string{"XXX", "xxx", "\x00\x00\x00", "   "} {
		doc := mustParseBytes(t, mp3WithFrames(t, commFrame(4, lang, "", "c"), usltFrame(4, lang, "", "l")))
		if got := doc.Comments(); len(got) != 1 || got[0].Language != "" {
			t.Errorf("language %q: Comments() = %+v, want no language", lang, got)
		}
		if got := doc.Lyrics(); len(got) != 1 || got[0].Language != "" {
			t.Errorf("language %q: Lyrics() = %+v, want no language", lang, got)
		}
	}
}

func TestID3TechnicalCommentIsNotAnItem(t *testing.T) {
	doc := mustParseBytes(t, mp3WithFrames(t,
		commFrame(4, "eng", "iTunNORM", "machine data"), commFrame(4, "eng", "", "plain")))
	if got, want := doc.Comments(), []wl.Comment{{Text: "plain", Language: "eng"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
}

// A flat Set replaces the text and leaves the description and language where they were.
func TestSetCommentKeepsQualifiers(t *testing.T) {
	out, doc := prepareAndReparse(t, describedMP3(t), func(e *wl.Editor) { e.Set(tag.Comment, "new").Set(tag.Lyrics, "new words") })
	if got, want := doc.Comments(), []wl.Comment{{Text: "new", Description: "My Desc", Language: "eng"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
	if got, want := doc.Lyrics(), []wl.Lyrics{{Text: "new words", Description: "Lyr Desc", Language: "deu"}}; !slices.Equal(got, want) {
		t.Errorf("Lyrics() = %+v, want %+v", got, want)
	}
	if !bytes.Contains(out, commFields("eng", "My Desc", "new")) {
		t.Error("the rewritten COMM frame does not hold the description and language")
	}
}

func TestSetCommentsWritesQualifiers(t *testing.T) {
	want := []wl.Comment{{Text: "a", Description: "d", Language: "fra"}, {Text: "b", Language: "deu"}}
	out, doc := prepareAndReparse(t, mp3Audio(t), func(e *wl.Editor) { e.SetComments(want...) })
	if got := doc.Comments(); !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
	if v, _ := doc.Get(tag.Comment); !slices.Equal(v, []string{"a", "b"}) {
		t.Errorf("COMMENT = %q, want [a b]", v)
	}
	if !bytes.Contains(out, commFields("fra", "d", "a")) || !bytes.Contains(out, commFields("deu", "", "b")) {
		t.Error("the written COMM frames do not hold each comment's own description and language")
	}
}

func TestSetLyricsWritesQualifiers(t *testing.T) {
	want := []wl.Lyrics{{Text: "words", Description: "Translation", Language: "fra"}}
	out, doc := prepareAndReparse(t, mp3Audio(t), func(e *wl.Editor) { e.SetLyrics(want...) })
	if got := doc.Lyrics(); !slices.Equal(got, want) {
		t.Errorf("Lyrics() = %+v, want %+v", got, want)
	}
	if !bytes.Contains(out, commFields("fra", "Translation", "words")) {
		t.Error("the written USLT frame does not hold the description and language")
	}
}

// A comment written with no language takes ID3's customary eng, the one players look for.
func TestNewID3CommentDefaultsToEnglish(t *testing.T) {
	_, doc := prepareAndReparse(t, mp3Audio(t), func(e *wl.Editor) { e.Set(tag.Comment, "c").Set(tag.Lyrics, "l") })
	if got, want := doc.Comments(), []wl.Comment{{Text: "c", Language: "eng"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
	if got, want := doc.Lyrics(), []wl.Lyrics{{Text: "l", Language: "eng"}}; !slices.Equal(got, want) {
		t.Errorf("Lyrics() = %+v, want %+v", got, want)
	}
}

// A frame whose comment or lyrics the edit leaves alone keeps its bytes, an undefined language
// included, while the frame of the item the edit changes is rendered again.
func TestUntouchedDescribedFramesAreKeptVerbatim(t *testing.T) {
	comm, uslt := commFrame(4, "XXX", "My Desc", "hello"), usltFrame(4, "deu", "Lyr Desc", "la la")
	data := mp3WithFrames(t, comm, commFrame(4, "eng", "", "plain"), uslt)

	out, doc := prepareAndReparse(t, data, func(e *wl.Editor) { e.Set(tag.Comment, "hello", "edited") })
	if !bytes.Contains(out, comm) || !bytes.Contains(out, uslt) {
		t.Error("an untouched described frame was rendered again")
	}
	if v, _ := doc.Get(tag.Comment); !slices.Equal(v, []string{"hello", "edited"}) {
		t.Errorf("COMMENT = %q, want [hello edited]", v)
	}

	changed := wl.Lyrics{Text: "new words", Description: "Lyr Desc", Language: "deu"}
	out, doc = prepareAndReparse(t, data, func(e *wl.Editor) { e.SetLyrics(changed) })
	if bytes.Contains(out, uslt) {
		t.Error("the changed lyrics kept the old frame")
	}
	if got := doc.Lyrics(); !slices.Equal(got, []wl.Lyrics{changed}) {
		t.Errorf("Lyrics() = %+v, want %+v", got, changed)
	}
	if !bytes.Contains(out, comm) {
		t.Error("a lyrics edit rendered the comment frame again")
	}
}

// A change of description or language alone is a write, and the plan names it.
func TestQualifierOnlyEditIsWritten(t *testing.T) {
	data := describedMP3(t)
	ed := mustParseBytes(t, data).Edit().SetComments(wl.Comment{Text: "hello", Description: "Other", Language: "fra"})
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("a description and language change planned no write")
	}
	var lines []string
	for _, c := range plan.Changes() {
		lines = append(lines, c.String())
	}
	if want := []string{`~ COMMENT description: "My Desc" -> "Other"`, `~ COMMENT language: "eng" -> "fra"`}; !slices.Equal(lines, want) {
		t.Errorf("changes = %q, want %q", lines, want)
	}
	doc := mustParseBytes(t, applyToBytes(t, data, plan))
	if got, want := doc.Comments(), []wl.Comment{{Text: "hello", Description: "Other", Language: "fra"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
}

// Comments sharing a description and language are one COMM frame, the form ID3v2.4 defines.
func TestCommentsSharingQualifiersShareAFrame(t *testing.T) {
	out, doc := prepareAndReparse(t, mp3WithFrames(t, testfile.TextFrame(4, "TIT2", "T")), func(e *wl.Editor) {
		e.Set(tag.Comment, "one", "two")
	})
	if !bytes.Contains(out, commFields("eng", "", "one\x00two")) {
		t.Error("two plain comments were not written as one multi-value COMM frame")
	}
	if v, _ := doc.Get(tag.Comment); !slices.Equal(v, []string{"one", "two"}) {
		t.Errorf("COMMENT = %q, want [one two]", v)
	}
}

// A qualifier a format cannot store is named in a warning when an edit authors it.
func TestAuthoredQualifierAFormatCannotStoreWarns(t *testing.T) {
	doc := mustParseBytes(t, testfile.TaggedFLAC("ref"))
	plan, err := doc.Edit().SetComments(wl.Comment{Text: "c", Description: "d", Language: "fra"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnValueReduced)
	if !ok || !slices.Contains(w.Keys, tag.Comment) {
		t.Fatalf("warnings = %v, want value-reduced keyed COMMENT", plan.Report().Warnings)
	}
	plain, err := doc.Edit().Set(tag.Comment, "c").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plain, wl.WarnValueReduced); ok {
		t.Error("a comment with no qualifiers warned")
	}
}

func TestSetCommentsRejectsUnwritableQualifiers(t *testing.T) {
	doc := mustParseBytes(t, mp3Audio(t))
	for _, c := range []wl.Comment{{Text: "c", Description: "a\x00b"}, {Text: "c", Language: "\xff\xfe\xfd"}} {
		if _, err := doc.Edit().SetComments(c).Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("SetComments(%+v): err = %v, want ErrInvalidData", c, err)
		}
	}
}

// idTagLanguage is the Matroska TagLanguage element.
const idTagLanguage = 0x447A

// langSimpleTag builds a SimpleTag carrying a TagLanguage.
func langSimpleTag(name, lang, value string) []byte {
	return testfile.El(testfile.IDSimpleTag, testfile.El(testfile.IDTagName, []byte(name)),
		testfile.El(idTagLanguage, []byte(lang)), testfile.El(testfile.IDTagString, []byte(value)))
}

// languageMKA holds two comments and a lyric: French, undetermined and German.
func languageMKA() []byte {
	return testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags, testfile.TagGroup(0,
		langSimpleTag("COMMENT", "fra", "bonjour"), langSimpleTag("COMMENT", "und", "plain"),
		langSimpleTag("LYRICS", "deu", "la la"))))
}

func TestMatroskaTagLanguageIsRead(t *testing.T) {
	doc := mustParseBytes(t, languageMKA())
	if got, want := doc.Comments(), []wl.Comment{{Text: "bonjour", Language: "fra"}, {Text: "plain"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
	if got, want := doc.Lyrics(), []wl.Lyrics{{Text: "la la", Language: "deu"}}; !slices.Equal(got, want) {
		t.Errorf("Lyrics() = %+v, want %+v", got, want)
	}
}

func TestMatroskaTagLanguageIsWritten(t *testing.T) {
	comments := []wl.Comment{{Text: "a", Language: "fra"}, {Text: "b"}}
	lyrics := []wl.Lyrics{{Text: "l", Language: "deu"}}
	out, doc := prepareAndReparse(t, readFixture(t, notagsMKA), func(e *wl.Editor) { e.SetComments(comments...).SetLyrics(lyrics...) })
	if got := doc.Comments(); !slices.Equal(got, comments) {
		t.Errorf("Comments() = %+v, want %+v", got, comments)
	}
	if got := doc.Lyrics(); !slices.Equal(got, lyrics) {
		t.Errorf("Lyrics() = %+v, want %+v", got, lyrics)
	}
	if !bytes.Contains(out, []byte{0x44, 0x7A, 0x83, 'f', 'r', 'a'}) {
		t.Error("no TagLanguage element holds fra")
	}
}

// A flat Set keeps the language of the comment whose text it replaces, and warns of no lost
// structure: the language is part of the model.
func TestMatroskaSetCommentKeepsLanguage(t *testing.T) {
	data := languageMKA()
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Comment, "salut", "plain").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnTagStructureDropped); ok {
		t.Errorf("a comment edit that keeps its language warned %v", w)
	}
	got := mustParseBytes(t, applyToBytes(t, data, plan)).Comments()
	if want := []wl.Comment{{Text: "salut", Language: "fra"}, {Text: "plain"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
}

// A change of language alone is a write.
func TestMatroskaLanguageOnlyEditIsWritten(t *testing.T) {
	data := languageMKA()
	want := []wl.Comment{{Text: "bonjour", Language: "spa"}, {Text: "plain", Language: "eng"}}
	plan, err := mustParseBytes(t, data).Edit().SetComments(want...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("a language change planned no write")
	}
	doc := mustParseBytes(t, applyToBytes(t, data, plan))
	if got := doc.Comments(); !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
	if got, lyrics := doc.Lyrics(), []wl.Lyrics{{Text: "la la", Language: "deu"}}; !slices.Equal(got, lyrics) {
		t.Errorf("Lyrics() = %+v, want %+v untouched", got, lyrics)
	}
}

// Matroska has no field for a description: authoring one warns and writes the rest.
func TestMatroskaCommentDescriptionWarns(t *testing.T) {
	data := readFixture(t, notagsMKA)
	plan, err := mustParseBytes(t, data).Edit().SetComments(wl.Comment{Text: "c", Description: "d", Language: "fra"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnValueReduced); !ok || !strings.Contains(w.Message, "description not carried") {
		t.Errorf("warnings = %v, want value-reduced naming the description", plan.Report().Warnings)
	}
	got := mustParseBytes(t, applyToBytes(t, data, plan)).Comments()
	if want := []wl.Comment{{Text: "c", Language: "fra"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
}

// A comment tagged at track scope stays where it is, byte for byte, while its text and language
// are what the edit asks for, and moves to album scope with its new language when they are not.
func TestMatroskaScopedCommentFollowsItsLanguage(t *testing.T) {
	scoped := langSimpleTag("COMMENT", "eng", "track note")
	data := testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags, testfile.TagGroup(7, scoped)))

	out, doc := prepareAndReparse(t, data, func(e *wl.Editor) { e.Add(tag.Comment, "second") })
	if !bytes.Contains(out, scoped) {
		t.Error("the untouched track-scope comment was rendered again")
	}
	// The album scope reads first.
	if got, want := doc.Comments(), []wl.Comment{{Text: "second"}, {Text: "track note", Language: "eng"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}

	out, doc = prepareAndReparse(t, data, func(e *wl.Editor) { e.SetComments(wl.Comment{Text: "track note", Language: "fra"}) })
	if bytes.Contains(out, scoped) {
		t.Error("the comment kept its old language in place")
	}
	if got, want := doc.Comments(), []wl.Comment{{Text: "track note", Language: "fra"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
}

// A source whose language field holds a NUL still copies: the language reads as the code before
// the NUL.
func TestTransferOfANULPaddedLanguage(t *testing.T) {
	src := mustParseBytes(t, mp3WithFrames(t, commFrame(4, "e\x00g", "", "c")))
	if got, want := src.Comments(), []wl.Comment{{Text: "c", Language: "e"}}; !slices.Equal(got, want) {
		t.Fatalf("Comments() = %+v, want %+v", got, want)
	}
	if _, _, err := src.PrepareTransfer(mustParseBytes(t, mp3Audio(t))); err != nil {
		t.Errorf("transfer: %v", err)
	}
}

// A value added beside a comment of the same description joins its frame: ID3 allows one COMM
// per language and description, and a value given no language takes the frame's.
func TestAddedCommentJoinsTheFrameOfItsLanguage(t *testing.T) {
	for _, version := range []byte{3, 4} {
		data := append(testfile.ID3v2(version, commFrame(version, "eng", "", "c")), mp3Audio(t)...)
		out, doc := prepareAndReparse(t, data, func(e *wl.Editor) { e.Add(tag.Comment, "second") })
		if n := bytes.Count(out, []byte("COMM")); n != 1 {
			t.Errorf("v2.%d: %d COMM frames, want 1", version, n)
		}
		if !bytes.Contains(out, commFields("eng", "", "c\x00second")) {
			t.Errorf("v2.%d: the added value is not in the eng frame", version)
		}
		want := []wl.Comment{{Text: "c", Language: "eng"}, {Text: "second", Language: "eng"}}
		if got := doc.Comments(); !slices.Equal(got, want) {
			t.Errorf("v2.%d: Comments() = %+v, want %+v", version, got, want)
		}
	}
	// A frame whose language is undefined takes a value given none, and keeps its bytes.
	data := mp3WithFrames(t, commFrame(4, "XXX", "", "c"))
	if out, _ := prepareAndReparse(t, data, func(e *wl.Editor) { e.Add(tag.Comment, "second") }); !bytes.Contains(out, commFields("XXX", "", "c\x00second")) {
		t.Error("the added value is not in the frame of the undefined language")
	}
}

// ID3 holds one lyric per description and language: a second one given the same pair is dropped
// with a warning, and two such frames a file already has stay while the edit keeps their text.
func TestOneLyricPerDescriptionAndLanguage(t *testing.T) {
	data := mp3Audio(t)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Lyrics, "one", "two").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnValueDropped); !ok || !slices.Contains(w.Keys, tag.Lyrics) {
		t.Errorf("warnings = %v, want value-dropped keyed LYRICS", plan.Report().Warnings)
	}
	out := applyToBytes(t, data, plan)
	if n := bytes.Count(out, []byte("USLT")); n != 1 {
		t.Errorf("%d USLT frames, want 1", n)
	}
	if got, want := mustParseBytes(t, out).Lyrics(), []wl.Lyrics{{Text: "one", Language: "eng"}}; !slices.Equal(got, want) {
		t.Errorf("Lyrics() = %+v, want %+v", got, want)
	}

	// A file that already holds two is read whole, and an edit that keeps both texts keeps both
	// frames, adding a translation beside them.
	a, b := usltFrame(4, "eng", "", "one"), usltFrame(4, "eng", "", "two")
	twice := mp3WithFrames(t, a, b)
	out, doc := prepareAndReparse(t, twice, func(e *wl.Editor) {
		e.SetLyrics(wl.Lyrics{Text: "one", Language: "eng"}, wl.Lyrics{Text: "two", Language: "eng"}, wl.Lyrics{Text: "eins", Language: "deu"})
	})
	if !bytes.Contains(out, a) || !bytes.Contains(out, b) || len(doc.Lyrics()) != 3 {
		t.Errorf("Lyrics() = %+v, want the file's two frames kept and the translation added", doc.Lyrics())
	}
}

// The flat edits that follow SetComments act on its values: a kept text keeps its qualifiers, a
// replaced one passes them on by position, and a cleared key keeps none.
func TestFlatEditsAfterSetComments(t *testing.T) {
	a, b := wl.Comment{Text: "a", Description: "da", Language: "fra"}, wl.Comment{Text: "b", Description: "db", Language: "deu"}
	cases := []struct {
		name string
		edit func(*wl.Editor)
		want []wl.Comment
	}{
		{"add", func(e *wl.Editor) { e.SetComments(a, b).Add(tag.Comment, "c") },
			[]wl.Comment{a, b, {Text: "c", Language: "eng"}}},
		{"set keeps a text", func(e *wl.Editor) { e.SetComments(a, b).Set(tag.Comment, "b") }, []wl.Comment{b}},
		{"set replaces a text", func(e *wl.Editor) { e.SetComments(a, b).Set(tag.Comment, "x", "b") },
			[]wl.Comment{{Text: "x", Description: "da", Language: "fra"}, b}},
		{"clear", func(e *wl.Editor) { e.SetComments(a, b).Clear(tag.Comment) }, nil},
		{"set then SetComments", func(e *wl.Editor) { e.Set(tag.Comment, "x").SetComments(a) }, []wl.Comment{a}},
		{"empty SetComments clears", func(e *wl.Editor) { e.SetComments() }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, doc := prepareAndReparse(t, mp3WithFrames(t, commFrame(4, "eng", "old", "old comment"), testfile.TextFrame(4, "TIT2", "T")), c.edit)
			if got := doc.Comments(); !slices.Equal(got, c.want) {
				t.Errorf("Comments() = %+v, want %+v", got, c.want)
			}
		})
	}
}

func lintCodes(doc *wl.Document) []string {
	var codes []string
	for _, f := range doc.Lint() {
		codes = append(codes, f.Code)
	}
	return codes
}

// Lyrics told apart by description or language, a text and its translation, are not several
// values of a single-valued key; lyrics with nothing to tell them apart still are.
func TestQualifiedLyricsAreNotACardinalityConflict(t *testing.T) {
	translated := mp3WithFrames(t, usltFrame(4, "eng", "", "words"), usltFrame(4, "deu", "Translation", "Worte"))
	if codes := lintCodes(mustParseBytes(t, translated)); slices.Contains(codes, "single-valued-multi") {
		t.Errorf("a lyric and its translation lint %v", codes)
	}
	same := mp3WithFrames(t, usltFrame(4, "eng", "", "words"), usltFrame(4, "eng", "", "more words"))
	if codes := lintCodes(mustParseBytes(t, same)); !slices.Contains(codes, "single-valued-multi") {
		t.Errorf("two lyrics of one description and language lint %v, want single-valued-multi", codes)
	}

	lyrics := []wl.Lyrics{{Text: "words", Language: "eng"}, {Text: "Worte", Language: "deu"}}
	plan, err := mustParseBytes(t, mp3Audio(t)).Edit().SetLyrics(lyrics...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnSingleValuedMulti); ok {
		t.Errorf("lyrics in two languages warned %v", w)
	}
	// FLAC stores neither qualifier, so the two lyrics it would hold have nothing to tell them apart.
	plan, err = mustParseBytes(t, testfile.TaggedFLAC("ref")).Edit().SetLyrics(lyrics...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnSingleValuedMulti); !ok {
		t.Errorf("two bare lyrics did not warn: %v", plan.Report().Warnings)
	}
}

// A comment description that names machine data is refused: the frame it would write is one no
// read projects and no edit reaches.
func TestSetCommentsRefusesAMachineDescription(t *testing.T) {
	for _, data := range [][]byte{mp3Audio(t), testfile.TaggedFLAC("ref")} {
		doc := mustParseBytes(t, data)
		for _, desc := range []string{"iTunNORM", "replaygain_track_gain"} {
			_, err := doc.Edit().SetComments(wl.Comment{Text: "x", Description: desc}).Prepare()
			if !errors.Is(err, waxerr.ErrUnsupportedTag) || !strings.Contains(err.Error(), desc) {
				t.Errorf("%s, description %q: err = %v, want ErrUnsupportedTag naming it", doc.Format(), desc, err)
			}
		}
	}
}

// A language the format stores in another form than the edit gave is named in a warning: ID3
// holds three bytes, and "xxx" is its undefined marker.
func TestAuthoredLanguageStoredInAnotherFormWarns(t *testing.T) {
	doc := mustParseBytes(t, mp3Audio(t))
	for lang, stored := range map[string]string{"english": "eng", "xxx": ""} {
		plan, err := doc.Edit().SetComments(wl.Comment{Text: "c", Language: lang}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		w, ok := warningFor(plan, wl.WarnValueReduced)
		if want := fmt.Sprintf("COMMENT: language %q reads back as %q", lang, stored); !ok || w.Message != want || !slices.Contains(w.Keys, tag.Comment) {
			t.Errorf("language %q: warnings = %v, want value-reduced %q", lang, plan.Report().Warnings, want)
		}
	}
	for _, lang := range []string{"eng", "ENG", ""} {
		plan, err := doc.Edit().SetLyrics(wl.Lyrics{Text: "l", Description: "d", Language: lang}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if w, ok := warningFor(plan, wl.WarnValueReduced); ok {
			t.Errorf("language %q warned %v", lang, w)
		}
	}
}

// A comment's TagLanguageBCP47 is not in the model, so an edit that renders the tag again still
// reports the structure it drops.
func TestMatroskaCommentBCP47LanguageDropWarns(t *testing.T) {
	simple := testfile.El(testfile.IDSimpleTag, testfile.El(testfile.IDTagName, []byte("COMMENT")),
		testfile.El(idTagLanguage, []byte("fra")), testfile.El(0x447B, []byte("fr-CA")), testfile.El(testfile.IDTagString, []byte("bonjour")))
	data := testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags, testfile.TagGroup(0, simple)))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Comment, "salut").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnTagStructureDropped); !ok || !slices.Contains(w.Keys, tag.Comment) {
		t.Errorf("warnings = %v, want tag-structure-dropped keyed COMMENT", plan.Report().Warnings)
	}
}

// A duplicate tag's lyric the kept tag holds under another description and language is lost
// with the duplicate, and the write says so; one the kept tag holds as it is costs nothing.
func TestDroppedDuplicateTagGradesQualifiers(t *testing.T) {
	kept := testfile.ID3v2(4, usltFrame(4, "eng", "", "words"), commFrame(4, "eng", "", "c"))
	lossy := testfile.ID3v2(4, usltFrame(4, "deu", "Translation", "words"), commFrame(4, "eng", "", "c"))
	plan, err := mustParseBytes(t, slices.Concat(kept, lossy, mp3Audio(t))).Edit().Set(tag.Artist, "A").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnDuplicateTagBlockDropped); !ok || !strings.Contains(w.Message, "1 lyrics description or language") || strings.Contains(w.Message, "comment") {
		t.Errorf("warnings = %v, want duplicate-tag-block-dropped naming the lyrics qualifiers alone", plan.Report().Warnings)
	}
	// A duplicate identical to the kept tag, or differing only in the default language against an
	// undefined one, loses nothing.
	undefined := testfile.ID3v2(4, usltFrame(4, "XXX", "", "words"), commFrame(4, "XXX", "", "c"))
	for _, dup := range [][]byte{kept, undefined} {
		plan, err = mustParseBytes(t, slices.Concat(kept, dup, mp3Audio(t))).Edit().Set(tag.Artist, "A").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if w, ok := warningFor(plan, wl.WarnDuplicateTagBlockDropped); ok {
			t.Errorf("a duplicate that loses nothing warned %v", w)
		}
	}
	plan, err = mustParseBytes(t, slices.Concat(undefined, kept, mp3Audio(t))).Edit().Set(tag.Artist, "A").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnDuplicateTagBlockDropped); ok {
		t.Errorf("a duplicate in the default language warned %v", w)
	}
}

// hasID3Chunk reports whether a WAV or AIFF document holds an id3 chunk.
func hasID3Chunk(doc *wl.Document) bool {
	for _, e := range doc.Native().Describe() {
		if strings.EqualFold(strings.TrimSpace(e.Kind), "id3 chunk") {
			return true
		}
	}
	return false
}

// WAV and AIFF keep a comment natively until it has a description or language, which only an id3
// chunk stores; an edit of another key adds none.
func TestNativeTextFormatsAddTheID3ChunkForQualifiers(t *testing.T) {
	for _, fixture := range []string{notagsWAV, notagsAIFF} {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			data := readFixture(t, fixture)
			out, doc := prepareAndReparse(t, data, func(e *wl.Editor) { e.Set(tag.Comment, "plain").Set(tag.Title, "T") })
			if hasID3Chunk(doc) {
				t.Fatal("a plain comment created an id3 chunk")
			}
			if _, doc := prepareAndReparse(t, out, func(e *wl.Editor) { e.Set(tag.Artist, "A") }); hasID3Chunk(doc) {
				t.Error("an edit of another key created an id3 chunk")
			}
			want := []wl.Comment{{Text: "plain", Description: "d", Language: "fra"}}
			_, doc = prepareAndReparse(t, out, func(e *wl.Editor) { e.SetComments(want...) })
			if got := doc.Comments(); !hasID3Chunk(doc) || !slices.Equal(got, want) {
				t.Errorf("Comments() = %+v with id3 chunk %v, want %+v in one", got, hasID3Chunk(doc), want)
			}
			// An explicit eng is a language the edit asked for.
			eng := []wl.Comment{{Text: "plain", Language: "eng"}}
			_, doc = prepareAndReparse(t, out, func(e *wl.Editor) { e.SetComments(eng...) })
			if got := doc.Comments(); !slices.Equal(got, eng) {
				t.Errorf("Comments() = %+v, want %+v", got, eng)
			}
		})
	}
}

// A copy from ID3 does not add an id3 chunk for the eng every ID3 comment carries; another
// language, a description, or an eng a format with no default states is content, and does.
func TestCopyOntoNativeTextFormatsAndTheDefaultLanguage(t *testing.T) {
	source := func(comment []byte) *wl.Document {
		return mustParseBytes(t, mp3WithFrames(t, testfile.TextFrame(4, "TIT2", "T"), comment))
	}
	for _, fixture := range []string{notagsWAV, notagsAIFF} {
		data := readFixture(t, fixture)
		for _, c := range []struct {
			name  string
			src   *wl.Document
			chunk bool
			want  wl.Comment
		}{
			{"default language", source(commFrame(4, "eng", "", "c")), false, wl.Comment{Text: "c"}},
			{"another language", source(commFrame(4, "deu", "", "c")), true, wl.Comment{Text: "c", Language: "deu"}},
			{"description", source(commFrame(4, "eng", "d", "c")), true, wl.Comment{Text: "c", Description: "d", Language: "eng"}},
			// Matroska has no default: its eng is a language the file states.
			{"matroska eng", mustParseBytes(t, testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags,
				testfile.TagGroup(0, langSimpleTag("COMMENT", "eng", "c"))))), true, wl.Comment{Text: "c", Language: "eng"}},
		} {
			t.Run(filepath.Base(fixture)+"/"+c.name, func(t *testing.T) {
				plan, report, err := c.src.PrepareTransfer(mustParseBytes(t, data))
				if err != nil || !report.Lossless() {
					t.Fatalf("transfer: %v, report %+v", err, report.Items)
				}
				out := applyToBytes(t, data, plan)
				doc := mustParseBytes(t, out)
				if got := doc.Comments(); hasID3Chunk(doc) != c.chunk || !slices.Equal(got, []wl.Comment{c.want}) {
					t.Errorf("Comments() = %+v with id3 chunk %v, want %+v with %v", got, hasID3Chunk(doc), c.want, c.chunk)
				}
				if again, _, err := c.src.PrepareTransfer(doc); err != nil || !again.IsNoOp() {
					t.Errorf("a second copy is not a no-op: %v", err)
				}
			})
		}
	}
}

// BenchmarkCommentFlood parses a file of n comments, then plans an edit that reverses them and
// one that changes each one's language: the shapes that make a per-comment scan quadratic.
// Matroska's element cap admits some 25,000 such tags.
func BenchmarkCommentFlood(b *testing.B) {
	audio, err := os.ReadFile(notagsMP3)
	if err != nil {
		b.Fatal(err)
	}
	texts := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strconv.Itoa(i)
		}
		return out
	}
	mp3 := func(n int) []byte {
		return append(testfile.ID3v2(4, commFrame(4, "eng", "", strings.Join(texts(n), "\x00"))), audio...)
	}
	mka := func(n int) []byte {
		var simples [][]byte
		for _, text := range texts(n) {
			simples = append(simples, langSimpleTag("COMMENT", "fra", text))
		}
		return testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags, testfile.TagGroup(0, simples...)))
	}
	for _, c := range []struct {
		name  string
		build func(int) []byte
		sizes []int
	}{{"mp3", mp3, []int{40000, 80000, 160000}}, {"mka", mka, []int{5000, 10000, 20000}}} {
		for _, n := range c.sizes {
			data := c.build(n)
			b.Run(c.name+"/"+strconv.Itoa(n), func(b *testing.B) {
				for range b.N {
					doc, err := wl.Parse(context.Background(), wl.BytesSource(data))
					if err != nil {
						b.Fatal(err)
					}
					reversed := doc.Comments()
					slices.Reverse(reversed)
					spanish := doc.Comments()
					for i := range spanish {
						spanish[i].Language = "spa"
					}
					for _, items := range [][]wl.Comment{reversed, spanish} {
						if _, err := doc.Edit().SetComments(items...).Prepare(); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

// A language code reads in lowercase, the form ISO 639-2 gives it, whatever case the file holds,
// and a frame rendered again keeps the file's bytes.
func TestLanguageCodesReadLowercase(t *testing.T) {
	data := mp3WithFrames(t, commFrame(4, "ENG", "", "hello"))
	doc := mustParseBytes(t, data)
	if got, want := doc.Comments(), []wl.Comment{{Text: "hello", Language: "eng"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
	if out, _ := prepareAndReparse(t, data, func(e *wl.Editor) { e.Set(tag.Comment, "edited") }); !bytes.Contains(out, commFields("ENG", "", "edited")) {
		t.Error("the rendered frame did not keep the file's language bytes")
	}
	// A copy reads the same on both sides.
	dst := mp3Audio(t)
	plan, _, err := doc.PrepareTransfer(mustParseBytes(t, dst))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustParseBytes(t, applyToBytes(t, dst, plan)).Comments(); !slices.Equal(got, doc.Comments()) {
		t.Errorf("copied Comments() = %+v, want %+v", got, doc.Comments())
	}
	mka := testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags, testfile.TagGroup(0, langSimpleTag("COMMENT", "FRA", "bonjour"))))
	if got, want := mustParseBytes(t, mka).Comments(), []wl.Comment{{Text: "bonjour", Language: "fra"}}; !slices.Equal(got, want) {
		t.Errorf("Matroska Comments() = %+v, want %+v", got, want)
	}
}

// A SimpleTag with a TagLanguageBCP47 has its TagLanguage ignored, as Matroska requires: the
// model holds no language for it, and the tag stays as it is until its text is edited.
func TestMatroskaBCP47SupersedesTagLanguage(t *testing.T) {
	simple := testfile.El(testfile.IDSimpleTag, testfile.El(testfile.IDTagName, []byte("COMMENT")),
		testfile.El(idTagLanguage, []byte("eng")), testfile.El(0x447B, []byte("fr")), testfile.El(testfile.IDTagString, []byte("bonjour")))
	data := testfile.Matroska("matroska", "T", testfile.El(testfile.IDTags, testfile.TagGroup(0, simple)))
	doc := mustParseBytes(t, data)
	if got, want := doc.Comments(), []wl.Comment{{Text: "bonjour"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
	if out, _ := prepareAndReparse(t, data, func(e *wl.Editor) { e.Set(tag.Artist, "A") }); !bytes.Contains(out, simple) {
		t.Error("an edit of another key rendered the tag again")
	}
}

// A cleared key hands nothing on: a comment added after a Clear is a new one, whichever way the
// clear reaches the editor.
func TestClearedCommentHandsNoQualifiersOn(t *testing.T) {
	data := mp3WithFrames(t, commFrame(4, "deu", "Ripped by EAC", "old"), usltFrame(4, "deu", "d", "old words"))
	var patch tag.TagPatch
	patch.Clear(tag.Comment).Add(tag.Comment, "new")
	patch.Clear(tag.Lyrics).Set(tag.Lyrics, "new words")
	edits := map[string]func(*wl.Editor){
		"clear then add": func(e *wl.Editor) {
			e.Clear(tag.Comment).Add(tag.Comment, "new").Clear(tag.Lyrics).Set(tag.Lyrics, "new words")
		},
		"applied patch": func(e *wl.Editor) { e.Apply(patch) },
		"clear after SetComments": func(e *wl.Editor) {
			e.SetComments(wl.Comment{Text: "a", Description: "da", Language: "fra"}).Clear(tag.Comment).Add(tag.Comment, "new")
			e.SetLyrics(wl.Lyrics{Text: "a", Description: "da", Language: "fra"}).Clear(tag.Lyrics).Add(tag.Lyrics, "new words")
		},
	}
	for name, edit := range edits {
		_, doc := prepareAndReparse(t, data, edit)
		if got, want := doc.Comments(), []wl.Comment{{Text: "new", Language: "eng"}}; !slices.Equal(got, want) {
			t.Errorf("%s: Comments() = %+v, want %+v", name, got, want)
		}
		if got, want := doc.Lyrics(), []wl.Lyrics{{Text: "new words", Language: "eng"}}; !slices.Equal(got, want) {
			t.Errorf("%s: Lyrics() = %+v, want %+v", name, got, want)
		}
	}
}

// The language a writer fills in for a comment the edit did not author is not a planned change,
// as diff does not report it; a language the edit sets is.
func TestPlanListsOnlyAnAuthoredLanguage(t *testing.T) {
	languageChanges := func(plan *wl.Plan) (lines []string) {
		for _, c := range plan.Changes() {
			if strings.HasSuffix(string(c.Key), " language") {
				lines = append(lines, c.String())
			}
		}
		return lines
	}
	// A chapter edit moves a WAV's LIST/INFO comment into the id3 chunk it creates, where the
	// comment gains eng.
	wav, _ := prepareAndReparse(t, readFixture(t, notagsWAV), func(e *wl.Editor) { e.Set(tag.Comment, "plain") })
	plan, err := mustParseBytes(t, wav).Edit().SetChapters(wl.Chapter{Title: "One"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if got := mustParseBytes(t, applyToBytes(t, wav, plan)).Comments(); !slices.Equal(got, []wl.Comment{{Text: "plain", Language: "eng"}}) {
		t.Fatalf("setup: Comments() = %+v, want the comment in the id3 chunk", got)
	}
	if lines := languageChanges(plan); len(lines) != 0 {
		t.Errorf("a chapter edit plans %q", lines)
	}
	mp3 := mp3WithFrames(t, commFrame(4, "XXX", "", "c"))
	plan, err = mustParseBytes(t, mp3).Edit().SetComments(wl.Comment{Text: "c", Language: "eng"}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if lines, want := languageChanges(plan), []string{`~ COMMENT language: "" -> "eng"`}; !slices.Equal(lines, want) {
		t.Errorf("an authored language plans %q, want %q", lines, want)
	}
}
