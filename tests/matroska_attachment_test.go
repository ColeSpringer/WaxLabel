package waxlabel_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
)

const (
	idFileDesc = 0x467E
	idFileUID  = 0x46AE
)

// mkAttached builds an AttachedFile; desc, when not nil, is written as a FileDescription.
func mkAttached(name, mime string, desc *string, data []byte, uid uint64) []byte {
	body := concat(mkStr(idFileName, name), mkStr(idFileMime, mime))
	if desc != nil {
		body = concat(body, mkStr(idFileDesc, *desc))
	}
	return testfile.El(idAttached, body, testfile.El(idFileData, data), testfile.Uint(idFileUID, uid))
}

// bookletFile holds an image named as no cover with an empty description, a text file, and an
// image of a type the sniffer does not know.
func bookletFile() (src, booklet, notes, logo []byte) {
	empty := ""
	booklet = mkAttached("Booklet page 1.jpg", "image/jpeg", &empty, testfile.JPEG(), 0x1122334455)
	notes = mkAttached("notes.txt", "text/plain", nil, []byte("liner notes"), 7)
	logo = mkAttached("logo.svg", "image/svg+xml", nil, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0x99)
	return testfile.Matroska("matroska", "x", testfile.El(idAttachments, booklet, notes, logo)), booklet, notes, logo
}

func pictureMIMEs(pics []wl.Picture) []string {
	out := make([]string, len(pics))
	for i, p := range pics {
		out[i] = p.MIME
	}
	return out
}

func pictureTypes(pics []wl.Picture) []wl.PictureType {
	out := make([]wl.PictureType, len(pics))
	for i, p := range pics {
		out[i] = p.Type
	}
	return out
}

// An image attachment reads under the type it declares where its bytes name none.
func TestMatroskaDeclaredImageTypeStands(t *testing.T) {
	src, _, _, _ := bookletFile()
	pics := mustParseBytes(t, src).Pictures()
	if got, want := pictureMIMEs(pics), []string{"image/jpeg", "image/svg+xml"}; !slices.Equal(got, want) {
		t.Errorf("picture types = %q, want %q", got, want)
	}
}

// Adding a cover leaves every attachment the file holds as it is, in its place: name, type,
// description, UID. Only the new cover is named by the cover-art convention.
func TestMatroskaAddedCoverKeepsOtherAttachments(t *testing.T) {
	src, booklet, notes, logo := bookletFile()
	doc := mustParseBytes(t, src)
	out, _ := saveMatroska(t, src, doc.Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()}))

	if !bytes.Contains(out, concat(booklet, notes, logo)) {
		t.Error("the three attachments are not carried byte for byte, in order")
	}
	if n := bytes.Count(out, []byte("cover.png")); n != 1 {
		t.Errorf("cover.png names = %d, want 1", n)
	}
	if bytes.Contains(out, []byte("small_cover")) {
		t.Error("an attachment the edit left alone was renamed small_cover")
	}
	re := mustParseBytes(t, out).Pictures()
	if got, want := pictureMIMEs(re), []string{"image/jpeg", "image/svg+xml", "image/png"}; !slices.Equal(got, want) {
		t.Errorf("picture types = %q, want %q", got, want)
	}
	if got, want := pictureTypes(re), []wl.PictureType{wl.PicOther, wl.PicOther, wl.PicFrontCover}; !slices.Equal(got, want) {
		t.Errorf("picture roles = %v, want %v", got, want)
	}
}

// Removing one picture leaves the rest where they stand.
func TestMatroskaRemovedPictureKeepsOtherAttachments(t *testing.T) {
	src, booklet, notes, logo := bookletFile()
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().
		RemovePictures(func(p wl.Picture) bool { return p.MIME == "image/jpeg" }))
	if bytes.Contains(out, booklet) {
		t.Error("the removed picture's attachment is still in the file")
	}
	if !bytes.Contains(out, concat(notes, logo)) {
		t.Error("the attachments the edit kept are not carried byte for byte, in order")
	}
	if got, want := pictureMIMEs(mustParseBytes(t, out).Pictures()), []string{"image/svg+xml"}; !slices.Equal(got, want) {
		t.Errorf("picture types = %q, want %q", got, want)
	}
}

