package core

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

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

// TestTextItemLosesMetadata: only a non-empty qualifier the destination drops is a loss.
func TestTextItemLosesMetadata(t *testing.T) {
	cases := []struct {
		item TextItem
		loss TextItemLoss
		want bool
	}{
		{TextItem{Text: "t", Description: "d", Language: "eng"}, TextLossNone, false},
		{TextItem{Text: "t", Description: "d"}, TextLossDescription, true},
		{TextItem{Text: "t", Language: "eng"}, TextLossDescription, false},
		{TextItem{Text: "t", Language: "eng"}, TextLossDescriptionAndLanguage, true},
		{TextItem{Text: "t", Description: "d"}, TextLossDescriptionAndLanguage, true},
		{TextItem{Text: "t"}, TextLossDescriptionAndLanguage, false},
	}
	for _, c := range cases {
		if got := TextItemLosesMetadata(c.item, c.loss); got != c.want {
			t.Errorf("TextItemLosesMetadata(%+v, %d) = %v, want %v", c.item, c.loss, got, c.want)
		}
	}
	// A capability that does not say claims the loss, so a codec that forgets reports it.
	if !TextItemLosesMetadata(TextItem{Text: "t", Language: "eng"}, Capability{}.TextLoss) {
		t.Error("the zero TextLoss claims qualifiers survive")
	}
}

// TestSyncTextItems: the flat COMMENT/LYRICS values stay authoritative. An item keeps its
// qualifiers through a reorder or removal by following its text; a replaced text takes the
// qualifiers of the item it replaced.
func TestSyncTextItems(t *testing.T) {
	tags := func(kv ...string) tag.TagSet {
		var ts tag.TagSet
		for i := 0; i < len(kv); i += 2 {
			ts.Add(tag.Key(kv[i]), kv[i+1])
		}
		return ts
	}
	described := TextItem{Text: "old", Description: "d", Language: "eng"}
	cases := []struct {
		name string
		m    Media
		want []TextItem
	}{
		{"derived when unset", Media{Tags: tags("COMMENT", "a", "COMMENT", "b")},
			[]TextItem{{Text: "a"}, {Text: "b"}}},
		{"qualifiers kept by position", Media{Tags: tags("COMMENT", "new", "COMMENT", "extra"), Comments: []TextItem{described}},
			[]TextItem{{Text: "new", Description: "d", Language: "eng"}, {Text: "extra"}}},
		{"tail dropped", Media{Tags: tags("COMMENT", "x"), Comments: []TextItem{{Text: "x", Description: "1"}, {Text: "y", Description: "2"}}},
			[]TextItem{{Text: "x", Description: "1"}}},
		{"no values", Media{Comments: []TextItem{described}}, nil},
		{"aligned", Media{Tags: tags("COMMENT", "old"), Comments: []TextItem{described}}, []TextItem{described}},
		{"first removed", Media{Tags: tags("COMMENT", "b"), Comments: []TextItem{{Text: "a", Description: "1"}, {Text: "b", Description: "2"}}},
			[]TextItem{{Text: "b", Description: "2"}}},
		{"reordered", Media{Tags: tags("COMMENT", "b", "COMMENT", "a"), Comments: []TextItem{{Text: "a", Description: "1"}, {Text: "b", Description: "2"}}},
			[]TextItem{{Text: "b", Description: "2"}, {Text: "a", Description: "1"}}},
		{"one replaced", Media{Tags: tags("COMMENT", "c", "COMMENT", "b"), Comments: []TextItem{{Text: "a", Description: "1"}, {Text: "b", Description: "2"}}},
			[]TextItem{{Text: "c", Description: "1"}, {Text: "b", Description: "2"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			SyncTextItems(&c.m)
			if !slices.Equal(c.m.Comments, c.want) {
				t.Errorf("Comments = %+v, want %+v", c.m.Comments, c.want)
			}
		})
	}
	m := Media{Tags: tags("LYRICS", "la"), Lyrics: []TextItem{{Text: "old", Language: "deu"}}}
	SyncTextItems(&m)
	if want := []TextItem{{Text: "la", Language: "deu"}}; !slices.Equal(m.Lyrics, want) {
		t.Errorf("Lyrics = %+v, want %+v", m.Lyrics, want)
	}
}
