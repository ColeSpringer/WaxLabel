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

// TestRebuildDropsReservedKey: a newly-added custom key in any of the three reserved

func TestRebuildDropsReservedKey(t *testing.T) {
	validCover := base64.StdEncoding.EncodeToString(RenderPicture(core.Picture{
		Type: core.PicFrontCover, MIME: "image/png", Data: []byte{1, 2, 3},
	}))
	for _, tc := range []struct {
		label  string
		key    tag.Key
		value  string
		wantNS string // the namespace phrase the value-dropped message must name
	}{
		{"chapter", tag.Key("CHAPTER005"), "hijack", "reserved chapter namespace"},
		{"synced lyrics", tag.Key("SYNCEDLYRICS"), "[00:01.00]hi", "reserved synced lyrics namespace"},
		{"cover art", tag.Key("METADATA_BLOCK_PICTURE"), validCover, "reserved cover art namespace"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			edited := tag.NewTagSet()
			edited.Set(tc.key, tc.value)
			out, info := Rebuild(nil, edited, map[tag.Key]bool{tc.key: true}, nil, false, nil, false)
			for _, cm := range out {
				if strings.EqualFold(cm.Name, string(tc.key)) {
					t.Errorf("a reserved %s key was emitted as a comment: %+v", tc.label, cm)
				}
			}
			if !slices.Contains(info.ReservedKeys, tc.key) {
				t.Errorf("ReservedKeys = %v, want it to contain %s", info.ReservedKeys, tc.key)
			}
			found := false
			for _, w := range RebuildWarnings(nil, info) {
				if w.Code != core.WarnValueDropped || !slices.Contains(w.Keys, tc.key) {
					continue
				}
				found = true
				if !strings.Contains(w.Message, tc.wantNS) {
					t.Errorf("value-dropped message = %q, want it to name %q", w.Message, tc.wantNS)
				}
			}
			if !found {
				t.Errorf("RebuildWarnings did not surface a value-dropped warning for %s", tc.key)
			}
		})
	}

	// A plain custom key (in no reserved namespace) is still written normally.
	edited := tag.NewTagSet()
	edited.Set(tag.Key("MYFIELD"), "keep")
	out, info := Rebuild(nil, edited, map[tag.Key]bool{tag.Key("MYFIELD"): true}, nil, false, nil, false)
	if len(info.ReservedKeys) != 0 {
		t.Errorf("a non-reserved custom key must not be flagged reserved: %v", info.ReservedKeys)
	}
	if !slices.ContainsFunc(out, func(cm Comment) bool { return cm.Name == "MYFIELD" && cm.Value == "keep" }) {
		t.Errorf("a plain custom key should still be written: %+v", out)
	}
}

// TestRebuildSetOnExistingPictureCommentDrops: the case where the file already holds a

func TestRebuildSetOnExistingPictureCommentDrops(t *testing.T) {
	orig := []Comment{{Name: "TITLE", Value: "Keep"}, {Name: "METADATA_BLOCK_PICTURE", Value: "not-valid-base64!!"}}
	edited := tag.NewTagSet()
	edited.Set(tag.Title, "Keep")
	edited.Set(tag.Key("METADATA_BLOCK_PICTURE"), "hijack") // must not overwrite the existing comment
	changed := map[tag.Key]bool{tag.Key("METADATA_BLOCK_PICTURE"): true}

	out, info := Rebuild(orig, edited, changed, nil, false, nil, false)

	// The existing picture comment is preserved verbatim and never overwritten by the set value.
	pics := 0
	for _, cm := range out {
		if IsPictureComment(cm.Name) {
			pics++
			if cm.Value != "not-valid-base64!!" {
				t.Errorf("existing picture comment was overwritten by the --set value: %q", cm.Value)
			}
		}
	}
	if pics != 1 {
		t.Errorf("want exactly the 1 preserved picture comment, got %d: %+v", pics, out)
	}
	// The set value must drop-with-warning, exactly like a newly-added reserved key.
	if !slices.Contains(info.ReservedKeys, tag.Key("METADATA_BLOCK_PICTURE")) {
		t.Errorf("ReservedKeys = %v, want METADATA_BLOCK_PICTURE (the --set must drop-with-warning)", info.ReservedKeys)
	}
	if !slices.ContainsFunc(RebuildWarnings(nil, info), func(w core.Warning) bool {
		return w.Code == core.WarnValueDropped && slices.Contains(w.Keys, tag.Key("METADATA_BLOCK_PICTURE"))
	}) {
		t.Error("no value-dropped warning for the dropped METADATA_BLOCK_PICTURE --set over an existing comment")
	}
}

