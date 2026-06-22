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

// TestLegacyConflictWarning (Codex #5): editing a key also held in a preserved legacy
// container (the trailing ID3v1 of sample.mp3) under the default LegacyPreserve policy
// surfaces a legacy-conflict warning, since the ID3v1 copy now disagrees with the
// native tag. --legacy strip (which removes the legacy container) resolves it without
// warning, setting the legacy value verbatim does not conflict, and editing a key the
// legacy container does not hold does not conflict either.
func TestLegacyConflictWarning(t *testing.T) {
	path := copyToTemp(t, sampleMP3) // carries TITLE in both id3v2 and id3v1

	// Editing TITLE to a new value leaves the preserved id3v1 copy stale -> warn.
	plan, err := mustParseFile(t, path).Edit().Set(tag.Title, "A Different Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasWarning(plan.Report().Warnings, wl.WarnLegacyConflict) {
		t.Errorf("expected a legacy-conflict warning; got %v", plan.Report().Warnings)
	}

	// --legacy strip removes the legacy container, so there is nothing to conflict.
	stripped, err := mustParseFile(t, path).Edit().Set(tag.Title, "A Different Title").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if reportHasWarning(stripped.Report().Warnings, wl.WarnLegacyConflict) {
		t.Errorf("--legacy strip should resolve the conflict, not warn; got %v", stripped.Report().Warnings)
	}

	// Setting TITLE to the value the legacy copy already holds does not conflict.
	same, err := mustParseFile(t, path).Edit().Set(tag.Title, "Sample Title").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if reportHasWarning(same.Report().Warnings, wl.WarnLegacyConflict) {
		t.Errorf("setting the legacy value verbatim should not warn; got %v", same.Report().Warnings)
	}

	// A multi-value edit that keeps the legacy value present is NOT a conflict: the
	// legacy ARTIST "Sample Artist" still appears in ARTIST=[Sample Artist, Extra].
	// Each legacy family entry is single-valued, so a naive slice-equality check would
	// falsely flag this (the regression this guards against); FamilySelected, which
	// tests presence in the whole edited set, does not.
	keepLegacy, err := mustParseFile(t, path).Edit().
		Set(tag.Artist, "Sample Artist").Add(tag.Artist, "Extra").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if reportHasWarning(keepLegacy.Report().Warnings, wl.WarnLegacyConflict) {
		t.Errorf("a multi-value edit keeping the legacy value should not warn; got %v", keepLegacy.Report().Warnings)
	}

	// Clearing a key the legacy container holds does not fire this warning: the native
	// key is then absent, which FamilySelected (like the linter) treats as no conflict.
	cleared, err := mustParseFile(t, path).Edit().Clear(tag.Title).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if reportHasWarning(cleared.Report().Warnings, wl.WarnLegacyConflict) {
		t.Errorf("clearing a key should not raise a legacy-conflict warning; got %v", cleared.Report().Warnings)
	}

	// A value the codec re-projects to the one already written is NOT a conflict: the
	// fixture's GENRE is "Rock", and GENRE=17 writes back as the numeric genre "Rock", so
	// judging against the plan's result tags (not the raw edited "17") raises no warning.
	// Comparing against the raw edited value would have falsely flagged it.
	genre, err := mustParseFile(t, path).Edit().Set(tag.Genre, "17").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if reportHasWarning(genre.Report().Warnings, wl.WarnLegacyConflict) {
		t.Errorf("a numeric genre re-projected to the existing value should not warn; got %v", genre.Report().Warnings)
	}
	// The same edit re-projects to the value already on disk, so it must read as an
	// immediate no-op: IsNoOp() and Changes() agree (#2), not a churning rewrite.
	if !genre.IsNoOp() {
		t.Error("GENRE=17 over an existing Rock: IsNoOp() = false, want true (re-projection is a no-op)")
	}
	if ch := genre.Changes(); len(ch) != 0 {
		t.Errorf("GENRE=17 over an existing Rock: Changes() = %v, want empty", ch)
	}

	// Editing a key the legacy container does not hold (a custom key) does not conflict.
	custom, err := mustParseFile(t, path).Edit().Set(tag.Key("MOOD"), "calm").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if reportHasWarning(custom.Report().Warnings, wl.WarnLegacyConflict) {
		t.Errorf("editing a non-legacy key should not warn; got %v", custom.Report().Warnings)
	}
}

