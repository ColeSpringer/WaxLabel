package waxlabel_test

import (
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestNoAudioMP3RefusesHashAndWrite checks that an MP3 selected by a leading ID3v2
// tag but carrying non-MPEG bytes refuses hashing and writes. The parser reports
// WarnNoAudioFrames over a non-empty essence range; HashAudioEssence, verify, and
// Editor.Prepare all fail with ErrInvalidData instead of treating those bytes as audio.
func TestNoAudioMP3RefusesHashAndWrite(t *testing.T) {
	ctx := context.Background()
	data := append(id3v2(4, textFrame(4, "TIT2", "x")), []byte("this is not audio, just plain text\n")...)
	path := writeTempFile(t, "notaudio.mp3", data)
	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !hasWarning(doc, wl.WarnNoAudioFrames) {
		t.Fatal("expected a no-audio warning on a text .mp3")
	}
	// HashAudioEssence refuses (the verify command inherits this).
	if _, err := doc.HashAudioEssence(ctx); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("HashAudioEssence err = %v, want ErrInvalidData", err)
	}
	// Editor.Prepare refuses (set/plan, lint --fix, and a copy's destination editor all
	// funnel through it, so they inherit the guard at one site).
	if _, err := doc.Edit().Set(tag.Title, "Y").Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("Prepare err = %v, want ErrInvalidData", err)
	}
}

// TestNoAudioAACRefusesHashAndWrite checks that an AAC file selected by a valid ADTS
// header but containing no whole frame is treated like the MP3 no-audio case. The
// parser reports WarnNoAudioFrames and hashing, verify, and writes fail with
// ErrInvalidData instead of digesting non-audio bytes.
//
// Under content-only detection only a genuine ADTS signature reaches the AAC codec.
// Non-ADTS garbage is unsupported; the valid-but-truncated header below still
// self-detects and exercises the zero-frame gate.
func TestNoAudioAACRefusesHashAndWrite(t *testing.T) {
	ctx := context.Background()

	// adtsHeaderOnly builds a single 7-byte ADTS fixed header (no CRC) at 44.1 kHz
	// stereo that decodes - so the bytes are sniffed as AAC - but declares frameLen
	// bytes. With no payload supplied, the frame runs past EOF and the walk counts
	// zero whole frames, leaving TotalSamples at zero.
	adtsHeaderOnly := func(frameLen int) []byte {
		b := make([]byte, 7)
		b[0] = 0xFF                                // syncword high
		b[1] = 0xF1                                // sync low | MPEG-4 | layer 00 | no CRC
		b[2] = 0x50                                // profile=LC(1), sfIndex=4 (44100), chan hi=0
		b[3] = byte(2<<6) | byte((frameLen>>11)&3) // chanConfig=2 | frame_length hi
		b[4] = byte((frameLen >> 3) & 0xFF)        // frame_length mid
		b[5] = byte((frameLen & 7) << 5)           // frame_length lo | buffer fullness
		return b                                   // b[6] stays zero
	}

	cases := []struct {
		name string
		data []byte
	}{
		{"valid-header-zero-frames", adtsHeaderOnly(2000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempFile(t, "g.aac", tc.data)
			doc, err := wl.ParseFile(ctx, path)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if doc.Format() != wl.FormatAAC {
				t.Fatalf("format = %v, want AAC (the ADTS header must self-detect)", doc.Format())
			}
			if !hasWarning(doc, wl.WarnNoAudioFrames) {
				t.Fatal("expected a no-audio warning on a frameless .aac")
			}
			// verify inherits this: HashAudioEssence refuses rather than digesting bytes.
			if _, err := doc.HashAudioEssence(ctx); !errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("HashAudioEssence err = %v, want ErrInvalidData", err)
			}
			// set/plan/lint --fix all funnel through Prepare, which refuses too.
			if _, err := doc.Edit().Set(tag.Title, "Y").Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("Prepare err = %v, want ErrInvalidData", err)
			}
		})
	}
}
