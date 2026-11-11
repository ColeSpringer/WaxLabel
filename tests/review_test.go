package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// flacWithTwoVC builds a FLAC with two VORBIS_COMMENT blocks (out of spec, but real files have it);
// only the first is projected into the canonical tags, and any rewrite collapses the extras to a
// single block (the multiple-vorbis-comment lint promises this).
func flacWithTwoVC() []byte {
	return testfile.FLAC(testfile.CommentBlock("test", "TITLE=x"), testfile.CommentBlock("test", "ALBUM=y"),
		testfile.FLACBlock(1, make([]byte, 4)))
}

// large existing padding region is reused exactly by splitting it across multiple legal FLAC
// padding blocks, under a policy with no Max. The default policy's Max caps what it keeps of the
// region; neither is the padding-clamped case, which is about a requested size.
func TestFLACReuseLargePaddingNoSpuriousClamp(t *testing.T) {
	data := testfile.FLAC(testfile.CommentBlock("test", "TITLE=x"),
		testfile.FLACBlock(1, make([]byte, 10<<20)), testfile.FLACBlock(1, make([]byte, 10<<20))) // two ~10 MiB PADDING blocks

	for name, tc := range map[string]struct {
		opts    []wl.WriteOption
		wantLen int
	}{
		"no Max":      {[]wl.WriteOption{wl.WithPadding(wl.PaddingPolicy{Target: 8192, ReuseInPlace: true})}, len(data)},
		"the default": {nil, len(data) - (20<<20 + 4) + 1<<20},
	} {
		plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "y").Prepare(tc.opts...)
		if err != nil {
			t.Fatalf("%s: prepare: %v", name, err)
		}
		for _, w := range plan.Report().Warnings {
			if w.Code == wl.WarnPaddingClamped {
				t.Errorf("%s: an in-place edit of a large-padding FLAC must not warn padding-clamped; got %v", name, plan.Report().Warnings)
			}
		}
		out := applyToBytes(t, data, plan)
		if len(out) != tc.wantLen {
			t.Errorf("%s: out=%d bytes, want %d (in=%d)", name, len(out), tc.wantLen, len(data))
		}
		if got := mustParseBytes(t, out).Fields().Title; got != "y" {
			t.Errorf("%s: title after edit = %q, want y", name, got)
		}
	}
}

// flacWithComments builds a FLAC carrying the given Vorbis comment entries.
func flacWithComments(entries ...string) []byte {
	return testfile.TaggedFLAC("test", entries...)
}

// An oversized picture must be rejected, not silently truncated.
func TestPictureTooLargeRejected(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("TITLE=x"))
	big := make([]byte, 16<<20) // 16 MiB body exceeds the 24-bit block length
	copy(big, testfile.JPEG())  // complete JPEG header for the image-recognition gate
	_, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/jpeg", Data: big}).Prepare()
	if !errors.Is(err, waxerr.ErrPictureTooLarge) {
		t.Fatalf("err = %v, want ErrPictureTooLarge", err)
	}
	// The sizes are humanized for the message, and this close to the limit, where both round to
	// 16.0 MiB, each carries its byte count so that the message does not read "16.0 MiB (max 16.0 MiB)".
	for _, want := range []string{"16.0 MiB (16777258 bytes)", "16.0 MiB (16777215 bytes)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err.Error(), want)
		}
	}
}

// Change.String must escape the KEY as well as the values, or diff and the write-plan preview would
// leak control bytes. The Vorbis reader drops such a name on projection, so this drives the
// sanitizer directly at the tag layer.
func TestDiffSanitizesHostileKey(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Add(tag.Title, "x")
	edited.Add(tag.Key("BAD\x1bKEY"), "v")
	var line string
	for _, c := range tag.Diff(tag.NewTagSet(), edited) {
		if strings.HasPrefix(string(c.Key), "BAD") {
			line = c.String()
		}
	}
	if line == "" {
		t.Fatal("expected a change for the hostile custom key")
	}
	if strings.Contains(line, "\x1b") {
		t.Errorf("Change.String leaked a raw ESC from the key: %q", line)
	}
	if !strings.Contains(line, `\x1b`) {
		t.Errorf("Change.String should escape the key's control byte: %q", line)
	}
}

// A "TAG" sequence inside the metadata region must not be mistaken for a trailing
// ID3v1 tag, which would make the audio length negative.
func TestTrailingID3FalsePositiveGuard(t *testing.T) {
	ctx := context.Background()
	// PADDING body of 130 bytes with "TAG" at file offset size-128, well inside
	// the metadata region (audio is only 2 bytes).
	// The padding block starts at offset 42 (4 + 4+34), its body at 46. With 2 audio bytes,
	// size = 4 + 38 + (4+130) + 2 = 178; size-128 = 50, which is body offset 4.
	out := testfile.FLAC(testfile.FLACBlock(1, make([]byte, 130)))
	copy(out[50:], "TAG")

	doc, err := wl.Parse(ctx, wl.BytesSource(out))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnTrailingID3v1 {
			t.Error("metadata-region TAG wrongly detected as trailing ID3v1")
		}
	}
	// Essence must be hashable (extent end >= start) and a no-op write must
	// reproduce the bytes (no negative copy length).
	if _, err := doc.HashAudioEssence(ctx, wl.WithHashSource(wl.BytesSource(out))); err != nil {
		t.Errorf("HashAudioEssence: %v", err)
	}
	plan, _ := doc.Edit().Prepare()
	var buf bytes.Buffer
	if _, _, err := plan.Execute(ctx, wl.WriteTo(&buf, wl.BytesSource(out))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), out) {
		t.Error("no-op write did not reproduce the input")
	}
}

