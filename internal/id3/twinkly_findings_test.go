package id3

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// TestLooksLikeID3v1 covers the stricter trailing-ID3v1 detection: a genuine ID3v1/v1.1 tag
// passes, but audio bytes that merely begin with "TAG" at size-128 are rejected so a false
// positive can no longer pull the audio-essence boundary back 128 bytes.
func TestLooksLikeID3v1(t *testing.T) {
	real := make([]byte, 128)
	copy(real[0:3], "TAG")
	copy(real[3:33], "Title")
	copy(real[33:63], "Artist")
	copy(real[63:93], "Album")
	copy(real[93:97], "2020")
	copy(real[97:127], "A comment")
	real[127] = 13 // genre index
	if !LooksLikeID3v1(real) {
		t.Error("a genuine ID3v1 tag should be detected")
	}

	// ID3v1.1: b[125]==0 and b[126] is a binary track byte (3 falls in the rejected control
	// range). The carve-out must let it through, or a real v1.1 tag is a false negative.
	v11 := slices.Clone(real)
	v11[125], v11[126] = 0, 3
	if !LooksLikeID3v1(v11) {
		t.Error("a genuine ID3v1.1 tag with a binary track byte should be detected")
	}

	// A false positive: audio bytes that happen to start with "TAG". The low bytes populate
	// control bytes in the text fields and non-digits in the year field.
	fake := make([]byte, 128)
	copy(fake[0:3], "TAG")
	for i := 3; i < 128; i++ {
		fake[i] = byte(i)
	}
	if LooksLikeID3v1(fake) {
		t.Error("audio bytes merely starting with TAG must not be detected as ID3v1")
	}

	// A non-digit year alone is enough to reject an otherwise clean-looking block.
	badYear := slices.Clone(real)
	copy(badYear[93:97], "20x0")
	if LooksLikeID3v1(badYear) {
		t.Error("a non-digit year must be rejected")
	}

	// Wrong length or missing magic.
	if LooksLikeID3v1(real[:127]) {
		t.Error("a 127-byte block is not an ID3v1 tag")
	}
	notTag := slices.Clone(real)
	copy(notTag[0:3], "XXX")
	if LooksLikeID3v1(notTag) {
		t.Error("a block without the TAG magic is not an ID3v1 tag")
	}
}

// FuzzLooksLikeID3v1 asserts the strict detector never panics and stays a subset of the
// lenient display parser: any block it accepts must also ParseV1 successfully, so tightening
// detection never loses a tag ParseV1 would have shown.
func FuzzLooksLikeID3v1(f *testing.F) {
	real := make([]byte, 128)
	copy(real[0:3], "TAG")
	copy(real[93:97], "2020")
	f.Add(real)
	v11 := slices.Clone(real)
	v11[125], v11[126] = 0, 3
	f.Add(v11)
	fake := make([]byte, 128)
	copy(fake[0:3], "TAG")
	for i := 3; i < 128; i++ {
		fake[i] = byte(i)
	}
	f.Add(fake)
	f.Add(make([]byte, 128))
	f.Add([]byte("TAG"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if LooksLikeID3v1(b) {
			if _, ok := ParseV1(b); !ok {
				t.Errorf("LooksLikeID3v1 accepted a block ParseV1 rejects: %x", b)
			}
		}
	})
}

// TestProjectWarnsMalformedAPIC covers the malformed-cover read warning: an APIC frame whose
// body is too short to hold even a NUL-terminated MIME fails decodeAPIC, so Project must
// surface WarnInvalidPicture (which dump and lint then report) rather than dropping it
// silently. Every ID3-embedding codec (MP3/AAC/WAV/AIFF) flows through this one projector.
func TestProjectWarnsMalformedAPIC(t *testing.T) {
	tg := tagWith(4, []Frame{{ID: "APIC", Body: []byte("\x00image/png")}}) // no NUL after MIME
	proj := Project(tg)
	if len(proj.Pictures) != 0 {
		t.Fatalf("a malformed APIC must not project a picture, got %d", len(proj.Pictures))
	}
	found := false
	for _, w := range proj.Warnings {
		if w.Code == core.WarnInvalidPicture {
			found = true
		}
	}
	if !found {
		t.Errorf("expected WarnInvalidPicture, got %v", proj.Warnings)
	}

	// A well-formed APIC still projects a picture with no invalid-picture warning.
	good := tagWith(4, []Frame{{ID: "APIC", Body: encodeAPIC(core.Picture{Type: core.PicFrontCover, MIME: "image/png", Data: tinyPNGBytes()}, 4)}})
	gp := Project(good)
	if len(gp.Pictures) != 1 {
		t.Fatalf("a valid APIC should project one picture, got %d", len(gp.Pictures))
	}
	for _, w := range gp.Warnings {
		if w.Code == core.WarnInvalidPicture {
			t.Errorf("a valid APIC must not warn invalid-picture: %v", gp.Warnings)
		}
	}
}

// tinyPNGBytes is a minimal PNG signature + IHDR so the picture sniffer recognizes image/png.
func tinyPNGBytes() []byte {
	return []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")
}
