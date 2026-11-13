package waxlabel_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// unrecognizedMIME is the MIME a picture reads under when its bytes are not a recognized
// image header, kept as a short local name for the assertions across this package.
const unrecognizedMIME = wl.UnrecognizedMIME

// covrItemAtom builds an MP4 covr ilst item holding one image data atom under the given covr
// type code (0 = implicit, 13 = JPEG, 14 = PNG, 27 = BMP).
func covrItemAtom(typ uint32, data []byte) []byte {
	return testfile.MP4Atom("covr", testfile.MP4Data(typ, data))
}

// apicBody builds a well-formed APIC frame body: Latin-1 encoding, NUL-terminated MIME,
// picture type, NUL-terminated (empty) description, then the image bytes.
func apicBody(mime string, ptype byte, data []byte) []byte {
	b := []byte{0x00} // encoding 0 = Latin-1
	b = append(b, mime...)
	b = append(b, 0x00, ptype, 0x00) // MIME terminator, type, empty-description terminator
	return append(b, data...)
}

// the sniffed bytes decide the MIME, not the covr type code.
func TestMP4CoverSniffedAuthoritatively(t *testing.T) {
	for _, c := range []struct {
		name string
		typ  uint32
		data []byte
		want string
	}{
		{"implicit type 0 PNG", 0, testfile.PNG(), "image/png"},
		{"implicit type 0 JPEG", 0, testfile.JPEG(), "image/jpeg"},
		{"implicit type 0 GIF", 0, testfile.GIF(), "image/gif"},
		{"JPEG type 13 over PNG bytes", 13, testfile.PNG(), "image/png"},
		{"unrecognizable implicit cover", 0, []byte("not an image"), unrecognizedMIME},
	} {
		doc := mustParseBytes(t, testfile.MP4Tagged(covrItemAtom(c.typ, c.data)))
		pics := doc.Pictures()
		if len(pics) != 1 {
			t.Fatalf("%s: expected 1 picture, got %d", c.name, len(pics))
		}
		if pics[0].MIME != c.want {
			t.Errorf("%s: MIME = %q, want %q", c.name, pics[0].MIME, c.want)
		}
	}
}

// a tag-only edit must not re-encode a carried cover through coverType's JPEG default.
func TestMP4CarriedCoverPreservedOnTagOnlyEdit(t *testing.T) {
	src := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "Before"), covrItemAtom(0, testfile.GIF()))
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
	if !bytes.Equal(pics[0].Data, testfile.GIF()) {
		t.Error("carried cover bytes changed")
	}
}

// a picture change on a file carrying a GIF cover trips checkCoverFormats (which runs only under
// a picture change) instead of writing a JPEG type code over GIF bytes.
func TestMP4PictureChangeRejectsCarriedUnsupportedCover(t *testing.T) {
	src := testfile.MP4Tagged(covrItemAtom(0, testfile.GIF()))
	doc := mustParseBytes(t, src)
	_, err := doc.Edit().AddPicture(wl.Picture{Type: wl.PicBackCover, Data: testfile.JPEG()}).Prepare()
	if !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("picture change with a carried GIF cover: err = %v, want ErrUnsupportedTag", err)
	}
}

// blank-MIME APIC reads the type its bytes imply; over unrecognizable bytes it reads the
// unrecognized MIME, not "image/".
func TestID3BlankMIMESniffed(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"blank MIME over JPEG bytes", testfile.JPEG(), "image/jpeg"},
		{"blank MIME over PNG bytes", testfile.PNG(), "image/png"},
		{"blank MIME over junk", []byte("not an image"), unrecognizedMIME},
	} {
		frame := testfile.ID3Frame(3, "APIC", apicBody("", 3, c.data))
		doc := mustParseBytes(t, append(testfile.ID3v2(3, frame), mp3Audio(t)...))
		pics := doc.Pictures()
		if len(pics) != 1 {
			t.Fatalf("%s: expected 1 picture, got %d", c.name, len(pics))
		}
		if pics[0].MIME != c.want {
			t.Errorf("%s: MIME = %q, want %q", c.name, pics[0].MIME, c.want)
		}
	}
}

// JPEG declared as image/png reads back as image/jpeg; the recognizable bytes override the declared
// MIME, matching the authoritative read path.
func TestID3MislabeledAPICBytesWin(t *testing.T) {
	frame := testfile.ID3Frame(3, "APIC", apicBody("image/png", 3, testfile.JPEG()))
	doc := mustParseBytes(t, append(testfile.ID3v2(3, frame), mp3Audio(t)...))
	pics := doc.Pictures()
	if len(pics) != 1 || pics[0].MIME != "image/jpeg" {
		t.Fatalf("pictures = %v, want one image/jpeg (bytes win over the declared image/png)", pics)
	}
}