// The pictures read back in the order the edit gave them: one taken out and added again stands
// behind the other, and both keep their attachments.
func TestMatroskaPictureOrderFollowsTheEdit(t *testing.T) {
	src, booklet, notes, logo := bookletFile()
	doc := mustParseBytes(t, src)
	page := doc.Pictures()[0]
	out, _ := saveMatroska(t, src, doc.Edit().
		RemovePictures(func(p wl.Picture) bool { return p.MIME == "image/jpeg" }).
		AddPicture(page))

	for _, raw := range [][]byte{booklet, notes, logo} {
		if !bytes.Contains(out, raw) {
			t.Error("an attachment the edit kept is not carried byte for byte")
		}
	}
	re := mustParseBytes(t, out).Pictures()
	if got, want := pictureMIMEs(re), []string{"image/svg+xml", "image/jpeg"}; !slices.Equal(got, want) {
		t.Errorf("picture types = %q, want %q", got, want)
	}
}

// A picture that takes another's place stands ahead of the kept one behind it.
func TestMatroskaReplacedPictureKeepsOtherAttachments(t *testing.T) {
	src, booklet, notes, logo := bookletFile()
	front := wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()}
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().
		ReplacePictures(func(p wl.Picture) bool { return p.MIME == "image/jpeg" }, front))
	if bytes.Contains(out, booklet) {
		t.Error("the replaced picture's attachment is still in the file")
	}
	if !bytes.Contains(out, notes) || !bytes.Contains(out, logo) {
		t.Error("an attachment the edit kept is not carried byte for byte")
	}
	re := mustParseBytes(t, out).Pictures()
	if got, want := pictureMIMEs(re), []string{"image/png", "image/svg+xml"}; !slices.Equal(got, want) {
		t.Errorf("picture types = %q, want %q", got, want)
	}
}

// An image attachment with no FileData is no picture, and a picture edit keeps it.
func TestMatroskaImageAttachmentWithoutDataIsKept(t *testing.T) {
	bare := testfile.El(idAttached, mkStr(idFileName, "missing.png"), mkStr(idFileMime, "image/png"), testfile.Uint(idFileUID, 5))
	src := testfile.Matroska("matroska", "x", testfile.El(idAttachments, bare))
	doc := mustParseBytes(t, src)
	if n := len(doc.Pictures()); n != 0 {
		t.Fatalf("pictures = %d, want none", n)
	}
	out, _ := saveMatroska(t, src, doc.Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()}))
	if !bytes.Contains(out, bare) {
		t.Error("the attachment without data is not carried into the rewritten element")
	}
}

// ffmpeg attaches a file under the name and type it is given; after a cover is added ffprobe
// reads the same name and type from it.
func TestMatroskaAddedCoverKeepsAttachmentForFFprobe(t *testing.T) {
	requireTool(t, "ffmpeg")
	requireTool(t, "ffprobe")
	dir := t.TempDir()
	page := filepath.Join(dir, "page.jpg")
	if err := os.WriteFile(page, testfile.JPEG(), 0o644); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(dir, "built.mka")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", sampleMKA,
		"-map", "0:a", "-c", "copy", "-attach", page,
		"-metadata:s:t:0", "mimetype=image/jpeg", "-metadata:s:t:0", "filename=Booklet page 1.jpg", built)
	if log, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, log)
	}
	src, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()}))

	type stream struct {
		CodecType string            `json:"codec_type"`
		Tags      map[string]string `json:"tags"`
	}
	var probe struct {
		Streams []stream `json:"streams"`
	}
	raw, err := exec.Command("ffprobe", "-v", "error", "-of", "json", "-show_streams", writeTempFile(t, "out.mka", out)).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range probe.Streams {
		if name := lookupCI(s.Tags, "filename"); name != "" {
			names = append(names, name+" "+lookupCI(s.Tags, "mimetype"))
		}
	}
	if want := []string{"Booklet page 1.jpg image/jpeg", "cover.png image/png"}; !slices.Equal(names, want) {
		t.Errorf("ffprobe attachments = %q, want %q", names, want)
	}
}

