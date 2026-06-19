package vorbis

import (
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestParseCommentListReportsConsumed checks the bytes-consumed return value the
// Ogg codecs rely on to find the Vorbis framing bit / preserve Opus padding. The
// tail is deliberately a well-formed-looking extra entry sitting past the declared
// comment count: a correct parser stops by count and reports n before it (so Opus
// would preserve it as padding), while a parser that ignored the count would
// wrongly swallow it - which a plain non-"=" tail could not detect.
func TestParseCommentListReportsConsumed(t *testing.T) {
	body := RenderCommentList("vend", []Comment{{"A", "1"}, {"B", "2"}})
	extra := []byte("EXTRA=ignored")
	tail := append([]byte{byte(len(extra)), 0, 0, 0}, extra...) // a valid length-prefixed entry
	in := append(slices.Clone(body), tail...)

	vendor, cs, n, err := ParseCommentList(in, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if vendor != "vend" {
		t.Errorf("vendor = %q", vendor)
	}
	if len(cs) != 2 || cs[0] != (Comment{"A", "1"}) || cs[1] != (Comment{"B", "2"}) {
		t.Fatalf("comments = %v, want exactly the two declared by the count", cs)
	}
	if n != int64(len(body)) {
		t.Errorf("consumed %d bytes, want %d (the entry past the count must not be consumed)", n, len(body))
	}
	if string(in[n:]) != string(tail) {
		t.Errorf("trailing after list = %q, want %q", in[n:], tail)
	}
}

// TestProjectMarksConflicts confirms two distinct native names mapping to one
// canonical key with disagreeing values are flagged as a conflict, while a plain
// multi-value of the same name is not.
func TestProjectMarksConflicts(t *testing.T) {
	_, fams := Project([]Comment{
		{"DATE", "2020"}, {"YEAR", "2019"}, // both -> RecordingDate, disagree
		{"ARTIST", "A"}, {"ARTIST", "B"}, // ordinary multi-value
	})
	selected := map[tag.Key]bool{}
	for _, f := range fams {
		selected[f.Key] = f.Selected
	}
	if selected[tag.RecordingDate] {
		t.Error("RecordingDate fed by disagreeing DATE/YEAR should be unselected (a conflict)")
	}
	if !selected[tag.Artist] {
		t.Error("repeated ARTIST is a multi-value, not a conflict")
	}
}

// TestRebuildMinimalChange checks the rebuild keeps unchanged comments verbatim,
// replaces a changed key in place, drops aliases of a changed key (deduping), and
// appends genuinely new keys.
func TestRebuildMinimalChange(t *testing.T) {
	orig := []Comment{
		{"TITLE", "Old"},
		{"date", "2019"}, // alias of RecordingDate, lower-case spelling
		{"YEAR", "2019"}, // second alias -> should be dropped when the key changes
		{"ARTIST", "Keep"},
	}
	base := tag.NewTagSet()
	base.Set(tag.Title, "Old")
	base.Set(tag.RecordingDate, "2019")
	base.Set(tag.Artist, "Keep")

	edited := base.Clone()
	edited.Set(tag.RecordingDate, "2020")
	edited.Set(tag.Genre, "Rock") // new key

	got := Rebuild(orig, edited, DiffKeys(base, edited))

	// TITLE and ARTIST unchanged and in place; RecordingDate replaced once at its
	// first occurrence (preferred spelling DATE); the YEAR alias dropped; GENRE
	// appended.
	want := []Comment{
		{"TITLE", "Old"},
		{"DATE", "2020"},
		{"ARTIST", "Keep"},
		{"GENRE", "Rock"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("rebuild = %v\n            want %v", got, want)
	}
}

// TestEncoderNoiseDeduplicatesVendorEcho checks that a transcoder stamp appearing
// in both the vendor string and an ENCODER comment is reported once, while a
// distinct stamp in each is reported twice.
func TestEncoderNoiseDeduplicatesVendorEcho(t *testing.T) {
	t.Run("same value collapses to one", func(t *testing.T) {
		ws := EncoderNoise("Lavf60.3.100", []Comment{{"ENCODER", "Lavf60.3.100"}})
		if len(ws) != 1 {
			t.Fatalf("got %d warnings, want 1: %v", len(ws), ws)
		}
		if !strings.Contains(ws[0].Message, "vendor string and encoder comment") {
			t.Errorf("combined message = %q", ws[0].Message)
		}
	})
	t.Run("case-variant value still collapses", func(t *testing.T) {
		ws := EncoderNoise("Lavf60.3.100", []Comment{{"ENCODER", "lavf60.3.100"}})
		if len(ws) != 1 {
			t.Fatalf("got %d warnings, want 1 (case-insensitive dedup): %v", len(ws), ws)
		}
	})
	t.Run("distinct values stay separate", func(t *testing.T) {
		ws := EncoderNoise("Lavf60.3.100", []Comment{{"ENCODER", "Lavf58.0.0"}})
		if len(ws) != 2 {
			t.Fatalf("got %d warnings, want 2: %v", len(ws), ws)
		}
	})
	t.Run("encoder comment only", func(t *testing.T) {
		ws := EncoderNoise("normal vendor", []Comment{{"ENCODER", "libavformat 60"}})
		if len(ws) != 1 {
			t.Fatalf("got %d warnings, want 1: %v", len(ws), ws)
		}
	})
}
