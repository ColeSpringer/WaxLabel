package core

import "testing"

// TestProjectPicturesSniffs: display projection sniffs MIME; clone leaves originals untouched.
func TestProjectPicturesSniffs(t *testing.T) {
	gif := append([]byte("GIF89a"), 0x03, 0x00, 0x05, 0x00, 0x77, 0x00, 0x00)
	orig := []Picture{
		{Type: PicFrontCover, MIME: "image/png", Data: gif},
		{Type: PicFrontCover, MIME: "", Data: gif},
		{Type: PicFrontCover, MIME: "", Data: []byte("not image")},
	}
	want := []string{"image/gif", "image/gif", UnrecognizedMIME}
	proj := ProjectPictures(orig)
	for i := range want {
		if proj[i].MIME != want[i] {
			t.Errorf("ProjectPictures[%d] MIME = %q, want %q", i, proj[i].MIME, want[i])
		}
	}
	if orig[0].MIME != "image/png" || orig[1].MIME != "" {
		t.Errorf("ProjectPictures mutated its input: %q, %q", orig[0].MIME, orig[1].MIME)
	}
}
