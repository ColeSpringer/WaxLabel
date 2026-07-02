package waxlabel_test

import (
	"bytes"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// unrecognizedMIME mirrors core.UnrecognizedMIME (not re-exported at the root package): the
// MIME a picture reads under when its bytes are not a recognized image header.
const unrecognizedMIME = "application/octet-stream"

// covrItemAtom builds an MP4 covr ilst item holding one image data atom under the given covr
// type code (0 = implicit, 13 = JPEG, 14 = PNG, 27 = BMP).
func covrItemAtom(typ uint32, data []byte) []byte {
	return mp4Atom("covr", mp4Data(typ, data))
}

// apicBody builds a well-formed APIC frame body: Latin-1 encoding, NUL-terminated MIME,
// picture type, NUL-terminated (empty) description, then the image bytes.
func apicBody(mime string, ptype byte, data []byte) []byte {
	b := []byte{0x00} // encoding 0 = Latin-1
	b = append(b, mime...)
	b = append(b, 0x00, ptype, 0x00) // MIME terminator, type, empty-description terminator
	return append(b, data...)
}

// TestMP4CoverSniffedAuthoritatively (H1): the covr type code no longer dictates the MIME.
// A PNG under the implicit type 0 - or mislabeled under the JPEG type 13 - reads image/png
// because the bytes win; an unrecognizable implicit cover reads honestly as the unrecognized
// MIME rather than the old manufactured image/jpeg.
func TestMP4CoverSniffedAuthoritatively(t *testing.T) {
	for _, c := range []struct {
		name string
		typ  uint32
		data []byte
		want string
	}{
		{"implicit type 0 PNG", 0, tinyPNG(), "image/png"},
		{"implicit type 0 JPEG", 0, tinyJPEG(), "image/jpeg"},
		{"implicit type 0 GIF", 0, tinyGIF(), "image/gif"},
		{"JPEG type 13 over PNG bytes", 13, tinyPNG(), "image/png"},
		{"unrecognizable implicit cover", 0, []byte("not an image"), unrecognizedMIME},
	} {
		doc := mustParseBytes(t, mp4Tagged(covrItemAtom(c.typ, c.data)))
		pics := doc.Pictures()
		if len(pics) != 1 {
			t.Fatalf("%s: expected 1 picture, got %d", c.name, len(pics))
		}
		if pics[0].MIME != c.want {
			t.Errorf("%s: MIME = %q, want %q", c.name, pics[0].MIME, c.want)
		}
	}
}

// TestMP4CarriedCoverPreservedOnTagOnlyEdit (H1 write-side guard gap): a tag-only edit must
// not re-encode a carried cover through coverType's JPEG default. A GIF stored under the
// implicit covr type 0 (now read as image/gif) is carried verbatim on a --set TITLE edit - no
// format error - and its bytes and type code survive, so a reparse still reads image/gif
// rather than a JPEG type code stamped over GIF bytes.
func TestMP4CarriedCoverPreservedOnTagOnlyEdit(t *testing.T) {
	src := mp4Tagged(mp4Text("\xa9nam", "Before"), covrItemAtom(0, tinyGIF()))
	doc := mustParseBytes(t, src)
	if pics := doc.Pictures(); len(pics) != 1 || pics[0].MIME != "image/gif" {
		t.Fatalf("parse: pictures = %v, want one image/gif", pics)
	}
	plan, err := doc.Edit().Set(tag.Title, "After").Prepare()
	if err != nil {
		t.Fatalf("tag-only edit of a file with an unsupported carried cover must not error: %v", err)
	}
	re := mustParseBytes(t, applyToBytes(t, src, plan))
	if re.Fields().Title != "After" {
		t.Errorf("title = %q, want After", re.Fields().Title)
	}
	pics := re.Pictures()
	if len(pics) != 1 || pics[0].MIME != "image/gif" {
		t.Fatalf("after tag edit: pictures = %v, want the GIF carried verbatim as image/gif", pics)
	}
	if !bytes.Equal(pics[0].Data, tinyGIF()) {
		t.Error("carried cover bytes changed")
	}
}

// TestMP4PictureChangeRejectsCarriedUnsupportedCover: once the read fix makes a GIF read as
// image/gif, a genuine picture change on a file carrying it trips checkCoverFormats (which
// runs only under a picture change) instead of silently writing a JPEG type code over GIF
// bytes. Adding a second cover changes the set, so the carried GIF is re-validated and
// rejected.
func TestMP4PictureChangeRejectsCarriedUnsupportedCover(t *testing.T) {
	src := mp4Tagged(covrItemAtom(0, tinyGIF()))
	doc := mustParseBytes(t, src)
	_, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicBackCover, Data: tinyJPEG()}).Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("picture change with a carried GIF cover: err = %v, want ErrUnsupportedTag", err)
	}
}

