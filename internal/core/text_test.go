package core

import (
	"slices"
	"strconv"
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

// TestTextItemLossReason: only a non-empty qualifier the destination drops is a loss, and the
// reason names what a set of items loses between them.
func TestTextItemLossReason(t *testing.T) {
	both, desc, lang := "description and language not carried", "description not carried", "language not carried"
	cases := []struct {
		items []TextItem
		loss  TextItemLoss
		want  string
	}{
		{[]TextItem{{Text: "t", Description: "d", Language: "eng"}}, TextLossNone, ""},
		{[]TextItem{{Text: "t", Description: "d", Language: "eng"}}, TextLossDescription, desc},
		{[]TextItem{{Text: "t", Language: "eng"}}, TextLossDescription, ""},
		{[]TextItem{{Text: "t", Language: "eng"}}, TextLossDescriptionAndLanguage, lang},
		{[]TextItem{{Text: "t", Description: "d"}}, TextLossDescriptionAndLanguage, desc},
		{[]TextItem{{Text: "t", Description: "d"}, {Text: "u", Language: "eng"}}, TextLossDescriptionAndLanguage, both},
		{[]TextItem{{Text: "t"}}, TextLossDescriptionAndLanguage, ""},
		{nil, TextLossDescriptionAndLanguage, ""},
	}
	for _, c := range cases {
		if got := TextItemLossReason(c.items, c.loss); got != c.want {
			t.Errorf("TextItemLossReason(%+v, %d) = %q, want %q", c.items, c.loss, got, c.want)
		}
	}
	// A capability that does not say claims the loss, so a codec that forgets reports it.
	if TextItemLossReason([]TextItem{{Text: "t", Language: "eng"}}, Capability{}.TextLoss) == "" {
		t.Error("the zero TextLoss claims qualifiers survive")
	}
}

// TestTextItemLossKept: the qualifiers each loss leaves, in a fixed order.
func TestTextItemLossKept(t *testing.T) {
	for loss, want := range map[TextItemLoss][]string{
		TextLossNone:                   {"description", "language"},
		TextLossDescription:            {"language"},
		TextLossDescriptionAndLanguage: nil,
	} {
		if got := loss.Kept(); !slices.Equal(got, want) {
			t.Errorf("TextItemLoss(%d).Kept() = %q, want %q", loss, got, want)
		}
	}
}

// TestSingleValuedMulti: the key's own rule, but for lyrics told apart by a qualifier.
func TestSingleValuedMulti(t *testing.T) {
	translated := []TextItem{{Text: "a", Language: "eng"}, {Text: "b", Language: "deu"}}
	same := []TextItem{{Text: "a", Language: "eng"}, {Text: "b", Language: "eng"}}
	cases := []struct {
		key    tag.Key
		count  int
		lyrics []TextItem
		want   bool
	}{
		{tag.Lyrics, 2, translated, false},
		{tag.Lyrics, 2, same, true},
		{tag.Lyrics, 1, same[:1], false},
		{tag.Title, 2, translated, true},
		{tag.Comment, 2, same, false},
	}
	for _, c := range cases {
		if got := SingleValuedMulti(c.key, c.count, c.lyrics); got != c.want {
			t.Errorf("SingleValuedMulti(%s, %d, %+v) = %v, want %v", c.key, c.count, c.lyrics, got, c.want)
		}
	}
}

// TestQualifiersDistinct: several items no two of which share a description and language.
func TestQualifiersDistinct(t *testing.T) {
	cases := []struct {
		items []TextItem
		want  bool
	}{
		{nil, false},
		{[]TextItem{{Text: "a", Language: "eng"}}, false},
		{[]TextItem{{Text: "a", Language: "eng"}, {Text: "b", Language: "deu"}}, true},
		{[]TextItem{{Text: "a"}, {Text: "b", Description: "Translation"}}, true},
		{[]TextItem{{Text: "a", Language: "eng"}, {Text: "b", Language: "eng"}}, false},
		{[]TextItem{{Text: "a"}, {Text: "b", Language: "deu"}, {Text: "c"}}, false},
	}
	for _, c := range cases {
		if got := QualifiersDistinct(c.items); got != c.want {
			t.Errorf("QualifiersDistinct(%+v) = %v, want %v", c.items, got, c.want)
		}
	}
}

// TestQualifierStoredAs: a set qualifier the format keeps but stores in another form is found;
// one the format drops, an unset one and a language differing in case are not.
func TestQualifierStoredAs(t *testing.T) {
	requested := []TextItem{{Text: "a", Description: "d", Language: "english"}, {Text: "b", Language: "ENG"}}
	stored := []TextItem{{Text: "b", Language: "eng"}, {Text: "a", Description: "d", Language: "eng"}}
	if q, want, got, differs := QualifierStoredAs(requested, stored, TextLossNone); !differs || q != "language" || want != "english" || got != "eng" {
		t.Errorf("QualifierStoredAs = %q %q %q %v, want language english eng true", q, want, got, differs)
	}
	if _, _, _, differs := QualifierStoredAs(requested, stored, TextLossDescriptionAndLanguage); differs {
		t.Error("a language the format drops was compared")
	}
	bare := []TextItem{{Text: "a"}}
	if q, _, _, differs := QualifierStoredAs(requested[:1], bare, TextLossNone); !differs || q != "description" {
		t.Errorf("a lost description was not found: %q %v", q, differs)
	}
	if _, _, _, differs := QualifierStoredAs(bare, stored, TextLossNone); differs {
		t.Error("an unset qualifier that reads back set was reported")
	}
}

// TestTextsOf: one text per item, in order.
func TestTextsOf(t *testing.T) {
	if got := TextsOf([]TextItem{{Text: "a", Description: "d"}, {Text: "b"}}); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("TextsOf = %q, want [a b]", got)
	}
	if got := TextsOf(nil); got != nil {
		t.Errorf("TextsOf(nil) = %q, want nil", got)
	}
}

