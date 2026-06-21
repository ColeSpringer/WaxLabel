package core

import "testing"

// TestDefaultID3Version pins the one-rule policy: MP3 (read directly by legacy
// hardware) defaults a fresh id3 tag to v2.3; every other id3-bearing format is
// read only by modern software and defaults to v2.4.
func TestDefaultID3Version(t *testing.T) {
	cases := map[Format]byte{
		FormatMP3:  3,
		FormatWAV:  4,
		FormatAIFF: 4,
		FormatAAC:  4,
	}
	for f, want := range cases {
		if got := DefaultID3Version(f); got != want {
			t.Errorf("DefaultID3Version(%s) = %d, want %d", f, got, want)
		}
	}
}
