package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestNilContextRejected (M1): every public ctx-taking entry point returns a clean
// error for a nil context instead of panicking on the first ctx.Err() deref.
func TestNilContextRejected(t *testing.T) {
	data := readFixture(t, sampleFLAC)
	var nilCtx context.Context

	wantNil := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: nil context returned no error (want a clean error, not a panic)", name)
			return
		}
		if !strings.Contains(err.Error(), "nil context") {
			t.Errorf("%s: error = %v, want it to mention nil context", name, err)
		}
	}

	_, err := wl.Parse(nilCtx, wl.BytesSource(data))
	wantNil("Parse", err)
	_, err = wl.ParseFile(nilCtx, sampleFLAC)
	wantNil("ParseFile", err)
	_, err = wl.OpenSource(nilCtx, bytes.NewReader(data))
	wantNil("OpenSource", err)

	// Plan.Execute / HashAudioEssence / HashFile need a real document first; build it
	// with a valid context, then exercise the ctx-taking calls with nil.
	doc := mustParseFile(t, copyToTemp(t, sampleFLAC))
	plan, err := doc.Edit().Set(tag.Title, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = plan.Execute(nilCtx, wl.SaveBack())
	wantNil("Plan.Execute", err)
	_, err = doc.HashAudioEssence(nilCtx)
	wantNil("HashAudioEssence", err)
	_, err = doc.HashFile(nilCtx)
	wantNil("HashFile", err)
}

// TestAddedPictureValidation (M2): a picture added to an editor whose bytes are not
// a recognized image is rejected, unless opted out; a file's pre-existing pictures
// are never re-validated, and a transfer carrying already-embedded art still works.
func TestAddedPictureValidation(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)

	// Empty picture data is not a recognized image.
	_, err := mustParseFile(t, path).Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover}).Prepare()
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("empty picture: err = %v, want ErrInvalidData", err)
	}

	// A non-image payload is rejected even with a declared image MIME (Data is
	// re-sniffed), and the message names the opt-out.
	_, err = mustParseFile(t, path).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: []byte("not an image at all")}).
		Prepare()
	if err == nil || !strings.Contains(err.Error(), "WithUnrecognizedPictures") {
		t.Errorf("non-image picture: err = %v, want a message naming WithUnrecognizedPictures", err)
	}
	// The multi-word picture type is quoted so the message reads cleanly.
	if err != nil && !strings.Contains(err.Error(), `"Front cover"`) {
		t.Errorf("non-image picture: err = %v, want the type quoted as \"Front cover\"", err)
	}

	// WithUnrecognizedPictures opts a deliberately exotic cover back in.
	if _, err := mustParseFile(t, path).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: []byte("not an image at all")}).
		Prepare(wl.WithUnrecognizedPictures()); err != nil {
		t.Errorf("WithUnrecognizedPictures should allow a non-image picture, got: %v", err)
	}

	// Embed an exotic cover (opted in) and reparse, so the file now carries an
	// already-embedded non-sniffable picture.
	exotic := mustParseFile(t, path)
	plan, err := exotic.Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: []byte("\x00exotic-bytes-not-sniffable")}).
		Prepare(wl.WithUnrecognizedPictures())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	carrier := mustParseFile(t, path)
	if len(carrier.Pictures()) == 0 {
		t.Fatal("exotic cover did not round-trip")
	}

	// A tags-only edit on the carrier must not re-validate its pre-existing picture.
	if _, err := carrier.Edit().Set(tag.Title, "Tagged").Prepare(); err != nil {
		t.Errorf("tags-only edit re-validated a pre-existing picture: %v", err)
	}

	// Regression: transferring the carrier onto another file still succeeds - the
	// transfer engine opts picture validation out, carrying already-embedded art that
	// the header sniff would reject (copy has no --force).
	dest := mustParseFile(t, copyToTemp(t, sampleFLAC))
	if _, _, err := carrier.PrepareTransfer(dest); err != nil {
		t.Errorf("transfer carrying a non-sniffable embedded cover should succeed, got: %v", err)
	}
}