// TestPictureDecodePreservesStoredMIME: the re-serialization half: the decoders

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

// TestPictureCommentLenMatchesRender: pins the arithmetic PictureCommentLen to the actual

func TestPictureCommentLenMatchesRender(t *testing.T) {
	for _, p := range []core.Picture{
		{Type: core.PicFrontCover, MIME: "image/png", Description: "cover", Data: make([]byte, 5000)},
		{Type: core.PicOther, MIME: "", Description: "", Data: nil},
		{Type: core.PicBackCover, MIME: "image/jpeg", Description: "描述", Data: []byte{1, 2, 3}},
	} {
		want := int64(base64.StdEncoding.EncodedLen(len(RenderPicture(p))))
		if got := PictureCommentLen(p); got != want {
			t.Errorf("PictureCommentLen(%q,%d bytes) = %d, want %d (must track RenderPicture)", p.MIME, len(p.Data), got, want)
		}
	}
}

// TestParseCommentListCountCapped: ParseCommentList stops at maxElements

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

// TestParseCommentListReportsConsumed: the bytes-consumed return value the

func TestParseCommentListReportsConsumed(t *testing.T) {
	body := RenderCommentList("vend", []Comment{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}})
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
	if len(cs) != 2 || cs[0] != (Comment{Name: "A", Value: "1"}) || cs[1] != (Comment{Name: "B", Value: "2"}) {
		t.Fatalf("comments = %v, want exactly the two declared by the count", cs)
	}
	if n != int64(len(body)) {
		t.Errorf("consumed %d bytes, want %d (the entry past the count must not be consumed)", n, len(body))
	}
	if string(in[n:]) != string(tail) {
		t.Errorf("trailing after list = %q, want %q", in[n:], tail)
	}
}

// TestProjectMarksConflicts: two distinct native names mapping to one