// TestTextItemChanges: a qualifier change is reported for the items both sides hold, paired by
// text and then by position, and only for the qualifiers loss keeps.
func TestTextItemChanges(t *testing.T) {
	a := []TextItem{{Text: "x", Description: "d1", Language: "eng"}, {Text: "y", Language: "deu"}}
	lines := func(b []TextItem, loss TextItemLoss) []string {
		var out []string
		for _, c := range TextItemChanges("COMMENT", a, b, loss, false) {
			out = append(out, c.String())
		}
		return out
	}
	cases := []struct {
		name string
		b    []TextItem
		loss TextItemLoss
		want []string
	}{
		{"same", a, TextLossNone, nil},
		{"description cleared", []TextItem{{Text: "x", Language: "eng"}, a[1]}, TextLossNone,
			[]string{`~ COMMENT description: "d1" -> ""`}},
		{"both changed", []TextItem{{Text: "x", Description: "d2", Language: "fra"}, a[1]}, TextLossNone,
			[]string{`~ COMMENT description: "d1" -> "d2"`, `~ COMMENT language: "eng" -> "fra"`}},
		{"reordered", []TextItem{a[1], a[0]}, TextLossNone, nil},
		{"replaced text keeps its pair", []TextItem{{Text: "z", Description: "d2", Language: "eng"}, a[1]}, TextLossNone,
			[]string{`~ COMMENT description: "d1" -> "d2"`}},
		{"added item is not a change", append(slices.Clone(a), TextItem{Text: "new", Language: "eng"}), TextLossNone, nil},
		{"removed item is not a change", a[:1], TextLossNone, nil},
		{"description not compared", []TextItem{{Text: "x", Language: "fra"}, a[1]}, TextLossDescription,
			[]string{`~ COMMENT language: "eng" -> "fra"`}},
		{"nothing compared", []TextItem{{Text: "x"}, {Text: "y"}}, TextLossDescriptionAndLanguage, nil},
	}
	for _, c := range cases {
		if got := lines(c.b, c.loss); !slices.Equal(got, c.want) {
			t.Errorf("%s: changes = %q, want %q", c.name, got, c.want)
		}
	}
	// An unset language matches any when asked to; two set languages still differ.
	unset := []TextItem{{Text: "x", Description: "d1"}, {Text: "y", Language: "fra"}}
	var got []string
	for _, c := range TextItemChanges("COMMENT", a, unset, TextLossNone, true) {
		got = append(got, c.String())
	}
	if want := []string{`~ COMMENT language: "deu" -> "fra"`}; !slices.Equal(got, want) {
		t.Errorf("unset language matches: changes = %q, want %q", got, want)
	}
	if TextLossNone.Both(TextLossDescription) != TextLossDescription || TextLossDescription.Both(TextLossDescriptionAndLanguage) != TextLossDescriptionAndLanguage {
		t.Error("Both does not keep the greater loss")
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

// BenchmarkAlignTextItemsFlood aligns n items to n values, in order and reversed: the shapes that
// make a per-value scan of the items quadratic.
func BenchmarkAlignTextItemsFlood(b *testing.B) {
	for _, n := range []int{10000, 20000, 40000} {
		items := make([]TextItem, n)
		ts, reversed := tag.NewTagSet(), tag.NewTagSet()
		for i := range items {
			items[i] = TextItem{Text: strconv.Itoa(i), Language: "eng"}
		}
		ts.Set(tag.Comment, TextsOf(items)...)
		texts := TextsOf(items)
		slices.Reverse(texts)
		reversed.Set(tag.Comment, texts...)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			for range b.N {
				AlignTextItems(items, ts, tag.Comment)
				AlignTextItems(items, reversed, tag.Comment)
			}
		})
	}
}
