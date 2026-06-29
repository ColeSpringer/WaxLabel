package vorbis

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestParseCommentListCountCapped verifies that ParseCommentList stops at maxElements
// with ErrSizeTooLarge. The comment count is an attacker-controlled uint32, and an Ogg
// comment packet is bounded only by the alloc limit, so a run of minimum entries would
// otherwise amplify into one Comment descriptor each. A zero cap stays unbounded.
func TestParseCommentListCountCapped(t *testing.T) {
	const max = 1000
	entries := make([]Comment, max+50)
	for i := range entries {
		entries[i] = Comment{Name: "X", Value: ""} // renders "X=", so it is stored and counted
	}
	body := RenderCommentList("v", entries)

	if _, _, _, err := ParseCommentList(body, 1<<20, max); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("over the %d cap: err = %v, want ErrSizeTooLarge", max, err)
	}
	if _, cs, _, err := ParseCommentList(body, 1<<20, 0); err != nil || len(cs) != max+50 {
		t.Fatalf("uncapped (0): got %d comments, err = %v; want all %d", len(cs), err, max+50)
	}
}

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

	vendor, cs, n, err := ParseCommentList(in, 1<<20, 0)
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

	got := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)

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

// TestRebuildPreservesEditedKeyCasing checks that editing an existing key keeps the
// file's own spelling for that key (lowercase "title" stays "title") rather than forcing
// the canonical upper-case name. Untouched keys stay verbatim, and an edited alias still
// canonicalizes to its preferred spelling (DATE).
func TestRebuildPreservesEditedKeyCasing(t *testing.T) {
	orig := []Comment{
		{"artist", "A"},
		{"title", "Old"},
		{"year", "2019"}, // alias of RecordingDate, lower-case
	}
	base := tag.NewTagSet()
	base.Set(tag.Artist, "A")
	base.Set(tag.Title, "Old")
	base.Set(tag.RecordingDate, "2019")

	edited := base.Clone()
	edited.Set(tag.Title, "New")          // edit an existing lowercase key
	edited.Set(tag.RecordingDate, "2020") // edit an alias

	got := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
	want := []Comment{
		{"artist", "A"},  // untouched: verbatim casing
		{"title", "New"}, // edited but keeps the file's lowercase spelling
		{"DATE", "2020"}, // alias canonicalizes to the preferred Vorbis spelling
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

// TestProjectSanitizesInvalidUTF8 is the QA-review regression: the Vorbis reader stores raw
// bytes, so a non-conformant file can hold invalid UTF-8. Project must sanitize it into the
// canonical model (like the ID3/MP4/WAV/AIFF readers) so the value never reaches the TagSet
// or family view invalid - otherwise a copy of it would be spuriously rejected by the
// write-time UTF-8 guard, and --json would emit raw invalid bytes.
func TestProjectSanitizesInvalidUTF8(t *testing.T) {
	ts, fams := Project([]Comment{{Name: "ARTIST", Value: "bad\xff\xfevalue"}})
	if v, _ := ts.First(tag.Artist); !utf8.ValidString(v) {
		t.Errorf("Project left invalid UTF-8 in the TagSet: %q", v)
	}
	if len(fams) != 1 || len(fams[0].Values) != 1 || !utf8.ValidString(fams[0].Values[0]) {
		t.Errorf("Project left invalid UTF-8 in the family view: %+v", fams)
	}
	// A valid value is untouched.
	if ts2, _ := Project([]Comment{{Name: "ARTIST", Value: "Valid ☃"}}); func() bool {
		v, _ := ts2.First(tag.Artist)
		return v != "Valid ☃"
	}() {
		t.Error("Project altered a valid UTF-8 value")
	}
}

// TestParsePictureSanitizesDescription is a QA-review regression: a FLAC/Ogg picture
// description is stored as raw bytes, so a non-conformant file can hold invalid UTF-8.
// ParsePicture must sanitize it so a transfer that re-adds the picture is not rejected by the
// write-time UTF-8 guard.
func TestParsePictureSanitizesDescription(t *testing.T) {
	body := RenderPicture(core.Picture{
		Type: core.PicFrontCover, MIME: "image/png", Description: "bad\xff\xfedesc", Data: []byte{1, 2, 3},
	})
	p, err := ParsePicture(body, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(p.Description) {
		t.Errorf("ParsePicture left invalid UTF-8 in the description: %q", p.Description)
	}
}
