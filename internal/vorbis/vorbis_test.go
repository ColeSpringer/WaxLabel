package vorbis

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestRebuildDropsReservedChapterKey covers Finding 8: a newly-added custom key in the reserved
// CHAPTERxxx namespace is not emitted as a comment (on read it is owned by the chapter model, so a
// written comment would vanish from the tag view) and is recorded in ReservedChapterKeys so the
// caller surfaces a value-dropped warning rather than claim the key was written.
func TestRebuildDropsReservedChapterKey(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Set(tag.Key("CHAPTER005"), "hijack")
	changed := map[tag.Key]bool{tag.Key("CHAPTER005"): true}
	out, info := Rebuild(nil, edited, changed, nil, false, nil, false)
	for _, cm := range out {
		if cm.Name == "CHAPTER005" {
			t.Errorf("a reserved chapter key was emitted as a comment: %+v", cm)
		}
	}
	if !slices.Contains(info.ReservedChapterKeys, tag.Key("CHAPTER005")) {
		t.Errorf("ReservedChapterKeys = %v, want it to contain CHAPTER005", info.ReservedChapterKeys)
	}
	ws := RebuildWarnings(nil, info)
	if !slices.ContainsFunc(ws, func(w core.Warning) bool {
		return w.Code == core.WarnValueDropped && slices.Contains(w.Keys, tag.Key("CHAPTER005"))
	}) {
		t.Errorf("RebuildWarnings did not surface a value-dropped warning for CHAPTER005: %v", ws)
	}
	// A plain custom key (not in the chapter namespace) is still written normally.
	edited2 := tag.NewTagSet()
	edited2.Set(tag.Key("MYFIELD"), "keep")
	out2, info2 := Rebuild(nil, edited2, map[tag.Key]bool{tag.Key("MYFIELD"): true}, nil, false, nil, false)
	if len(info2.ReservedChapterKeys) != 0 {
		t.Errorf("a non-chapter custom key must not be flagged reserved: %v", info2.ReservedChapterKeys)
	}
	if !slices.ContainsFunc(out2, func(cm Comment) bool { return cm.Name == "MYFIELD" && cm.Value == "keep" }) {
		t.Errorf("a plain custom key should still be written: %+v", out2)
	}
}

// TestPictureDecodePreservesStoredMIME covers the re-serialization half of Finding 6: the decoders
// (ParsePicture for a native FLAC block, DecodePictureComment for an Ogg comment) return each cover's
// MIME and dimensions exactly as stored, never sniffed. This is the re-serialization source, so a
// mislabeled cover's on-disk label survives an unrelated edit rather than being silently rewritten.
func TestPictureDecodePreservesStoredMIME(t *testing.T) {
	gif := append([]byte("GIF89a"), 0x03, 0x00, 0x05, 0x00, 0x77, 0x00, 0x00)
	body := RenderPicture(core.Picture{Type: core.PicFrontCover, MIME: "image/png", Data: gif}) // mislabeled
	if p, err := ParsePicture(body, 1<<20); err != nil {
		t.Fatalf("ParsePicture: %v", err)
	} else if p.MIME != "image/png" {
		t.Errorf("ParsePicture MIME = %q, want the stored \"image/png\" (unsniffed)", p.MIME)
	}
	if pc, err := DecodePictureComment(base64.StdEncoding.EncodeToString(body), 1<<20); err != nil {
		t.Fatalf("DecodePictureComment: %v", err)
	} else if pc.MIME != "image/png" {
		t.Errorf("DecodePictureComment MIME = %q, want the stored \"image/png\" (unsniffed)", pc.MIME)
	}
}

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

	got, _ := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)

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

	got, _ := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
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

// TestProjectSanitizesInvalidUTF8 checks that invalid Vorbis comment bytes are sanitized
// before reaching the canonical tag model or family view.
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

// TestParsePictureSanitizesDescription checks that FLAC/Ogg picture descriptions are valid
// UTF-8 once exposed.
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

// TestParsePictureClampsOutOfRangeType checks that a picture type past the single-byte
// ID3/FLAC role space reads as PicOther rather than narrowing/wrapping into a misleading
// valid role (259 & 0xFF == 3, "Front cover"). The image bytes are preserved regardless;
// only the role projection is clamped. Protects both FLAC PICTURE blocks and Ogg
// METADATA_BLOCK_PICTURE comments, which share this decoder.
func TestParsePictureClampsOutOfRangeType(t *testing.T) {
	body := RenderPicture(core.Picture{
		Type: core.PicFrontCover, MIME: "image/png", Data: []byte{1, 2, 3},
	})
	// Overwrite the 32-bit type field (first 4 bytes) with 259, which a bare uint8
	// conversion would wrap to 3 (PicFrontCover).
	binary.BigEndian.PutUint32(body[0:4], 259)
	p, err := ParsePicture(body, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != core.PicOther {
		t.Errorf("out-of-range type 259 read as %v (%d), want PicOther", p.Type, p.Type)
	}
	if !slices.Equal(p.Data, []byte{1, 2, 3}) {
		t.Errorf("picture data corrupted by the type clamp: %v", p.Data)
	}
}

// TestProjectSkipsPictureComment checks that picture comments stay out of the custom tag
// projection. Malformed picture comments are kept opaque by the parser, but they are still
// picture metadata and should not appear as tag or family values.
func TestProjectSkipsPictureComment(t *testing.T) {
	for _, name := range []string{"METADATA_BLOCK_PICTURE", "metadata_block_picture"} {
		ts, fams := Project([]Comment{
			{"TITLE", "T"},
			{name, "not-valid-base64!!"},
		})
		if vals, ok := ts.Get(tag.Key(name)); ok {
			t.Errorf("%s leaked as a tag: %v", name, vals)
		}
		for _, f := range fams {
			if strings.EqualFold(string(f.Key), name) {
				t.Errorf("%s leaked as a family value", name)
			}
		}
		if v, _ := ts.First(tag.Title); v != "T" {
			t.Errorf("TITLE = %q, want T (a real tag still projects)", v)
		}
	}
}

// TestRebuildPreservesPictureComment checks that an opaque picture comment survives an
// unrelated tag edit. The codec re-renders decoded pictures, while malformed picture comments
// remain ordinary preserved comments.
func TestRebuildPreservesPictureComment(t *testing.T) {
	orig := []Comment{
		{"TITLE", "Old"},
		{"METADATA_BLOCK_PICTURE", "not-valid-base64!!"},
	}
	base := tag.NewTagSet()
	base.Set(tag.Title, "Old")
	edited := base.Clone()
	edited.Set(tag.Title, "New")

	got, _ := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
	want := []Comment{
		{"TITLE", "New"},
		{"METADATA_BLOCK_PICTURE", "not-valid-base64!!"}, // preserved verbatim
	}
	if !slices.Equal(got, want) {
		t.Errorf("rebuild = %v\n            want %v", got, want)
	}
}
