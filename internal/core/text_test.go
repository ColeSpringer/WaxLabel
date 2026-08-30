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

// TestIsTranscoderStamp locks the transcoder vocabulary. Both halves of the ffmpeg family
// count: libavformat writes the muxer stamp and libavcodec the codec one, and matching only
// the first left a "Lavc61.19.101 libopus" ENCODER surviving --strip-encoder and lint --fix.
// A genuine encoder name must not match, or those paths would destroy a user's own value.
func TestIsTranscoderStamp(t *testing.T) {
	stamps := []string{
		"Lavf61.7.100",
		"Lavf58.29.100",
		"libavformat 60",
		"Lavc61.19.101 libopus",
		"Lavc60.31.102",
		"libavcodec 60.31.102",
		"LAVC61.19.101 LIBOPUS", // case-folded
	}
	for _, s := range stamps {
		if !IsTranscoderStamp(s) {
			t.Errorf("IsTranscoderStamp(%q) = false, want true", s)
		}
	}
	clean := []string{
		"",
		"opusenc 0.2 libopus 1",
		"reference libFLAC 1.4.3 20230623",
		"LAME 3.100",
		"iTunes 12.12.4.1",
		"Nero AAC Encoder",
		"My Favourite Lav Recorder", // "lav" alone is not the ffmpeg prefix
	}
	for _, s := range clean {
		if IsTranscoderStamp(s) {
			t.Errorf("IsTranscoderStamp(%q) = true, want false", s)
		}
	}
}
