package waxlabel_test

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestWarnOversizedChunkIsPublic checks that oversized-chunk warnings are re-exported for
// external callers and emitted by a real WAV parse.
func TestWarnOversizedChunkIsPublic(t *testing.T) {
	le := binary.LittleEndian
	chunk := func(id string, declared uint32, body []byte) []byte {
		h := make([]byte, 8)
		copy(h, id)
		le.PutUint32(h[4:], declared)
		return append(h, body...)
	}
	fmtBody := make([]byte, 16)
	le.PutUint16(fmtBody[0:], 1)     // PCM
	le.PutUint16(fmtBody[2:], 1)     // mono
	le.PutUint32(fmtBody[4:], 8000)  // sample rate
	le.PutUint32(fmtBody[8:], 16000) // byte rate
	le.PutUint16(fmtBody[12:], 2)    // block align
	le.PutUint16(fmtBody[14:], 16)   // bits
	body := append([]byte("WAVE"), chunk("fmt ", 16, fmtBody)...)
	body = append(body, chunk("JUNK", 9999, []byte{1, 2, 3, 4})...) // declares 9999, 4 present
	var sz [4]byte
	le.PutUint32(sz[:], uint32(len(body)))
	data := append(append([]byte("RIFF"), sz[:]...), body...)

	doc, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	found := false
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnOversizedChunk {
			found = true
		}
	}
	if !found {
		t.Errorf("no WarnOversizedChunk in %v; the public code must be emitted and switchable", doc.Warnings())
	}
}

// TestID3PrefixedContainerIsUnsupported checks that a container signature after a leading
// ID3 tag is reported unsupported instead of being routed to a parser that starts at byte 0.
func TestID3PrefixedContainerIsUnsupported(t *testing.T) {
	id3 := make([]byte, 10)
	copy(id3, "ID3")
	id3[3] = 4  // version 2.4
	id3[9] = 16 // sync-safe body size = 16
	data := append(id3, make([]byte, 16)...)
	// An MP4 ftyp box past the ID3 tag.
	data = append(data, []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p',
		'M', '4', 'A', ' ', 0, 0, 0, 0, 'M', '4', 'A', ' ', 'm', 'p', '4', '2',
	}...)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
		t.Errorf("ID3-prefixed MP4 err = %v, want ErrUnsupportedFormat (exit 3), not a corrupt/exit-4 error", err)
	}
}

// TestRF64IsRejectedLoudly checks that RF64/BW64 sniffs as WAV and receives the parser's
// explicit out-of-scope error instead of a generic detection failure.
func TestRF64IsRejectedLoudly(t *testing.T) {
	for _, magic := range []string{"RF64", "BW64"} {
		t.Run(magic, func(t *testing.T) {
			data := make([]byte, 64)
			copy(data[0:4], magic)
			copy(data[8:12], "WAVE")
			_, err := wl.Parse(context.Background(), wl.BytesSource(data))
			if !errors.Is(err, waxerr.ErrUnsupportedFormat) {
				t.Fatalf("%s err = %v, want ErrUnsupportedFormat", magic, err)
			}
			if !strings.Contains(err.Error(), "out of scope") {
				t.Errorf("%s err = %q, want the loud 'out of scope' rejection, not 'could not identify'", magic, err)
			}
		})
	}
}