// blank-MIME APIC over unrecognizable bytes reads as the unrecognized MIME and, on a later edit,
// round-trips to that same explicit MIME.
func TestID3BlankMIMEUnrecognizedRoundTrips(t *testing.T) {
	frame := testfile.ID3Frame(3, "APIC", apicBody("", 3, []byte("still not an image")))
	src := append(testfile.ID3v2(3, frame), mp3Audio(t)...)
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

// covr whose payload is not a valid data atom (owned==false) is not duplicated on a tag-only edit.
// preservedItems carries such an item verbatim, so the cover-carry path (covrItems) must not also
// return it by name, or every edit would append it again.
func TestMP4MalformedCoverNotDuplicatedOnEdit(t *testing.T) {
	malformedCovr := testfile.MP4Atom("covr", []byte("not a valid data atom"))
	src := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "Before"), malformedCovr)
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

// FLAC native PICTURE block declared image/png but carrying GIF bytes reads back image/gif;
// recognizable bytes win, matching the ID3/MP4/Matroska read paths.
func TestFLACPictureSniffedAuthoritatively(t *testing.T) {
	src := flacWithCommentBlock(nil, wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.GIF()})
	pics := mustParseBytes(t, src).Pictures()
	if len(pics) != 1 || pics[0].MIME != "image/gif" {
		t.Fatalf("pictures = %v, want one image/gif (bytes win over the declared image/png)", pics)
	}
}

// the read-path sniff is a projection only, so a no-op write on a FLAC whose native PICTURE is
// mislabeled must be byte-identical (the block is cloned verbatim, not re-emitted from the sniffed
// MIME), and a title-only edit must leave the stored MIME on disk while the read view still
// reports the sniffed type.
func TestFLACMislabeledPictureNoOpFidelity(t *testing.T) {
	src := flacWithCommentBlock(nil, wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.GIF()})

	noop, err := mustParseBytes(t, src).Edit().Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if out := applyToBytes(t, src, noop); !bytes.Equal(out, src) {
		t.Errorf("no-op on a mislabeled-picture FLAC changed bytes: %d -> %d", len(src), len(out))
	}

	// A title-only edit keeps the picture block verbatim: the stored image/png survives and the
	// sniffed image/gif never leaks into the written bytes.
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "After").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Contains(out, []byte("image/png")) {
		t.Error("a title-only edit rewrote the picture's stored MIME (image/png no longer on disk)")
	}
	if bytes.Contains(out, []byte("image/gif")) {
		t.Error("the sniffed image/gif leaked into the written PICTURE block")
	}
	if pics := mustParseBytes(t, out).Pictures(); len(pics) != 1 || pics[0].MIME != "image/gif" {
		t.Fatalf("after the title edit: pictures = %v, want one image/gif (read projection unchanged)", pics)
	}
}

// a FLAC cover stored as a base64 METADATA_BLOCK_PICTURE comment reads as its sniffed type
// (image/gif), but a tag-only edit must materialize it into a native block with its stored MIME
// (image/png). The sniff is a display projection and must not leak into the written bytes.
func TestFLACCommentCoverMIMENotRewrittenOnEdit(t *testing.T) {
	pic := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.GIF()} // mislabeled on disk
	comment := vorbis.Comment{Name: "METADATA_BLOCK_PICTURE", Value: base64.StdEncoding.EncodeToString(vorbis.RenderPicture(pic))}
	src := flacWithCommentBlock([]vorbis.Comment{comment})

	if pics := mustParseBytes(t, src).Pictures(); len(pics) != 1 || pics[0].MIME != "image/gif" {
		t.Fatalf("read: pics = %v, want one image/gif (projection sniffed)", pics)
	}
	plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "After").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Contains(out, []byte("image/png")) {
		t.Error("a tag-only edit rewrote the stored comment-cover MIME (image/png no longer on disk)")
	}
	if bytes.Contains(out, []byte("image/gif")) {
		t.Error("the sniffed image/gif leaked into the written comment cover")
	}
	if pics := mustParseBytes(t, out).Pictures(); len(pics) != 1 || pics[0].MIME != "image/gif" {
		t.Fatalf("re-read: pics = %v, want one image/gif (projection unchanged)", pics)
	}
}