// TestRejectNULInEditValues (D1): a NUL byte in a value the edit sets, in a chapter
// title, or in an added picture's description is refused at Prepare - a NUL silently
// truncates the field on a C-string format - rather than written and cut.
func TestRejectNULInEditValues(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)

	// A NUL in a --set value is rejected, and the message names the offending key.
	_, err := mustParseFile(t, path).Edit().Set(tag.Title, "a\x00b").Prepare()
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("NUL in set value: err = %v, want ErrInvalidData", err)
	}
	if err != nil && !strings.Contains(err.Error(), "NUL") {
		t.Errorf("NUL in set value: err = %v, want it to mention NUL", err)
	}

	// A NUL in any of an --add'ed multi-value is rejected too.
	if _, err := mustParseFile(t, path).Edit().Add(tag.Artist, "ok", "bad\x00name").Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("NUL in add value: err = %v, want ErrInvalidData", err)
	}

	// A NUL in an added picture's description is rejected (the PNG bytes are valid, so
	// only the description triggers it).
	if _, err := mustParseFile(t, path).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG(), Description: "desc\x00rest"}).
		Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("NUL in picture description: err = %v, want ErrInvalidData", err)
	}

	// A NUL in a chapter title is rejected on a chapter-capable format (Matroska), so
	// the NUL - not a missing-chapter-support gate - is the reason.
	if _, err := mustParseFile(t, sampleMKA).Edit().
		SetChapters(wl.Chapter{Start: 0, Title: "bad\x00title"}).Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("NUL in chapter title: err = %v, want ErrInvalidData", err)
	}

	// A normal edit (no NUL anywhere) is unaffected.
	if _, err := mustParseFile(t, path).Edit().Set(tag.Title, "clean").Prepare(); err != nil {
		t.Errorf("clean edit should still prepare, got: %v", err)
	}
}

// TestPictureMIMESniffReconcile (D3): on the embed path SniffAuthoritative lets a
// recognized image's bytes win over a caller-declared MIME/dimension that disagrees,
// so a mislabeled cover cannot be embedded under a contradicting MIME. On the read
// path SniffInto only fills empty fields, so a decoder keeps the MIME a container
// stored (no silent relabel of on-disk metadata). A failed sniff preserves the
// caller's MIME under both.
func TestPictureMIMESniffReconcile(t *testing.T) {
	// Embed path: PNG bytes wrongly declared JPEG with a bogus width - both corrected.
	embed := wl.Picture{Type: wl.PicFrontCover, MIME: "image/jpeg", Width: 999, Data: tinyPNG()}
	if !embed.SniffAuthoritative() {
		t.Fatal("tinyPNG should sniff as a recognized image")
	}
	if embed.MIME != "image/png" {
		t.Errorf("authoritative MIME = %q, want image/png (bytes win over the caller's lie)", embed.MIME)
	}
	if embed.Width != 1 || embed.Height != 1 {
		t.Errorf("authoritative dims = %dx%d, want 1x1 (sniff wins for a determined dimension)", embed.Width, embed.Height)
	}

	// Read path: a stored MIME that disagrees with the bytes is preserved (fill-only),
	// so dump/verify report what is on disk and an MP4 cover-format gate is not tripped
	// on an unrelated tags-only edit - while an empty field is still filled.
	read := wl.Picture{Type: wl.PicFrontCover, MIME: "image/jpeg", Data: tinyPNG()}
	if !read.SniffInto() {
		t.Fatal("tinyPNG should sniff as a recognized image")
	}
	if read.MIME != "image/jpeg" {
		t.Errorf("read-path MIME = %q, want the stored image/jpeg preserved", read.MIME)
	}
	if read.Width != 1 {
		t.Errorf("read-path Width = %d, want 1 filled from the sniff", read.Width)
	}

	// A failed sniff (junk bytes) leaves a caller-declared MIME intact under both.
	for _, authoritative := range []bool{true, false} {
		junk := wl.Picture{MIME: "image/heic", Data: []byte("not an image")}
		var ok bool
		if authoritative {
			ok = junk.SniffAuthoritative()
		} else {
			ok = junk.SniffInto()
		}
		if ok {
			t.Fatalf("authoritative=%v: junk bytes should not sniff", authoritative)
		}
		if junk.MIME != "image/heic" {
			t.Errorf("authoritative=%v: failed-sniff MIME = %q, want image/heic preserved", authoritative, junk.MIME)
		}
	}
}

