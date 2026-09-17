package core

import "testing"

// TestDefaultID3Version: MP3 defaults to v2.3; other id3 formats to v2.4.
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