// TestID3BlankMIMESniffed (H1/M11): a blank-MIME APIC reads the type its bytes imply (bytes
// win over the old blank->"image/" coercion); over unrecognizable bytes it reads the
// unrecognized MIME rather than "image/".
func TestID3BlankMIMESniffed(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"blank MIME over JPEG bytes", tinyJPEG(), "image/jpeg"},
		{"blank MIME over PNG bytes", tinyPNG(), "image/png"},
		{"blank MIME over junk", []byte("not an image"), unrecognizedMIME},
	} {
		frame := apicFrameRaw(apicBody("", 3, c.data))
		doc := mustParseBytes(t, append(id3v2(3, frame), mp3Audio(t)...))
		pics := doc.Pictures()
		if len(pics) != 1 {
			t.Fatalf("%s: expected 1 picture, got %d", c.name, len(pics))
		}
		if pics[0].MIME != c.want {
			t.Errorf("%s: MIME = %q, want %q", c.name, pics[0].MIME, c.want)
		}
	}
}

// TestID3MislabeledAPICBytesWin (H1): a JPEG declared as image/png reads back as image/jpeg -
// the recognizable bytes override the declared MIME, matching the authoritative read path.
func TestID3MislabeledAPICBytesWin(t *testing.T) {
	frame := apicFrameRaw(apicBody("image/png", 3, tinyJPEG()))
	doc := mustParseBytes(t, append(id3v2(3, frame), mp3Audio(t)...))
	pics := doc.Pictures()
	if len(pics) != 1 || pics[0].MIME != "image/jpeg" {
		t.Fatalf("pictures = %v, want one image/jpeg (bytes win over the declared image/png)", pics)
	}
}

// TestID3BlankMIMEUnrecognizedRoundTrips (M11): after the read fix a blank-MIME APIC over
// unrecognizable bytes reads as the unrecognized MIME and, on a later edit, round-trips to
// that same explicit MIME (not the old blank->"image/") - a stable, consistent round-trip.
func TestID3BlankMIMEUnrecognizedRoundTrips(t *testing.T) {
	frame := apicFrameRaw(apicBody("", 3, []byte("still not an image")))
	src := append(id3v2(3, frame), mp3Audio(t)...)
	doc := mustParseBytes(t, src)
	if pics := doc.Pictures(); len(pics) != 1 || pics[0].MIME != unrecognizedMIME {
		t.Fatalf("parse: pictures = %v, want one %s", doc.Pictures(), unrecognizedMIME)
	}
	plan, err := doc.Edit().Set(tag.Title, "After").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	re := mustParseBytes(t, applyToBytes(t, src, plan))
	pics := re.Pictures()
	if len(pics) != 1 || pics[0].MIME != unrecognizedMIME {
		t.Fatalf("re-read: pictures = %v, want one %s preserved", pics, unrecognizedMIME)
	}
}

// TestMP4MalformedCoverNotDuplicatedOnEdit checks that a covr whose payload is not a valid data
// atom, and so fails to decode (owned==false), is not duplicated on a tag-only edit.
// preservedItems already carries such an item verbatim, so the cover-carry path (covrItems) must
// not also return it by name, or the edit would append it twice and grow it on every later edit.
func TestMP4MalformedCoverNotDuplicatedOnEdit(t *testing.T) {
	malformedCovr := mp4Atom("covr", []byte("not a valid data atom"))
	src := mp4Tagged(mp4Text("\xa9nam", "Before"), malformedCovr)
	if pics := mustParseBytes(t, src).Pictures(); len(pics) != 0 {
		t.Fatalf("malformed covr should decode to 0 pictures, got %d", len(pics))
	}
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "After").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if n := bytes.Count(out, []byte("covr")); n != 1 {
		t.Fatalf("covr atom count = %d after one tag edit, want 1 (no duplication)", n)
	}
	// A second edit on the output must keep it at one (no exponential growth).
	plan2, err := mustParseBytes(t, out).Edit().Set(tag.Title, "Again").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(applyToBytes(t, out, plan2), []byte("covr")); n != 1 {
		t.Fatalf("covr atom count = %d after a second edit, want 1", n)
	}
}

// TestMatroskaAttachmentSniffedAuthoritatively (Cluster B, third site): a Matroska attachment
// declared image/png but carrying JPEG bytes reads back image/jpeg - recognizable bytes win,
// matching the ID3/MP4 read paths.
func TestMatroskaAttachmentSniffedAuthoritatively(t *testing.T) {
	att := mkEl(idAttachments, mkEl(idAttached, concat(
		mkStr(idFileName, "cover.png"),
		mkStr(idFileMime, "image/png"),
		mkEl(idFileData, tinyJPEG()),
	)))
	file := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, att))
	pics := mustParseBytes(t, file).Pictures()
	if len(pics) != 1 || pics[0].MIME != "image/jpeg" {
		t.Fatalf("pictures = %v, want one image/jpeg (bytes win over declared image/png)", pics)
	}
}
