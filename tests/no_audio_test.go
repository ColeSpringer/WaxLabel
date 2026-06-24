package waxlabel_test

import (
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestNoAudioMP3RefusesHashAndWrite (H1): a non-empty text file named .mp3 parses
// (detected by extension) with no audio frames - the parser flags WarnNoAudioFrames
// even though it set a non-empty essence range over the text bytes. The library must
// refuse to hash, verify, or write it, so HashAudioEssence (and thus verify) and
// Editor.Prepare (and thus set/plan/lint --fix/copy-dest) all fail with ErrInvalidData
// rather than silently succeed over non-audio bytes - a no-audio file lints and verifies
// alike. empty.mp3 was already covered by the all-empty-range path; this is the
// non-empty-range case the digest guard formerly missed.
func TestNoAudioMP3RefusesHashAndWrite(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "notaudio.mp3", []byte("this is not audio, just plain text\n"))
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

// TestNoAudioAACRefusesHashAndWrite (Fix 3): a .aac file with no decodable ADTS
// frame must be flagged exactly like the MP3 case. The parser sets a non-empty
// essence range over the whole post-ID3 region but no whole frame decodes, so it
// raises WarnNoAudioFrames; the library then refuses to hash, verify, or write it
// (ErrInvalidData) instead of digesting non-audio bytes. Before Fix 3 the AAC
// parser stayed silent on every one of these and verify happily hashed the bytes.
//
// Three no-frame shapes share the one TotalSamples==0 gate:
//   - garbage: a long non-ADTS payload (detected as AAC only by the .aac extension);
//   - shorter-than-header: essence below the 7-byte ADTS header size;
//   - valid-header-zero-frames: a header that decodes (so the bytes self-detect as
//     AAC) but declares a frame the truncated body cannot complete - zero whole
//     frames counted.
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
		{"garbage", append([]byte("this is not audio, just text"), make([]byte, 4096)...)},
		{"shorter-than-header", []byte{0x01, 0x02, 0x03}},
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
				t.Fatalf("format = %v, want AAC (the .aac extension must still resolve)", doc.Format())
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