// A copy carries a picture as the source holds it: one of a type the sniffer cannot verify keeps
// the type it declares, so a copy of a file onto itself changes nothing and a copy onto another
// file reads back the same pictures.
func TestMatroskaCopyKeepsAnUnverifiablePictureType(t *testing.T) {
	src, _, _, _ := bookletFile()
	doc := mustParseBytes(t, src)
	plan, report, err := doc.Transfer().Prepare(mustParseBytes(t, src))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() {
		t.Errorf("a copy of the file onto itself plans a write: %v (report %+v)", plan.Report().Operations, report.Items)
	}

	dstBytes := readFixture(t, notagsMKA)
	plan, _, err = doc.Transfer().Prepare(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, dstBytes, plan)
	got := mustParseBytes(t, out).Pictures()
	if want := []string{"image/jpeg", "image/svg+xml"}; !slices.Equal(pictureMIMEs(got), want) {
		t.Errorf("copied picture types = %q, want %q", pictureMIMEs(got), want)
	}
	// The attachment written for the SVG is named as one.
	if !bytes.Contains(out, []byte("small_cover.svg")) || bytes.Contains(out, []byte("small_cover_1.jpg")) {
		t.Error("the SVG is not written as small_cover.svg")
	}
}

// Two attachments holding the same picture are two pictures: an edit keeps each in its own
// attachment and does not write one of them twice.
func TestMatroskaDuplicatePicturesKeepTheirAttachments(t *testing.T) {
	first := mkAttached("scan a.jpg", "image/jpeg", nil, testfile.JPEG(), 0xA1)
	second := mkAttached("scan b.jpg", "image/jpeg", nil, testfile.JPEG(), 0xB2)
	src := testfile.Matroska("matroska", "x", testfile.El(idAttachments, first, second))
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()}))
	if !bytes.Contains(out, concat(first, second)) {
		t.Error("the two attachments are not both carried byte for byte, in order")
	}
	if got, want := pictureMIMEs(mustParseBytes(t, out).Pictures()), []string{"image/jpeg", "image/jpeg", "image/png"}; !slices.Equal(got, want) {
		t.Errorf("picture types = %q, want %q", got, want)
	}
}

// A new cover takes a name no attachment the edit keeps holds, picture or not, and a picture is
// matched to the attachment that reads as it, not to a file that is no picture standing ahead.
func TestMatroskaNewCoverNameAvoidsKeptAttachments(t *testing.T) {
	notes := mkAttached("cover.png", "text/plain", nil, []byte("not a picture, whatever its name"), 7)
	cover := mkAttached("cover_1.png", "image/png", nil, testfile.PNG(), 0xC0)
	src := testfile.Matroska("matroska", "x", testfile.El(idAttachments, notes, cover))
	if n := len(mustParseBytes(t, src).Pictures()); n != 1 {
		t.Fatalf("setup: pictures = %d, want the PNG alone", n)
	}
	other := append(testfile.PNG(), 0xAA)
	out, _ := saveMatroska(t, src, mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: other}))
	if !bytes.Contains(out, concat(notes, cover)) {
		t.Error("the text file and the cover the edit keeps are not carried byte for byte, in order")
	}
	for name, want := range map[string]int{"cover.png": 1, "cover_1.png": 1, "cover_2.png": 1} {
		if n := bytes.Count(out, []byte(name)); n != want {
			t.Errorf("attachments named %s = %d, want %d: the new cover is cover_2.png", name, n, want)
		}
	}
}