func TestProjectMarksConflicts(t *testing.T) {
	_, fams := Project([]Comment{
		{Name: "DATE", Value: "2020"}, {Name: "YEAR", Value: "2019"}, // both -> RecordingDate, disagree
		{Name: "ARTIST", Value: "A"}, {Name: "ARTIST", Value: "B"}, // ordinary multi-value
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

// TestRebuildMinimalChange: the rebuild keeps unchanged comments verbatim,

func TestRebuildMinimalChange(t *testing.T) {
	orig := []Comment{
		{Name: "TITLE", Value: "Old"},
		{Name: "date", Value: "2019"}, // alias of RecordingDate, lower-case spelling
		{Name: "YEAR", Value: "2019"}, // second alias -> should be dropped when the key changes
		{Name: "ARTIST", Value: "Keep"},
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
		{Name: "TITLE", Value: "Old"},
		{Name: "DATE", Value: "2020"},
		{Name: "ARTIST", Value: "Keep"},
		{Name: "GENRE", Value: "Rock"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("rebuild = %v\n            want %v", got, want)
	}
}

// TestRebuildPreservesEditedKeyCasing: editing an existing key keeps the

func TestRebuildPreservesEditedKeyCasing(t *testing.T) {
	orig := []Comment{
		{Name: "artist", Value: "A"},
		{Name: "title", Value: "Old"},
		{Name: "year", Value: "2019"}, // alias of RecordingDate, lower-case
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
		{Name: "artist", Value: "A"},  // untouched: verbatim casing
		{Name: "title", Value: "New"}, // edited but keeps the file's lowercase spelling
		{Name: "DATE", Value: "2020"}, // alias canonicalizes to the preferred Vorbis spelling
	}
	if !slices.Equal(got, want) {
		t.Errorf("rebuild = %v\n            want %v", got, want)
	}
}

// TestEncoderNoiseDeduplicatesVendorEcho: a transcoder stamp appearing

func TestEncoderNoiseDeduplicatesVendorEcho(t *testing.T) {
	t.Run("same value collapses to one", func(t *testing.T) {
		ws := EncoderNoise("Lavf60.3.100", []Comment{{Name: "ENCODER", Value: "Lavf60.3.100"}})
		if len(ws) != 1 {
			t.Fatalf("got %d warnings, want 1: %v", len(ws), ws)
		}
		if !strings.Contains(ws[0].Message, "vendor string and encoder comment") {
			t.Errorf("combined message = %q", ws[0].Message)
		}
	})
	t.Run("case-variant value still collapses", func(t *testing.T) {
		ws := EncoderNoise("Lavf60.3.100", []Comment{{Name: "ENCODER", Value: "lavf60.3.100"}})
		if len(ws) != 1 {
			t.Fatalf("got %d warnings, want 1 (case-insensitive dedup): %v", len(ws), ws)
		}
	})
	t.Run("distinct values stay separate", func(t *testing.T) {
		ws := EncoderNoise("Lavf60.3.100", []Comment{{Name: "ENCODER", Value: "Lavf58.0.0"}})
		if len(ws) != 2 {
			t.Fatalf("got %d warnings, want 2: %v", len(ws), ws)
		}
	})
	t.Run("encoder comment only", func(t *testing.T) {
		ws := EncoderNoise("normal vendor", []Comment{{Name: "ENCODER", Value: "libavformat 60"}})
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

// TestParsePictureClampsOutOfRangeType: a picture type past the single-byte

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

// TestProjectSkipsPictureComment: picture comments stay out of the custom tag

func TestProjectSkipsPictureComment(t *testing.T) {
	for _, name := range []string{"METADATA_BLOCK_PICTURE", "metadata_block_picture"} {
		ts, fams := Project([]Comment{
			{Name: "TITLE", Value: "T"},
			{Name: name, Value: "not-valid-base64!!"},
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

// TestRebuildPreservesPictureComment: an opaque picture comment survives an

func TestRebuildPreservesPictureComment(t *testing.T) {
	orig := []Comment{
		{Name: "TITLE", Value: "Old"},
		{Name: "METADATA_BLOCK_PICTURE", Value: "not-valid-base64!!"},
	}
	base := tag.NewTagSet()
	base.Set(tag.Title, "Old")
	edited := base.Clone()
	edited.Set(tag.Title, "New")

	got, _ := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
	want := []Comment{
		{Name: "TITLE", Value: "New"},
		{Name: "METADATA_BLOCK_PICTURE", Value: "not-valid-base64!!"}, // preserved verbatim
	}
	if !slices.Equal(got, want) {
		t.Errorf("rebuild = %v\n            want %v", got, want)
	}
}

// TestNeutralizeVendorCodecStamp: the comment-header vendor string is where a transcode

func TestNeutralizeVendorCodecStamp(t *testing.T) {
	for _, v := range []string{"Lavc61.19.101 libopus", "libavcodec 60.31.102", "Lavf61.7.100"} {
		got, changed := NeutralizeVendor(v, true)
		if !changed || got != WaxLabelVendor {
			t.Errorf("NeutralizeVendor(%q, true) = %q, %v; want %q, true", v, got, changed, WaxLabelVendor)
		}
	}
	const clean = "reference libFLAC 1.4.3 20230623"
	if got, changed := NeutralizeVendor(clean, true); changed || got != clean {
		t.Errorf("NeutralizeVendor(%q, true) = %q, %v; want it preserved", clean, got, changed)
	}
	if got, changed := NeutralizeVendor("Lavc61.19.101 libopus", false); changed || got != "Lavc61.19.101 libopus" {
		t.Errorf("NeutralizeVendor without the strip flag changed the vendor: %q, %v", got, changed)
	}
}
