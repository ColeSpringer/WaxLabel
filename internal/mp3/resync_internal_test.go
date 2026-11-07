package mp3

import (
	"slices"
	"testing"
)

// Resync confirms an MPEG frame only by a second header at the computed frame length, inside the
// window, agreeing on version, layer and sample rate. A lone header, a free-format frame or a next
// frame past the window confirm nothing, though the parser's scan accepts them.
func TestResyncNeedsAnAgreeingSecondFrame(t *testing.T) {
	frame := func(header ...byte) []byte {
		b := make([]byte, 417) // MPEG-1 Layer 3, 128 kbps, 44.1 kHz, no padding
		copy(b, header)
		return b
	}
	l3 := frame(0xFF, 0xFB, 0x90, 0x00)
	at48k := frame(0xFF, 0xFB, 0x94, 0x00)
	free := frame(0xFF, 0xFB, 0x00, 0x00) // bitrate index 0
	for _, c := range []struct {
		name    string
		window  []byte
		off     int
		ok      bool
		scanned bool // what the parser's lenient scan makes of the window
	}{
		{"two agreeing frames", slices.Concat([]byte{1, 2, 3}, l3, l3[:4]), 3, true, true},
		{"lone header", slices.Concat([]byte{1, 2, 3}, l3), 0, false, true},
		{"next frame past the window", slices.Concat(l3, l3[:3]), 0, false, true},
		{"next frame at another rate", slices.Concat(l3, at48k[:4]), 0, false, true},
		{"free format", slices.Concat(free, free[:4]), 0, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if off, ok := (Codec{}).Resync(c.window); off != c.off || ok != c.ok {
				t.Errorf("Resync = %d, %v; want %d, %v", off, ok, c.off, c.ok)
			}
			if _, _, ok := parseMPEG(c.window); ok != c.scanned {
				t.Errorf("parseMPEG ok = %v, want %v", ok, c.scanned)
			}
		})
	}
}
