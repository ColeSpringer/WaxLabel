package waxlabel_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// synthFLAC builds a minimal valid FLAC with STREAMINFO + PADDING + a little
// audio and no VORBIS_COMMENT block, so editing it exercises the "create a
// comment block where none existed" path.
func synthFLAC() []byte {
	return append(testfile.FLAC(testfile.FLACBlock(1, make([]byte, 8))), 0, 0)
}

func TestEditFileWithoutVorbisBlock(t *testing.T) {
	ctx := context.Background()
	data := synthFLAC()

	doc := mustParseBytes(t, data)
	if doc.Tags().Len() != 0 {
		t.Fatalf("synthetic file should have no tags, got %d", doc.Tags().Len())
	}

	plan, err := doc.Edit().Set(tag.Title, "Added From Scratch").Set(tag.Artist, "Synth").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("adding tags to a tagless file is not a no-op")
	}
	out := applyToBytes(t, data, plan)

	got := mustParseBytes(t, out)
	if got.Fields().Title != "Added From Scratch" {
		t.Errorf("Title = %q", got.Fields().Title)
	}
	if len(got.Fields().Artists) != 1 || got.Fields().Artists[0] != "Synth" {
		t.Errorf("Artists = %v", got.Fields().Artists)
	}
	// The audio frame must be preserved byte-for-byte.
	if e, err := got.HashAudioEssence(ctx, wl.WithHashSource(wl.BytesSource(out))); err == nil {
		base, _ := doc.HashAudioEssence(ctx, wl.WithHashSource(wl.BytesSource(data)))
		if !e.Equal(base) {
			t.Error("audio essence changed when adding a comment block")
		}
	}
}

func applyToBytes(t *testing.T, src []byte, plan *wl.Plan) []byte {
	t.Helper()
	var w writerTo
	if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(src))); err != nil {
		t.Fatal(err)
	}
	return w.b
}

func TestDocumentAccessorsAndEditorSurface(t *testing.T) {
	ctx := context.Background()
	doc := mustParseFile(t, sampleFLAC)

	if got := doc.Identity(); got.Size == 0 {
		t.Error("Identity().Size should be non-zero")
	}
	if doc.Native() == nil || len(doc.Native().Describe()) == 0 {
		t.Error("Native() should describe blocks")
	}
	if len(doc.Families()) == 0 {
		t.Error("Families() should be non-empty for a tagged file")
	}

	caps := doc.Capabilities()
	if caps.Format != wl.FormatFLAC || caps.ReadOnly {
		t.Errorf("FLAC caps = %+v, want writable FLAC", caps)
	}
	if caps.Field(tag.Title).Write != wl.AccessFull {
		t.Error("FLAC should fully support writing Title")
	}

	// HashFile (whole-file identity) differs from essence and is stable.
	src := readFixture(t, sampleFLAC)
	file, err := doc.HashFile(ctx, wl.WithHashSource(wl.BytesSource(src)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(file.String(), "sha256/whole-file-v1:") {
		t.Errorf("HashFile string = %q", file.String())
	}

	// Editor mutators: Add, Clear, picture add/remove/clear, native entries.
	ed := doc.Edit()
	ed.Add(tag.Genre, "Rock").Clear(tag.Comment)
	ed.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()})
	ed.RemovePictures(func(p wl.Picture) bool { return p.Type == wl.PicBackCover }) // removes nothing
	if entries := ed.Native().Entries(); len(entries) == 0 {
		t.Error("Native().Entries() should be non-empty")
	}
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	re := mustParseBytes(t, out)
	if g := re.Fields().Genres; len(g) == 0 || g[len(g)-1] != "Rock" {
		t.Errorf("Genre after Add = %v", re.Fields().Genres)
	}
	if len(re.Pictures()) != 1 {
		t.Errorf("expected 1 picture, got %d", len(re.Pictures()))
	}

	// ClearPictures removes them.
	plan2, _ := re.Edit().ClearPictures().Prepare()
	out2 := applyToBytes(t, out, plan2)
	if len(mustParseBytes(t, out2).Pictures()) != 0 {
		t.Error("ClearPictures should remove all pictures")
	}
}

// builds a FLAC carrying an APPLICATION block (the same preserve-verbatim path used for CUESHEET
// and SEEKTABLE) and confirms a tag edit leaves that block byte-for-byte intact.
func TestPreservesUnknownBlocks(t *testing.T) {
	marker := []byte("CUESHEET-STYLE-PRESERVE-ME-0xABCDEF")
	appBody := append([]byte("WAXX"), marker...) // APPLICATION: 4-byte id + data
	data := testfile.FLAC(testfile.FLACBlock(2, appBody), testfile.CommentBlock("test", "TITLE=Keep"),
		testfile.FLACBlock(1, make([]byte, 4)))

	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "Keep" {
		t.Fatalf("setup: Title = %q", doc.Fields().Title)
	}
	plan, err := doc.Edit().Set(tag.Title, "Changed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if !bytes.Contains(out, marker) {
		t.Error("APPLICATION block bytes were not preserved through a tag edit")
	}
	re := mustParseBytes(t, out)
	if re.Fields().Title != "Changed" {
		t.Errorf("Title = %q, want Changed", re.Fields().Title)
	}
	foundApp := false
	for _, e := range re.Native().Describe() {
		if e.Kind == "APPLICATION" {
			foundApp = true
		}
	}
	if !foundApp {
		t.Error("APPLICATION block missing from native view after edit")
	}
}

func TestEnumStrings(t *testing.T) {
	if wl.FormatFLAC.String() != "FLAC" {
		t.Errorf("FormatFLAC.String() = %q", wl.FormatFLAC.String())
	}
	if wl.PicFrontCover.String() != "Front cover" {
		t.Errorf("PicFrontCover.String() = %q", wl.PicFrontCover.String())
	}
	if wl.AccessFull.String() != "full" {
		t.Errorf("AccessFull.String() = %q", wl.AccessFull.String())
	}
	if wl.FamilyVorbis.String() != "vorbis" {
		t.Errorf("FamilyVorbis.String() = %q", wl.FamilyVorbis.String())
	}
	if wl.LegacyStrip.String() != "strip" {
		t.Errorf("LegacyStrip.String() = %q", wl.LegacyStrip.String())
	}
	// Out-of-range values fall back gracefully.
	if wl.PictureType(250).String() != "reserved" {
		t.Errorf("out-of-range PictureType = %q", wl.PictureType(250).String())
	}
	if wl.Format(200).String() != "unknown" {
		t.Errorf("out-of-range Format = %q", wl.Format(200).String())
	}
}