// Two native fields mapping to one canonical key with different values are a
// conflict and must surface in the family view and Lint.
func TestConflictingFamiliesDetected(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("DATE=2020", "YEAR=2019"))

	conflict := false
	for _, f := range doc.Families() {
		if f.Key == tag.RecordingDate && !f.Selected {
			conflict = true
		}
	}
	if !conflict {
		t.Error("DATE vs YEAR should be an unselected (conflicting) family")
	}
	if !findingCodes(doc.Lint())["conflicting-families"] {
		t.Error("Lint should report conflicting-families")
	}
}

// A multi-value (same field repeated) is not a conflict.
func TestRepeatedFieldIsNotConflict(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("ARTIST=A", "ARTIST=B"))
	for _, f := range doc.Families() {
		if f.Key == tag.Artist && !f.Selected {
			t.Error("repeated ARTIST is a multi-value, not a conflict")
		}
	}
	if findingCodes(doc.Lint())["conflicting-families"] {
		t.Error("multi-value ARTIST wrongly flagged as conflicting")
	}
}

// The structural fingerprint must be used in save-back so an in-place metadata edit
// that preserves size, mtime, and inode is still caught.
func TestFingerprintDetectsInPlaceMetadataChange(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, path)
	if !doc.Identity().HasFinger {
		t.Skip("no structural fingerprint recorded")
	}

	// Flip one byte inside the metadata region (offset 30 is within STREAMINFO),
	// keeping the length identical, then restore the recorded mtime.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], 30); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xFF
	if _, err := f.WriteAt(b[:], 30); err != nil {
		t.Fatal(err)
	}
	f.Close()
	rec := time.Unix(0, doc.Identity().ModTimeUnixNano)
	if err := os.Chtimes(path, rec, rec); err != nil {
		t.Fatal(err)
	}

	plan, _ := doc.Edit().Set(tag.Title, "x").Prepare()
	_, _, err = plan.Execute(context.Background(), wl.SaveBack())
	if !errors.Is(err, waxerr.ErrSourceChanged) {
		t.Errorf("err = %v, want ErrSourceChanged (fingerprint must catch the in-place edit)", err)
	}
}

// A key set with no values (present but empty, which Vorbis cannot store) is absent from the
// written file, here the fixture's TITLE.
func TestPresentEmptyKeyNotWritten(t *testing.T) {
	data := readFixture(t, sampleFLAC)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mustParseBytes(t, applyToBytes(t, data, plan)).Get(tag.Title); ok {
		t.Error("a present-but-empty Title should be absent from the written file")
	}
}

// An invalid key must be rejected at Prepare, not written verbatim.
func TestInvalidKeyRejectedOnWrite(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("TITLE=x"))
	if _, err := doc.Edit().Set(tag.Key("ARTIST=HACK"), "v").Prepare(); !errors.Is(err, waxerr.ErrInvalidKey) {
		t.Errorf("err = %v, want ErrInvalidKey", err)
	}
}

// Any rewrite collapses extra VORBIS_COMMENT blocks to one, as the multiple-vorbis-comment lint
// promises. It holds for tag edits, which re-render the block, and for picture-only, padding-only,
// and legacy-strip edits, where every change flag is false yet the de-dup must still fire.
func TestExtraVorbisBlocksCollapsedOnAnyEdit(t *testing.T) {
	data := flacWithTwoVC()

	// assertSingleVC checks that the extra block's ALBUM=y is gone and the first block's
	// TITLE=x survives.
	assertSingleVC := func(t *testing.T, out []byte) {
		t.Helper()
		if bytes.Contains(out, []byte("ALBUM=y")) {
			t.Error("edit did not collapse the extra comment block (ALBUM=y still present)")
		}
		if !bytes.Contains(out, []byte("TITLE=x")) {
			t.Error("edit dropped the authoritative first comment block (TITLE=x missing)")
		}
	}

	t.Run("picture-only collapses extras", func(t *testing.T) {
		doc := mustParseBytes(t, data)
		plan, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		assertSingleVC(t, applyToBytes(t, data, plan))
	})

	t.Run("padding-only collapses extras", func(t *testing.T) {
		doc := mustParseBytes(t, data)
		// A padding grow is an edit that sets no change flag, so it exercises the de-dup guard.
		plan, err := doc.Edit().Prepare(wl.WithPadding(wl.PaddingPolicy{Target: 4096}))
		if err != nil {
			t.Fatal(err)
		}
		if plan.IsNoOp() {
			t.Fatal("a padding grow should not be a no-op")
		}
		assertSingleVC(t, applyToBytes(t, data, plan))
	})

	t.Run("legacy-strip collapses extras", func(t *testing.T) {
		// A trailing ID3v1 makes the legacy strip an edit with no comment/vendor/picture
		// change, the third all-flags-false path.
		src := withTrailingID3v1(data)
		doc := mustParseBytes(t, src)
		plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
		if err != nil {
			t.Fatal(err)
		}
		if plan.IsNoOp() {
			t.Fatal("stripping a present trailing ID3v1 should not be a no-op")
		}
		assertSingleVC(t, applyToBytes(t, src, plan))
	})

	t.Run("tag edit collapses extras", func(t *testing.T) {
		doc := mustParseBytes(t, data)
		plan, _ := doc.Edit().Set(tag.Title, "z").Prepare()
		out := applyToBytes(t, data, plan)
		if bytes.Contains(out, []byte("ALBUM=y")) {
			t.Error("tag edit should collapse extra comment blocks")
		}
		if mustParseBytes(t, out).Fields().Title != "z" {
			t.Error("tag edit lost the new title")
		}
	})
}