// TestSaveBackRefusesReExecute (M2): executing the same plan with SaveBack twice
// fails the second time with a clear "already saved" message - not the confusing
// "source changed" the now-rewritten file would otherwise trigger - while a no-op
// SaveBack (which writes nothing) stays re-runnable.
func TestSaveBackRefusesReExecute(t *testing.T) {
	ctx := context.Background()
	plan, err := mustParseFile(t, copyToTemp(t, sampleFLAC)).Edit().Set(tag.Title, "Once").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatalf("first SaveBack: %v", err)
	}
	_, _, err = plan.Execute(ctx, wl.SaveBack())
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("second SaveBack err = %v, want ErrInvalidData", err)
	}
	if err != nil && !strings.Contains(err.Error(), "already saved") {
		t.Errorf("second SaveBack err = %v, want it to mention 'already saved'", err)
	}

	// A committed SaveBack spends the plan for EVERY destination, not just a second
	// SaveBack: re-reading the rewritten file with the original layout's segments would
	// corrupt the output, so SaveAsFile and WriteTo are refused too.
	if _, _, err := plan.Execute(ctx, wl.SaveAsFile(t.TempDir()+"/out.flac")); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("SaveAsFile after a committed SaveBack err = %v, want ErrInvalidData", err)
	}
	var buf bytes.Buffer
	if _, _, err := plan.Execute(ctx, wl.WriteTo(&buf, nil)); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("WriteTo after a committed SaveBack err = %v, want ErrInvalidData", err)
	}

	// A no-op SaveBack writes nothing, so re-running it stays valid (it never committed).
	noop, err := mustParseFile(t, copyToTemp(t, sampleFLAC)).Edit().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := noop.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatalf("first no-op SaveBack: %v", err)
	}
	if _, _, err := noop.Execute(ctx, wl.SaveBack()); err != nil {
		t.Errorf("re-running a no-op SaveBack should stay valid, got: %v", err)
	}
}

// TestUninitializedDocMessages (M6): the message papercuts report clearly - a zero
// Document's hash entry points, ParseFile(""), and a name-less Parse of
// unidentifiable bytes all give specific, actionable errors.
func TestUninitializedDocMessages(t *testing.T) {
	ctx := context.Background()
	var zero wl.Document
	if _, err := zero.HashAudioEssence(ctx); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("zeroDoc.HashAudioEssence err = %v, want it to mention 'not initialized'", err)
	}
	if _, err := zero.HashFile(ctx); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("zeroDoc.HashFile err = %v, want it to mention 'not initialized'", err)
	}
	// An empty path is a caller mistake, classified as ErrInvalidData (exit 4) like the
	// other nil/empty-input guards - deliberately not the fs.ErrNotExist a bare
	// os.Stat("") would have produced (an empty path is an invalid argument, not a
	// missing file). Pin the class so the deliberate change is not silently undone.
	if _, err := wl.ParseFile(ctx, ""); !errors.Is(err, waxerr.ErrInvalidData) || !strings.Contains(err.Error(), "input filename is empty") {
		t.Errorf("ParseFile(\"\") err = %v, want ErrInvalidData mentioning 'input filename is empty'", err)
	}
	// A name-less Parse of unidentifiable bytes names the source readably rather than
	// reporting `could not identify ""`.
	if _, err := wl.Parse(ctx, wl.BytesSource([]byte("not audio, no signature here"))); err == nil || !strings.Contains(err.Error(), "<unnamed input>") {
		t.Errorf("Parse(no name) err = %v, want it to mention '<unnamed input>'", err)
	}
}

