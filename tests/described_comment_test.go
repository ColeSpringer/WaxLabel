package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// commFrame builds a COMM frame: encoding byte, 3-byte language, NUL-terminated description, then
// the text. The encoding is UTF-8, which the reader accepts on any version.
func commFrame(version byte, lang, desc, text string) []byte {
	return testfile.ID3Frame(version, "COMM", slices.Concat([]byte{3}, commFields(lang, desc, text)))
}

// commFields is a COMM body without its leading encoding byte, for asserting on a frame the
// writer produced: the writer picks the narrowest encoding that fits, so the byte is its
// choice to make, while the language, description and text are the content under test.
func commFields(lang, desc, text string) []byte {
	return slices.Concat([]byte(lang), []byte(desc), []byte{0}, []byte(text))
}

// mp3WithFrames wraps frames in an ID3v2.4 tag on top of the tagless audio fixture.
func mp3WithFrames(t *testing.T, frames ...[]byte) []byte {
	t.Helper()
	return append(testfile.ID3v2(4, frames...), mp3Audio(t)...)
}

// A described COMM reads as COMMENT, as ffprobe shows it. Only a machine description stays out.
func TestDescribedCommentIsRead(t *testing.T) {
	data := mp3WithFrames(t, commFrame(4, "eng", "desc", "the comment"))
	doc := mustParseBytes(t, data)
	if v, _ := doc.Get(tag.Comment); !slices.Equal(v, []string{"the comment"}) {
		t.Errorf("COMMENT = %v, want [the comment]", v)
	}
}

// iTunes normalization state is machine data, not a comment, so it is neither read as COMMENT nor
// consumed by a COMMENT edit.
func TestTechnicalCommentStaysUnprojected(t *testing.T) {
	for _, desc := range []string{"iTunNORM", "iTunSMPB", "replaygain_track_gain", "iTunes_CDDB_1"} {
		data := mp3WithFrames(t, commFrame(4, "eng", desc, "machine data"))
		if v, ok := mustParseBytes(t, data).Get(tag.Comment); ok {
			t.Errorf("%s projected as COMMENT = %v, want unprojected", desc, v)
		}
	}
	// A COMMENT edit must not merge it into the new frame.
	data := mp3WithFrames(t, commFrame(4, "eng", "iTunNORM", "machine data"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Comment, "Hello").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("iTunNORM")) {
		t.Error("the iTunNORM frame was consumed by an unrelated COMMENT edit")
	}
	if v, _ := mustParseBytes(t, out).Get(tag.Comment); !slices.Equal(v, []string{"Hello"}) {
		t.Errorf("COMMENT = %v, want [Hello] only", v)
	}
}

// core.BuildFamilies marks a key unselected when distinct sources supply distinct values, so
// labelling each frame by its own description would turn a file carrying one plain and one
// described comment into a spurious conflicting-families finding. One source reads both as a
// multi-valued COMMENT.
func TestDescribedCommentNoFamilyConflict(t *testing.T) {
	data := mp3WithFrames(t,
		commFrame(4, "eng", "", "plain comment"),
		commFrame(4, "eng", "desc", "described comment"))
	doc := mustParseBytes(t, data)
	if v, _ := doc.Get(tag.Comment); !slices.Equal(v, []string{"plain comment", "described comment"}) {
		t.Errorf("COMMENT = %v, want both values", v)
	}
	for _, f := range doc.Lint() {
		if f.Code == "conflicting-families" {
			t.Errorf("two comments read as a family conflict: %+v", f)
		}
	}
}

// managed does not mean rewritten. An edit that does not touch COMMENT leaves the frame byte for
// byte, description and language included.
func TestUnrelatedEditPreservesDescribedComment(t *testing.T) {
	frame := commFrame(4, "eng", "desc", "the comment")
	data := mp3WithFrames(t, frame)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "A").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(applyToBytes(t, data, plan), frame) {
		t.Error("an unrelated edit did not preserve the described COMM frame verbatim")
	}
}

// One described comment from Windows Explorer or a CDDB-era tagger keeps its description and
// language across an edit of its text.
func TestSingleDescribedCommentEditKeepsDescription(t *testing.T) {
	data := mp3WithFrames(t, commFrame(4, "ger", "desc", "the comment"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Comment, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, commFields("ger", "desc", "New")) {
		t.Error("the description or language was not carried into the re-rendered frame")
	}
	// Exactly one comment frame, so a re-parse agrees with the plan.
	if v, _ := mustParseBytes(t, out).Get(tag.Comment); !slices.Equal(v, []string{"New"}) {
		t.Errorf("COMMENT after edit = %v, want [New]; a second frame would mean the plan lied", v)
	}
}

// Each comment frame keeps its own description and language: an edit of one comment's text
// leaves the other frame as it was.
func TestCommentEditKeepsEachFramesQualifiers(t *testing.T) {
	plain := commFrame(4, "eng", "", "plain comment")
	data := mp3WithFrames(t, plain, commFrame(4, "ger", "desc", "described comment"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Comment, "plain comment", "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, plain) || !bytes.Contains(out, commFields("ger", "desc", "New")) {
		t.Error("the comments did not each keep their own description and language")
	}
	want := []wl.Comment{{Text: "plain comment", Language: "eng"}, {Text: "New", Description: "desc", Language: "ger"}}
	if got := mustParseBytes(t, out).Comments(); !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
}

// The description labels the comment the destination had. A value arriving from another file is
// not the thing it labels, so a carry writes the source's comment as the source holds it.
func TestCarriedCommentDropsTheDestinationDescription(t *testing.T) {
	data := mp3WithFrames(t, commFrame(4, "ger", "Ripped by EAC", "old comment"))
	src := mustParseBytes(t, testfile.TaggedFLAC("ref", "COMMENT=From the source"))

	plan, report, err := src.PrepareTransfer(mustParseBytes(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Lossless() {
		t.Fatalf("setup: the comment should carry cleanly; report = %+v", report.Items)
	}
	got := mustParseBytes(t, applyToBytes(t, data, plan)).Comments()
	if want := []wl.Comment{{Text: "From the source", Language: "eng"}}; !slices.Equal(got, want) {
		t.Errorf("Comments() = %+v, want %+v", got, want)
	}
}

// Clearing COMMENT removes a described frame with its value.
func TestClearingCommentRemovesTheDescribedFrame(t *testing.T) {
	data := mp3WithFrames(t, commFrame(4, "eng", "desc", "the comment"))
	plan, err := mustParseBytes(t, data).Edit().Clear(tag.Comment).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := mustParseBytes(t, applyToBytes(t, data, plan)).Get(tag.Comment); ok {
		t.Errorf("COMMENT = %v, want gone", v)
	}
}

// A Set of one value over two comments keeps the first comment's language: the replaced text
// passes its qualifiers on by position.
func TestCommentLanguageFirstWins(t *testing.T) {
	data := mp3WithFrames(t,
		commFrame(4, "ger", "", "first"),
		commFrame(4, "fre", "", "second"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Comment, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, commFields("ger", "", "New")) {
		t.Error("the re-rendered comment did not keep the FIRST frame's language")
	}
}