// same for a picture-set edit: adding a second, different cover must not rewrite a pre-existing
// mislabeled cover's stored MIME.
func TestFLACPictureSetEditPreservesUntouchedCoverMIME(t *testing.T) {
	pic := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.GIF()} // mislabeled on disk
	comment := vorbis.Comment{Name: "METADATA_BLOCK_PICTURE", Value: base64.StdEncoding.EncodeToString(vorbis.RenderPicture(pic))}
	src := flacWithCommentBlock([]vorbis.Comment{comment})

	// Add a second, different cover; a picturesChanged edit that never touches the first cover.
	plan, err := mustParseBytes(t, src).Edit().AddPicture(wl.Picture{Type: wl.PicBackCover, Data: testfile.JPEG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, src, plan)
	if !bytes.Contains(out, []byte("image/png")) {
		t.Error("a picture-set edit rewrote the untouched cover's stored MIME (image/png gone)")
	}
	if bytes.Contains(out, []byte("image/gif")) {
		t.Error("the sniffed image/gif leaked into the untouched cover on a picture-set edit")
	}
	// Both covers read back: the pre-existing one still projects its true type, the added JPEG too.
	pics := mustParseBytes(t, out).Pictures()
	if len(pics) != 2 {
		t.Fatalf("expected 2 covers after the add, got %d", len(pics))
	}
	var gotGIF, gotJPEG bool
	for _, p := range pics {
		gotGIF = gotGIF || p.MIME == "image/gif"
		gotJPEG = gotJPEG || p.MIME == "image/jpeg"
	}
	if !gotGIF || !gotJPEG {
		t.Errorf("read projection = %v, want the mislabeled cover as image/gif and the added one as image/jpeg", pics)
	}
}

// a Matroska attachment declared image/png but carrying JPEG bytes reads back image/jpeg;
// recognizable bytes win, matching the ID3/MP4 read paths.
func TestMatroskaAttachmentSniffedAuthoritatively(t *testing.T) {
	att := testfile.El(idAttachments, testfile.El(idAttached, concat(
		mkStr(idFileName, "cover.png"),
		mkStr(idFileMime, "image/png"),
		testfile.El(idFileData, testfile.JPEG()),
	)))
	file := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, att))
	pics := mustParseBytes(t, file).Pictures()
	if len(pics) != 1 || pics[0].MIME != "image/jpeg" {
		t.Fatalf("pictures = %v, want one image/jpeg (bytes win over declared image/png)", pics)
	}
}

// An image type declared for bytes of no format the sniffer knows stands in every container: the
// picture reads under it, lint does not call it invalid, and a copy of the file onto itself
// changes nothing. A type the sniffer knows, or a spelling of one, declared for other bytes still
// reads as unrecognized.
func TestUnverifiableImageTypeStandsInEveryContainer(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	// The size a FLAC block declares for such a picture stands with its type.
	pic := wl.Picture{Type: wl.PicFrontCover, MIME: "image/svg+xml", Width: 512, Height: 512, Depth: 24, Data: svg}
	files := map[string][]byte{
		"MP3":  append(testfile.ID3v2(3, testfile.ID3Frame(3, "APIC", apicBody("image/svg+xml", 3, svg))), mp3Audio(t)...),
		"FLAC": flacWithCommentBlock([]vorbis.Comment{{Name: "TITLE", Value: "x"}}, pic),
	}
	for name, data := range files {
		doc := mustParseBytes(t, data)
		if got := pictureMIMEs(doc.Pictures()); !slices.Equal(got, []string{"image/svg+xml"}) {
			t.Errorf("%s: picture types = %q, want image/svg+xml", name, got)
		}
		if p := doc.Pictures()[0]; name == "FLAC" && (p.Width != 512 || p.Height != 512 || p.Depth != 24) {
			t.Errorf("%s: picture size = %dx%dx%d, want the 512x512x24 the block declares", name, p.Width, p.Height, p.Depth)
		}
		for _, f := range doc.Lint() {
			if f.Code == "invalid-picture" {
				t.Errorf("%s: lint calls the picture invalid: %s", name, f.Message)
			}
		}
		plan, _, err := doc.Transfer().Prepare(mustParseBytes(t, data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !plan.IsNoOp() {
			t.Errorf("%s: a copy of the file onto itself plans a write: %v", name, plan.Report().Operations)
		}
	}

	for _, declared := range []string{"image/jpeg", "image/jpg", "image/x-png", "image/pjpeg", "image/x-ms-bmp", "image/tif", "IMAGE/PNG; charset=binary"} {
		frame := testfile.ID3Frame(3, "APIC", apicBody(declared, 3, []byte("not an image")))
		pics := mustParseBytes(t, append(testfile.ID3v2(3, frame), mp3Audio(t)...)).Pictures()
		if len(pics) != 1 || pics[0].MIME != unrecognizedMIME {
			t.Errorf("%s over bytes that are no image: pictures = %q, want one unrecognized", declared, pictureMIMEs(pics))
		}
	}
}
