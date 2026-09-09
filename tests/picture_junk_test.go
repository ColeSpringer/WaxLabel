package waxlabel_test

import (
	"bytes"
	"os"
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// headlessPNG is a PNG with its signature and first IHDR byte removed: bytes no decoder can
// read, under whatever label a container declares for them.
func headlessPNG() []byte { return tinyPNG()[9:] }

// TestJunkPictureUnderDeclaredMIMEDegrades: a declared image/png over undecodable bytes reads
// as the unrecognized MIME with no dimensions, on every container that stores a label, and
// lint flags it, matching what the write path already does for the same bytes.
func TestJunkPictureUnderDeclaredMIMEDegrades(t *testing.T) {
	cases := []struct {
		name string
		file []byte
	}{
		{"id3 APIC", mp3WithFrames(t, id3Frame(4, "APIC", apicBody("image/png", 3, headlessPNG())))},
		{"flac PICTURE", flacWithCommentBlock(nil, wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Width: 16, Height: 16, Depth: 24, Data: headlessPNG()})},
		{"mp4 covr", mp4Tagged(covrItemAtom(14, headlessPNG()))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseBytes(t, c.file)
			pics := doc.Pictures()
			if len(pics) != 1 {
				t.Fatalf("pictures = %d, want 1", len(pics))
			}
			p := pics[0]
			if p.MIME != unrecognizedMIME || p.Width != 0 || p.Height != 0 || p.Depth != 0 {
				t.Errorf("picture = %s %dx%dx%d, want %s with no dimensions", p.MIME, p.Width, p.Height, p.Depth, unrecognizedMIME)
			}
			found := false
			for _, f := range doc.Lint() {
				if f.Code == "invalid-picture" {
					found = true
				}
			}
			if !found {
				t.Errorf("lint should flag the junk cover: %v", doc.Lint())
			}
		})
	}
}

// TestJunkPictureNeverRelabeledOnTransfer: the effective MIME the writers and transfer gates
// see is the unrecognized one, so a junk cover is never carried into a new container under
// the type its old label claimed. A destination whose covers must be a known image format
// drops it; one that stores any cover keeps the bytes, but under the honest label.
func TestJunkPictureNeverRelabeledOnTransfer(t *testing.T) {
	src := mustParseBytes(t, mp3WithFrames(t, id3Frame(4, "APIC", apicBody("image/png", 3, headlessPNG()))))

	// MP4's covr atom can only label JPEG, PNG or BMP, so there is no honest way to store it.
	rep, err := src.PlanTransfer(wl.FormatMP4)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, dropped := pictureCounts(rep); dropped != 1 {
		t.Errorf("junk cover should be dropped on transfer into MP4, report = %+v", rep)
	}

	// FLAC stores any cover MIME, so the bytes carry - and the block that lands must declare
	// the unrecognized type, never the image/png the source lied with.
	dstBytes := flacWithComments("TITLE=x")
	plan, rep, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatal(err)
	}
	if carried, _, _ := pictureCounts(rep); carried != 1 {
		t.Fatalf("junk cover should carry into FLAC, report = %+v", rep)
	}
	pics := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Pictures()
	if len(pics) != 1 || pics[0].MIME != unrecognizedMIME {
		t.Errorf("transferred cover = %+v, want one stored under %s", pics, unrecognizedMIME)
	}
	if bytes.Contains(applyToBytes(t, dstBytes, plan), []byte("image/png")) {
		t.Error("the written FLAC must not carry the source's image/png label for junk bytes")
	}
}

// TestLinkPictureSurvivesPictureEdit: a picture whose MIME is the "-->" URL-link sentinel
// declares that its payload is an address, not image bytes, so the sniff must leave it alone.
// Degrading it would rewrite the link as a broken cover the moment any picture edit re-renders
// the frame - the ID3 writer rebuilds every APIC from the picture set, so a lost sentinel is
// lost on disk.
func TestLinkPictureSurvivesPictureEdit(t *testing.T) {
	const url = "http://example.com/cover.png"
	for _, c := range []struct {
		name string
		file []byte
	}{
		{"id3 APIC", mp3WithFrames(t, id3Frame(4, "APIC", apicBody(wl.LinkMIME, 3, []byte(url))))},
		{"flac PICTURE", flacWithCommentBlock(nil, wl.Picture{Type: wl.PicFrontCover, MIME: wl.LinkMIME, Data: []byte(url)})},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseBytes(t, c.file)
			pics := doc.Pictures()
			if len(pics) != 1 || pics[0].MIME != wl.LinkMIME || string(pics[0].Data) != url {
				t.Fatalf("read pictures = %+v, want one %s picture holding the URL", pics, wl.LinkMIME)
			}
			// An edit that re-renders the picture set: the link must come back out intact.
			plan, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicBackCover, Data: tinyPNG()}).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			got := mustParseBytes(t, applyToBytes(t, c.file, plan)).Pictures()
			if len(got) != 2 {
				t.Fatalf("after adding a cover: %d pictures, want 2", len(got))
			}
			if got[0].MIME != wl.LinkMIME || string(got[0].Data) != url {
				t.Errorf("link picture = %+v, want %s holding the URL unchanged", got[0], wl.LinkMIME)
			}
			if got[1].MIME != "image/png" {
				t.Errorf("added cover = %+v, want image/png", got[1])
			}
		})
	}
}

// TestUnsniffableCoverCarriesIntoMatroska: Matroska stores a cover whose bytes the sniff
// cannot identify as an octet-stream attachment under the cover-art name, and reads it back
// as a picture, so its capability has to say so. Grading it unrepresentable would throw away
// a valid cover in a format the sniff does not know rather than carry it, now that a stored
// label no longer speaks for bytes nothing can decode.
func TestUnsniffableCoverCarriesIntoMatroska(t *testing.T) {
	ico := []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x10, 0x10, 0x00, 0x00, 0x01, 0x00, 0x20, 0x00, 0x68, 0x04}
	src := mustParseBytes(t, flacWithCommentBlock(nil,
		wl.Picture{Type: wl.PicFrontCover, MIME: "image/x-icon", Width: 16, Height: 16, Data: ico}))

	dstBytes, err := os.ReadFile(notagsMKA)
	if err != nil {
		t.Fatal(err)
	}
	plan, rep, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatal(err)
	}
	if carried, _, dropped := pictureCounts(rep); carried != 1 || dropped != 0 {
		t.Fatalf("the cover should carry into Matroska, report = %+v", rep)
	}
	pics := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Pictures()
	if len(pics) != 1 || !bytes.Equal(pics[0].Data, ico) {
		t.Errorf("read back %+v, want the cover bytes intact", pics)
	}
}
