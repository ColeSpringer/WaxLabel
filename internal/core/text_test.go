package core

import "testing"

// TestIndefiniteArticle locks the a/an choice for every format name WaxLabel
// interpolates, including the MP3/MP4 initialisms a plain leading-vowel rule gets
// wrong (MP4 is not reachable via the chapter message, so this is its coverage).
func TestIndefiniteArticle(t *testing.T) {
	cases := map[string]string{
		"AAC (ADTS)": "an", // vowel-initial
		"AIFF":       "an",
		"Ogg Vorbis": "an",
		"Ogg Opus":   "an",
		"MP3":        "an", // "em-pee-three": vowel sound
		"MP4":        "an",
		"mp3":        "an", // case-insensitive
		"FLAC":       "a",  // "flak": consonant
		"WAV":        "a",
		"WebM":       "a",
		"Matroska":   "a",
		"":           "a", // defensive
	}
	for name, want := range cases {
		if got := IndefiniteArticle(name); got != want {
			t.Errorf("IndefiniteArticle(%q) = %q, want %q", name, got, want)
		}
	}
}
