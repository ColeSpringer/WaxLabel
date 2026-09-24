package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// commFrame builds a COMM frame: encoding byte, 3-byte language, NUL-terminated description, then
// the text. The encoding is UTF-8, which the reader accepts on any version.
func commFrame(version byte, lang, desc, text string) []byte {
	return id3Frame(version, "COMM", slices.Concat([]byte{3}, commFields(lang, desc, text)))
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
	return append(id3v2(4, frames...), mp3Audio(t)...)
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

// The ordinary case, one described comment from Windows Explorer or a CDDB-era tagger, stays
// lossless: the merge is unambiguous, so the description and language survive the edit.
func TestSingleDescribedCommentEditKeepsDescription(t *testing.T) {
	data := mp3WithFrames(t, commFrame(4, "ger", "desc", "the comment"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Comment, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnCommentDescriptionDropped); ok {
		t.Errorf("an unambiguous single-frame merge warned: %v", plan.Report().Warnings)
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

// Several managed frames cannot all keep their description in the one frame the flat model writes,
// so the loss is warned.
func TestAmbiguousCommentMergeWarns(t *testing.T) {
	data := mp3WithFrames(t,
		commFrame(4, "eng", "", "plain comment"),
		commFrame(4, "eng", "desc", "described comment"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Comment, "New").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnCommentDescriptionDropped)
	if !ok {
		t.Fatalf("an ambiguous merge dropped a description silently: %v", plan.Report().Warnings)
	}
	if !slices.Contains(w.Keys, tag.Comment) {
		t.Errorf("warning keys = %v, want COMMENT", w.Keys)
	}
}

// The description labels the comment the destination had. A value arriving from another file is
// not the thing it labels, so keeping it would stamp the destination's "Ripped by EAC" onto the
// source's text.
func TestCarriedCommentDropsTheDestinationDescription(t *testing.T) {
	dst := mustParseBytes(t, mp3WithFrames(t, commFrame(4, "eng", "Ripped by EAC", "old comment")))
	src := mustParseBytes(t, flacWithVendor("ref", "COMMENT=From the source"))

	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Lossless() {
		t.Fatalf("setup: the comment should carry cleanly; report = %+v", report.Items)
	}
	if _, ok := warningFor(plan, wl.WarnCommentDescriptionDropped); !ok {
		t.Errorf("a carry kept the destination's description silently: %v", plan.Report().Warnings)
	}
}

// The description goes with the value the user removed; a removal is not a loss to report.
func TestClearingCommentIsNotADescriptionDrop(t *testing.T) {
	data := mp3WithFrames(t, commFrame(4, "eng", "desc", "the comment"))
	plan, err := mustParseBytes(t, data).Edit().Clear(tag.Comment).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnCommentDescriptionDropped); ok {
		t.Errorf("clearing COMMENT reported a description drop: %v", plan.Report().Warnings)
	}
	if v, ok := mustParseBytes(t, applyToBytes(t, data, plan)).Get(tag.Comment); ok {
		t.Errorf("COMMENT = %v, want gone", v)
	}
}

// With several managed COMM frames the first one's language wins.
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