// TestWriteToNilWriterRejected (B2): Plan.Execute with a WriteTo whose writer is nil
// returns a clean error instead of panicking on the first write deref.
func TestWriteToNilWriterRejected(t *testing.T) {
	src := readFixture(t, sampleFLAC)
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = plan.Execute(context.Background(), wl.WriteTo(nil, wl.BytesSource(src)))
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("WriteTo(nil): err = %v, want ErrInvalidData (and no panic)", err)
	}
}

// TestNoOpDowngradeOnReprojection (#2): when a codec re-projects an edit back to
// the value already on disk - a numeric genre (17 -> Rock), an integer track
// number (02 -> 2), or a dropped empty - the plan must read as an immediate no-op
// so IsNoOp() and Changes() agree. Before the fix the raw edit differed from base
// (so the fast-path no-op gate missed it) while the projected result equalled base
// (so Changes() was empty), and set/copy/lint --fix churned the file forever.
func TestNoOpDowngradeOnReprojection(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		key     tag.Key
		value   string
	}{
		{"mp3 numeric genre", sampleMP3, tag.Genre, "17"},       // GENRE=Rock; 17 projects to Rock
		{"aac numeric genre", sampleAAC, tag.Genre, "17"},       // GENRE=Rock; 17 projects to Rock
		{"aiff numeric genre", sampleAIFF, tag.Genre, "17"},     // GENRE=Rock; 17 projects to Rock
		{"mp4 integer track", sampleMP4, tag.TrackNumber, "02"}, // TRACKNUMBER=2; 02 projects to 2
		{"wav dropped empty", sampleWAV, tag.Copyright, ""},     // absent key, empty dropped by INFO
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := mustParseFile(t, tc.fixture).Edit().Set(tc.key, tc.value).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if !plan.IsNoOp() {
				t.Errorf("IsNoOp() = false, want true (the edit re-projects to the existing value)")
			}
			if ch := plan.Changes(); len(ch) != 0 {
				t.Errorf("Changes() = %v, want empty (must agree with IsNoOp)", ch)
			}
		})
	}
}

// TestNoOpDowngradeConvergesAndIsIdempotent (#2): a numeric-genre edit whose
// projection differs from the current value writes once; re-parsing and repeating
// the same edit is then a no-op whose WriteTo reproduces the source bytes exactly,
// identically across runs - no perpetual churn.
func TestNoOpDowngradeConvergesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := copyToTemp(t, sampleMP3) // GENRE=Rock

	// Seed a base whose genre differs from 17's projection (Rock), so the first
	// numeric-genre edit is a genuine write rather than an immediate no-op.
	seed, err := mustParseFile(t, path).Edit().Set(tag.Genre, "Jazz").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := seed.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	// GENRE=17 projects to "Rock" != "Jazz": a real change on the first pass.
	first, err := mustParseFile(t, path).Edit().Set(tag.Genre, "17").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if first.IsNoOp() {
		t.Fatal("first GENRE=17 over Jazz: IsNoOp() = true, want a real write")
	}
	if _, _, err := first.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatal(err)
	}

	// The file now reads GENRE=Rock; repeating GENRE=17 re-projects to Rock -> no-op.
	src := readFixture(t, path)
	conv, err := mustParseBytes(t, src).Edit().Set(tag.Genre, "17").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !conv.IsNoOp() {
		t.Error("converged GENRE=17: IsNoOp() = false, want true")
	}
	if ch := conv.Changes(); len(ch) != 0 {
		t.Errorf("converged GENRE=17: Changes() = %v, want empty", ch)
	}

	// A no-op WriteTo reproduces the source bytes exactly, identically across runs.
	var buf1, buf2 bytes.Buffer
	if _, _, err := conv.Execute(ctx, wl.WriteTo(&buf1, wl.BytesSource(src))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conv.Execute(ctx, wl.WriteTo(&buf2, wl.BytesSource(src))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Error("two WriteTo runs of the same no-op plan produced different bytes")
	}
	if !bytes.Equal(buf1.Bytes(), src) {
		t.Error("a no-op WriteTo did not reproduce the source bytes verbatim")
	}
}