// TestRemovePicturesMatchOnce (#3): RemovePictures evaluates the caller's match
// predicate exactly once per picture, including pictures added on the same editor -
// the old two-pass sync (DeleteFunc over both the picture list and the added set)
// invoked it twice for added pictures.
func TestRemovePicturesMatchOnce(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	base := len(doc.Pictures())
	ed := doc.Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).
		AddPicture(wl.Picture{Type: wl.PicBackCover, Data: tinyPNG()})
	calls := 0
	ed.RemovePictures(func(wl.Picture) bool { calls++; return false })
	if want := base + 2; calls != want {
		t.Errorf("match called %d times, want %d (exactly once per picture)", calls, want)
	}
}

// TestInvalidKeyHint (L2): a hand-built lowercase tag.Key fails Prepare with a
// message that points the caller at ParseKey/MustKey.
func TestInvalidKeyHint(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	_, err := doc.Edit().Set(tag.Key("title"), "x").Prepare()
	if !errors.Is(err, waxerr.ErrInvalidKey) {
		t.Fatalf("err = %v, want ErrInvalidKey", err)
	}
	if !strings.Contains(err.Error(), "ParseKey") {
		t.Errorf("invalid-key error should mention ParseKey; got %v", err)
	}
}

// TestChapterWarningsSurface (L3/L4): a chapter edit's sanity warnings flow through
// the plan report - a chapter past the file end, and two chapters sharing a start.
func TestChapterWarningsSurface(t *testing.T) {
	doc := mustParseFile(t, sampleM4B)
	dur := doc.Properties().Duration()
	if dur <= 0 {
		t.Fatalf("fixture %s has no duration; cannot test the past-duration warning", sampleM4B)
	}

	plan, err := doc.Edit().SetChapters(
		wl.Chapter{Start: 0, Title: "Intro"},
		wl.Chapter{Start: dur + time.Hour, Title: "WayLate"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasWarning(plan.Report().Warnings, wl.WarnChapterPastDuration) {
		t.Errorf("expected a chapter-past-duration warning; got %v", plan.Report().Warnings)
	}

	plan, err = doc.Edit().SetChapters(
		wl.Chapter{Start: time.Second, Title: "A"},
		wl.Chapter{Start: time.Second, Title: "B"},
	).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasWarning(plan.Report().Warnings, wl.WarnDuplicateChapter) {
		t.Errorf("expected a duplicate-chapter warning; got %v", plan.Report().Warnings)
	}
}

// TestCopyChaptersNoSanityWarnings (#2): a transfer carries the source's chapters
// verbatim, so it must not emit the edit-time chapter sanity warnings about them -
// even when the source's chapters run past the (shorter) destination's duration.
func TestCopyChaptersNoSanityWarnings(t *testing.T) {
	src := mustParseFile(t, sampleM4B)                               // ~9s, chapters at 0:00 / 0:03 / 0:06
	dst := mustParseFile(t, copyToTemp(t, "../testdata/sample.m4a")) // ~1s
	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatal(err)
	}
	// Sanity-check the chapters actually carried (else the test proves nothing).
	carried := false
	for _, it := range report.Items {
		if it.Kind == wl.TransferChapter && it.Disposition != wl.Dropped {
			carried = true
		}
	}
	if !carried {
		t.Skip("chapters were not carried in this transfer; cannot exercise the suppression")
	}
	if reportHasWarning(plan.Report().Warnings, wl.WarnChapterPastDuration) ||
		reportHasWarning(plan.Report().Warnings, wl.WarnDuplicateChapter) {
		t.Errorf("a faithful chapter carry should emit no chapter sanity warnings; got %v", plan.Report().Warnings)
	}
}

func reportHasWarning(ws []wl.Warning, code wl.WarningCode) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}
