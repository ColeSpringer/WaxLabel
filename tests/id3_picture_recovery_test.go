package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"
)

// TestPictureDescriptionTerminatorMissingRecovers: a PIC or APIC frame that omits the
// description terminator puts the image where the description should end; the image's own
// NUL bytes must not become the split point. The description reads empty and the image is
// whole.
func TestPictureDescriptionTerminatorMissingRecovers(t *testing.T) {
	png := tinyPNG()
	cases := []struct {
		name string
		file []byte
	}{
		{"v2.2 PIC", slices.Concat(id3v2(2, frame22("PIC", slices.Concat([]byte{0}, []byte("PNG"), []byte{3}, png))), mp3Audio(t))},
		{"v2.4 APIC", mp3WithFrames(t, id3Frame(4, "APIC", slices.Concat([]byte{0}, []byte("image/png\x00"), []byte{3}, png)))},
		{"v2.4 APIC UTF-16", mp3WithFrames(t, id3Frame(4, "APIC", slices.Concat([]byte{1}, []byte("image/png\x00"), []byte{3}, png)))},
		{"v2.3 APIC", slices.Concat(id3v2(3, id3Frame(3, "APIC", slices.Concat([]byte{0}, []byte("image/png\x00"), []byte{3}, png))), mp3Audio(t))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseBytes(t, c.file)
			pics := doc.Pictures()
			if len(pics) != 1 {
				t.Fatalf("pictures = %d, want 1 (warnings %v)", len(pics), doc.Warnings())
			}
			if pics[0].Description != "" || !bytes.Equal(pics[0].Data, png) || pics[0].MIME != "image/png" {
				t.Errorf("picture = %q %s %d bytes, want an empty description and the whole PNG", pics[0].Description, pics[0].MIME, len(pics[0].Data))
			}
		})
	}
}

// TestPictureDescriptionTerminatedStillSplitsThere: the recovery never overrides a real
// terminator, so a described picture keeps its description.
func TestPictureDescriptionTerminatedStillSplitsThere(t *testing.T) {
	png := tinyPNG()
	file := mp3WithFrames(t, id3Frame(4, "APIC", slices.Concat([]byte{0}, []byte("image/png\x00"), []byte{3}, []byte("Front\x00"), png)))
	pics := mustParseBytes(t, file).Pictures()
	if len(pics) != 1 || pics[0].Description != "Front" || !bytes.Equal(pics[0].Data, png) {
		t.Errorf("pictures = %+v", pics)
	}
}
