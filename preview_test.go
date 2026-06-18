package waxlabel_test

import (
	"context"
	"errors"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

func TestPlanChanges(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	plan, err := doc.Edit().Set(tag.Title, "New").Clear(tag.Encoder).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	changes := plan.Changes()
	got := map[tag.Key]tag.Change{}
	for _, c := range changes {
		got[c.Key] = c
	}
	if c, ok := got[tag.Title]; !ok || c.Kind != tag.ChangeChanged || c.New[0] != "New" {
		t.Errorf("TITLE change = %+v (ok=%v), want changed -> New", c, ok)
	}
	if c, ok := got[tag.Encoder]; !ok || c.Kind != tag.ChangeRemoved {
		t.Errorf("ENCODER change = %+v (ok=%v), want removed", c, ok)
	}
}

// TestPlanChangesNoOp: a plan that edits nothing reports no changes (so the
// preview never invents a change the write would not make).
func TestPlanChangesNoOp(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	plan, err := doc.Edit().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Changes(); len(got) != 0 {
		t.Errorf("no-op plan changes = %v, want none", got)
	}
}

// TestPlanChangesPictures: adding a cover to a picture-free file shows a
// picture-count change under the lowercase "pictures" pseudo-key (which cannot
// collide with a real, always-uppercase canonical key).
func TestPlanChangesPictures(t *testing.T) {
	doc := mustParseFile(t, "testdata/notags.flac")
	plan, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	changes := plan.Changes()
	var pic *tag.Change
	for i := range changes {
		if changes[i].Key == tag.Key("pictures") {
			pic = &changes[i]
		}
	}
	if pic == nil {
		t.Fatalf("expected a pictures change, got %v", changes)
	}
	if pic.Kind != tag.ChangeAdded || len(pic.New) == 0 || pic.New[0] != "1" {
		t.Errorf("pictures change = %+v, want added -> 1", *pic)
	}
}

// TestPlanChangesChapters: a chapter edit shows a chapter-count change under the
// lowercase "chapters" pseudo-key, so the preview is symmetric with the diff
// command (which already reports chapter deltas).
func TestPlanChangesChapters(t *testing.T) {
	doc := mustParseFile(t, "testdata/notags.mka")
	plan, err := doc.Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "One"},
		wl.Chapter{Start: 5 * time.Second, Title: "Two"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	changes := plan.Changes()
	var ch *tag.Change
	for i := range changes {
		if changes[i].Key == tag.Key("chapters") {
			ch = &changes[i]
		}
	}
	if ch == nil {
		t.Fatalf("expected a chapters change, got %v", changes)
	}
	if ch.Kind != tag.ChangeAdded || len(ch.New) == 0 || ch.New[0] != "2" {
		t.Errorf("chapters change = %+v, want added -> 2", *ch)
	}
}

// TestPlanLintFix: the shared fixer clears the encoder stamp so applying its
// patch removes ENCODER.
func TestPlanLintFix(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	fix := doc.PlanLintFix()
	plan, err := doc.Edit().Apply(fix.Patch).Prepare(fix.Options...)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, c := range plan.Changes() {
		if c.Key == tag.Encoder && c.Kind == tag.ChangeRemoved {
			removed = true
		}
	}
	if !removed {
		t.Errorf("PlanLintFix did not remove ENCODER; changes=%v", plan.Changes())
	}
}

// TestHashAudioEssenceEmptyErrors: a tag-only file has no essence to hash, so the
// digest is refused rather than minting a fake-stable hash that would collide
// across distinct empty files.
func TestHashAudioEssenceEmptyErrors(t *testing.T) {
	emptyMP3 := readFixture(t, "testdata/empty.mp3")
	doc, err := wl.Parse(context.Background(), wl.BytesSource(emptyMP3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(emptyMP3))); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("HashAudioEssence on empty file err = %v, want ErrInvalidData", err)
	}
}

// TestParseEmptyMP3WarnsNoAudio: a tag-only/truncated MP3 surfaces the no-audio
// warning so dump and lint can report it.
func TestParseEmptyMP3WarnsNoAudio(t *testing.T) {
	doc, err := wl.Parse(context.Background(), wl.BytesSource(readFixture(t, "testdata/empty.mp3")))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnNoAudioFrames {
			found = true
		}
	}
	if !found {
		t.Errorf("expected WarnNoAudioFrames; got %v", doc.Warnings())
	}
}

// TestLintNoAudioFinding: the no-audio warning maps to a lint error.
func TestLintNoAudioFinding(t *testing.T) {
	doc, err := wl.Parse(context.Background(), wl.BytesSource(readFixture(t, "testdata/empty.mp3")))
	if err != nil {
		t.Fatal(err)
	}
	got := false
	for _, f := range doc.Lint() {
		if f.Code == "no-audio" && f.Severity == wl.LintError {
			got = true
		}
	}
	if !got {
		t.Errorf("expected a no-audio lint error; got %v", doc.Lint())
	}
}

// TestNoAudioIsFormatAgnostic: the no-essence warning and the digest guard are
// driven by one condition for every format, not an MP3 special-case. A tag-only
// WAV must warn (so lint flags it) and refuse to hash (so verify fails) - the two
// surfaces agreeing for a non-MP3 file.
func TestNoAudioIsFormatAgnostic(t *testing.T) {
	emptyWAV := readFixture(t, "testdata/empty.wav")
	doc, err := wl.Parse(context.Background(), wl.BytesSource(emptyWAV))
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnNoAudioFrames {
			warned = true
		}
	}
	if !warned {
		t.Errorf("tag-only WAV: expected WarnNoAudioFrames; got %v", doc.Warnings())
	}
	if _, err := doc.HashAudioEssence(context.Background(), wl.WithHashSource(wl.BytesSource(emptyWAV))); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("tag-only WAV: HashAudioEssence err = %v, want ErrInvalidData", err)
	}
}
