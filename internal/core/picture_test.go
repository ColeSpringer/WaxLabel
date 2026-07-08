package core

import "testing"

// TestProjectPicturesSniffs covers the display-projection half: ProjectPictures lets
// each picture's own bytes decide its MIME (matching the id3/mp4/matroska read projection), so a GIF
// mislabeled image/png shows image/gif and a junk/blank cover degrades to UnrecognizedMIME (which
// lint flags) - all on an independent clone (Data shared, read-only) that leaves the caller's
// stored originals - the re-serialization source - untouched.
func TestProjectPicturesSniffs(t *testing.T) {
	gif := append([]byte("GIF89a"), 0x03, 0x00, 0x05, 0x00, 0x77, 0x00, 0x00)
	orig := []Picture{
		{Type: PicFrontCover, MIME: "image/png", Data: gif},        // mislabeled -> image/gif
		{Type: PicFrontCover, MIME: "", Data: gif},                 // blank -> image/gif
		{Type: PicFrontCover, MIME: "", Data: []byte("not image")}, // junk -> unrecognized
	}
	want := []string{"image/gif", "image/gif", UnrecognizedMIME}
	proj := ProjectPictures(orig)
	for i := range want {
		if proj[i].MIME != want[i] {
			t.Errorf("ProjectPictures[%d] MIME = %q, want %q", i, proj[i].MIME, want[i])
		}
	}
	// The originals (the re-serialization source) must be untouched.
	if orig[0].MIME != "image/png" || orig[1].MIME != "" {
		t.Errorf("ProjectPictures mutated its input: %q, %q", orig[0].MIME, orig[1].MIME)
	}
}
