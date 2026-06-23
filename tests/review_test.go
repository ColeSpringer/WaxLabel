package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// flacWithTwoVC builds a FLAC with two VORBIS_COMMENT blocks (out of spec, but
// real files have it); only the first is projected into the canonical tags.
func flacWithTwoVC() []byte {
	out := []byte("fLaC")
	out = append(out, flacBlock(0, false, validStreamInfo())...)
	out = append(out, flacBlock(4, false, renderVC("TITLE=x"))...) // authoritative
	out = append(out, flacBlock(4, false, renderVC("ALBUM=y"))...) // extra
	out = append(out, flacBlock(1, true, make([]byte, 4))...)
	return append(out, 0xFF, 0xF8)
}

// flacBlock renders a metadata block (header + body).
func flacBlock(code byte, last bool, body []byte) []byte {
	h := []byte{code, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	if last {
		h[0] |= 0x80
	}
	return append(h, body...)
}

// validStreamInfo returns a 34-byte STREAMINFO (44100 Hz, 2ch, 16-bit).
func validStreamInfo() []byte {
	si := make([]byte, 34)
	si[0], si[1], si[2], si[3] = 0x10, 0x00, 0x10, 0x00
	si[10], si[11] = 0x0A, 0xC4
	si[12] = 0x40 | (1 << 1)
	si[13] = 15 << 4
	return si
}

// flacWithComments builds a FLAC carrying the given Vorbis comment entries.
func flacWithComments(entries ...string) []byte {
	out := []byte("fLaC")
	out = append(out, flacBlock(0, false, validStreamInfo())...)
	out = append(out, flacBlock(4, false, renderVC(entries...))...)
	out = append(out, flacBlock(1, true, make([]byte, 4))...)
	return append(out, 0xFF, 0xF8)
}

// #1: an oversized picture must be rejected, not silently truncated.
func TestPictureTooLargeRejected(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("TITLE=x"))
	big := make([]byte, 16<<20) // 16 MiB body exceeds the 24-bit block length
	big[0], big[1], big[2] = 0xFF, 0xD8, 0xFF
	_, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/jpeg", Data: big}).Prepare()
	if !errors.Is(err, waxerr.ErrPictureTooLarge) {
		t.Errorf("err = %v, want ErrPictureTooLarge", err)
	}
	// The size is humanized for the message (M2): "picture block is 16.0 MiB (max
	// 16.0 MiB)", not a raw byte count.
	if err != nil && !strings.Contains(err.Error(), "MiB") {
		t.Errorf("error should humanize the size, got %q", err.Error())
	}
}

// TestDiffSanitizesHostileKey: a custom Vorbis field name carrying control bytes
// reaches the tag model unvalidated (the comment parser splits only on '='), so
// Change.String must escape the KEY as well as the values - otherwise the diff
// command and the write-plan preview would leak the control bytes to the terminal.
func TestDiffSanitizesHostileKey(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("TITLE=x", "BAD\x1bKEY=v"))
	var line string
	for _, c := range tag.Diff(tag.NewTagSet(), doc.Tags()) {
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

// #2: a "TAG" sequence inside the metadata region must not be mistaken for a
// trailing ID3v1 tag (which would make the audio length negative).
func TestTrailingID3FalsePositiveGuard(t *testing.T) {
	ctx := context.Background()
	// PADDING body of 130 bytes with "TAG" at file offset size-128, well inside
	// the metadata region (audio is only 2 bytes).
	pad := make([]byte, 130)
	out := []byte("fLaC")
	out = append(out, flacBlock(0, false, validStreamInfo())...)
	// Padding block starts at offset 42 (4 + 4+34); its body starts at 46.
	out = append(out, flacBlock(1, true, pad)...)
	out = append(out, 0xFF, 0xF8) // 2 audio bytes
	// size = 4 + 38 + (4+130) + 2 = 178; size-128 = 50, which is body offset 4.
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

// #4: two native fields mapping to one canonical key with different values is a
// real conflict and must surface in the family view and Lint.
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

// A genuine multi-value (same field repeated) is not a conflict.
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

// #1: the structural fingerprint must be used in save-back so an in-place
// metadata edit that preserves size, mtime, and inode is still caught.
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

// #2: a key set present-but-empty (which Vorbis cannot store) must be absent in
// the returned Document, matching a re-parse of the written bytes.
func TestPresentEmptyKeyAbsentInResult(t *testing.T) {
	data := readFixture(t, sampleFLAC) // has Title="Original Title"
	doc := mustParseBytes(t, data)

	plan, err := doc.Edit().Set(tag.Title).Prepare() // Set with no values
	if err != nil {
		t.Fatal(err)
	}
	var buf writerTo
	resDoc, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	_, inResult := resDoc.Get(tag.Title)
	_, inReparse := mustParseBytes(t, buf.b).Get(tag.Title)
	if inResult != inReparse {
		t.Errorf("result Title present=%v but re-parse present=%v - plan and write disagree", inResult, inReparse)
	}
	if inResult {
		t.Error("present-but-empty Title should be absent in the written result")
	}
}

// #3: an invalid key must be rejected at Prepare, not written verbatim.
func TestInvalidKeyRejectedOnWrite(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("TITLE=x"))
	if _, err := doc.Edit().Set(tag.Key("ARTIST=HACK"), "v").Prepare(); !errors.Is(err, waxerr.ErrInvalidKey) {
		t.Errorf("err = %v, want ErrInvalidKey", err)
	}
}

// #4: a picture-only edit must preserve extra VORBIS_COMMENT blocks; a tag edit
// collapses them (the documented behavior).
func TestExtraVorbisBlocksPreservedOnNonTagEdit(t *testing.T) {
	data := flacWithTwoVC()

	t.Run("picture-only preserves extras", func(t *testing.T) {
		doc := mustParseBytes(t, data)
		plan, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if !bytes.Contains(out, []byte("ALBUM=y")) {
			t.Error("picture-only edit dropped the extra comment block's tags")
		}
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

// #5: SaveAsFile to a new path should produce a conventional 0644 file, not the
// 0600 that os.CreateTemp leaves.
func TestSaveAsFilePermissions(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	out := filepath.Join(t.TempDir(), "out.flac")
	plan, _ := doc.Edit().Set(tag.Title, "x").Prepare()
	if _, _, err := plan.Execute(context.Background(), wl.SaveAsFile(out)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("new file mode = %o, want 0644", perm)
	}
}
