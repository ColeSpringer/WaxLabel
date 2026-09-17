package core

import "testing"

// TestIndefiniteArticle: a/an for format names (MP3/MP4 need vowel-sound rule).
func TestIndefiniteArticle(t *testing.T) {
	cases := map[string]string{
		"AAC (ADTS)": "an",
		"AIFF":       "an",
		"Ogg Vorbis": "an",
		"Ogg Opus":   "an",
		"MP3":        "an",
		"MP4":        "an",
		"mp3":        "an",
		"FLAC":       "a",
		"WAV":        "a",
		"WebM":       "a",
		"Matroska":   "a",
		"":           "a",
	}
	for name, want := range cases {
		if got := IndefiniteArticle(name); got != want {
			t.Errorf("IndefiniteArticle(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestIsTranscoderStamp: Lavf/Lavc/libavformat/libavcodec stamps; real encoder names excluded.
func TestIsTranscoderStamp(t *testing.T) {
	stamps := []string{
		"Lavf61.7.100",
		"Lavf58.29.100",
		"libavformat 60",
		"Lavc61.19.101 libopus",
		"Lavc60.31.102",
		"libavcodec 60.31.102",
		"LAVC61.19.101 LIBOPUS",
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
		"My Favourite Lav Recorder",
	}
	for _, s := range clean {
		if IsTranscoderStamp(s) {
			t.Errorf("IsTranscoderStamp(%q) = true, want false", s)
		}
	}
}
