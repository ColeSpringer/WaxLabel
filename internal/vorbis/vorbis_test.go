package vorbis

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestRebuildDropsReservedKey checks that a new custom key in a reserved namespace
// (CHAPTERxxx, SYNCEDLYRICS, METADATA_BLOCK_PICTURE) is dropped, recorded in
// ReservedKeys, and warned value-dropped naming the namespace. The synced-lyrics and
// picture payloads are valid, pinning that a valid payload is still dropped.
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

// TestRebuildSetOnExistingPictureCommentDrops checks that --set METADATA_BLOCK_PICTURE on
// a file holding a malformed picture comment drops the set value with a warning and
// preserves the existing comment verbatim, instead of overwriting it in place via the
// generic key path.
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

// TestPictureDecodePreservesStoredMIME checks that ParsePicture and DecodePictureComment
// return the MIME as stored, never sniffed, so a mislabeled cover's on-disk label
// survives an unrelated edit.
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

// TestPictureCommentLenMatchesRender pins PictureCommentLen to the RenderPicture layout,
// so the write-side size guard cannot under-count a cover if RenderPicture changes.
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

// TestParseCommentListCountCapped checks that ParseCommentList stops at maxElements with
// ErrSizeTooLarge, since the attacker-controlled count would otherwise amplify a run of
// minimum entries into one Comment descriptor each, and that a zero cap stays unbounded.
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

// TestCheckCommentCount is the writer's side of that cap: a list a reader with default limits
// refuses is not written, unless the file was read holding as many.
func TestCheckCommentCount(t *testing.T) {
	max := bits.DefaultLimits.MaxElements
	for _, tc := range []struct {
		n, orig int
		refused bool
	}{
		{max, 0, false},
		{max + 1, 0, true},
		{max + 50, max + 50, false},
		{max + 51, max + 50, true},
	} {
		err := CheckCommentCount(tc.n, tc.orig)
		if got := errors.Is(err, waxerr.ErrContentTooLarge); got != tc.refused || (err != nil) != tc.refused {
			t.Errorf("%d comments on a file read with %d: err = %v, refused want %v", tc.n, tc.orig, err, tc.refused)
		}
	}
}

// TestParseCommentListReportsConsumed checks the bytes-consumed value the Ogg codecs use
// to find the Vorbis framing bit and preserve Opus padding. The tail is a well-formed
// extra entry past the declared count: a parser that ignored the count would swallow
// it, which a plain non-"=" tail could not detect.
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

// TestProjectMarksConflicts checks that two distinct native names mapping to one
// canonical key with disagreeing values are a conflict, while a repeated name is a
// multi-value.
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

// TestRebuildMinimalChange checks that the rebuild keeps unchanged comments verbatim,
// replaces a changed key in place, drops aliases of a changed key, and appends new keys.
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

// TestRebuildPreservesEditedKeyCasing checks that editing an existing key keeps the
// file's spelling (lowercase "title" stays "title"), untouched keys stay verbatim, and
// an edited alias canonicalizes to its preferred spelling (DATE).
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

// TestEncoderNoiseDeduplicatesVendorEcho checks that a transcoder stamp in both the
// vendor string and an ENCODER comment is reported once, and distinct stamps twice.
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
	// The substitution is reported, by key and once per key.
	ws := InvalidTextWarnings([]Comment{
		{Name: "artist", Value: "bad\xff\xfevalue"}, {Name: "ARTIST", Value: "worse\xff"}, {Name: "TITLE", Value: "fine"},
	})
	if len(ws) != 1 || ws[0].Code != core.WarnInvalidText || !slices.Equal(ws[0].Keys, []tag.Key{tag.Artist}) {
		t.Errorf("warnings = %+v, want one invalid-text keyed ARTIST", ws)
	}
	// A valid value is untouched.
	valid := []Comment{{Name: "ARTIST", Value: "Valid ☃"}}
	if ts2, _ := Project(valid); func() bool {
		v, _ := ts2.First(tag.Artist)
		return v != "Valid ☃"
	}() {
		t.Error("Project altered a valid UTF-8 value")
	}
	if ws := InvalidTextWarnings(valid); len(ws) != 0 {
		t.Errorf("a valid value warned: %+v", ws)
	}
}

// TestInvalidTextWarningsOwnedComments: a chapter title or synced lyric read with a substitution
// is reported too, with no key and once for the titles; a comment nothing shows is not, nor is
// one under a name that is no key's spelling.
func TestInvalidTextWarningsOwnedComments(t *testing.T) {
	comments := []Comment{
		{Name: "CHAPTER001", Value: "00:00:00.000"}, {Name: "CHAPTER001NAME", Value: "Intr\xf6"},
		{Name: "CHAPTER002", Value: "00:00:05.000"}, {Name: "CHAPTER002NAME", Value: "Vers\xe9"},
		{Name: "SYNCEDLYRICS", Value: "[00:01.00]caf\xe9"},
		{Name: " ALBUM", Value: "bad\xff"}, {Value: "noseparator\xff", Unseparated: true},
		// Nothing shows a comment its projector passes over, with or without a substitution.
		{Name: "CHAPTER009NAME", Value: "stray\xff"}, {Name: "SYNCEDLYRICS", Value: "[00:02.00]second\xff"},
	}
	if ws := InvalidTextWarnings(comments); len(ws) != 0 {
		t.Errorf("the tag warnings cover an owned or opaque comment: %+v", ws)
	}
	ws := core.WarningsWithCode(ProjectOwned(comments).Warnings, core.WarnInvalidText)
	if len(ws) != 2 || len(ws[0].Keys)+len(ws[1].Keys) != 0 {
		t.Fatalf("warnings = %+v, want two keyless invalid-text", ws)
	}
	if !strings.Contains(ws[0].Message, "CHAPTER001NAME") || !strings.Contains(ws[0].Message, "and 1 more") || !strings.Contains(ws[1].Message, "SYNCEDLYRICS") {
		t.Errorf("warnings do not name the comments: %+v", ws)
	}
}

// TestRebuildMatchesStoredBytesInOnePass: writing stored values back costs the same for a key with
// many values as for one with few. A value-by-value search through them took minutes here.
func TestRebuildMatchesStoredBytesInOnePass(t *testing.T) {
	const n = 20000
	orig := make([]Comment, 0, 2*n)
	for i := range n {
		orig = append(orig, Comment{Name: "COMMENT", Value: "ok " + strconv.Itoa(i)})
	}
	for i := range n {
		orig = append(orig, Comment{Name: "COMMENT", Value: "bad\xff " + strconv.Itoa(i)})
	}
	base, _ := Project(orig)
	edited := base.Clone()
	edited.Add(tag.Comment, "one more")

	start := time.Now()
	got, _ := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the rebuild took %v", d)
	}
	if want := append(slices.Clone(orig), Comment{Name: "COMMENT", Value: "one more"}); !slices.Equal(got, want) {
		t.Errorf("the rebuild of %d values changed one it was to keep", len(want))
	}
}

// TestRebuildMatchesStoredBytesByPosition: of two values that read alike, the one an edit leaves
// in place keeps its own bytes, not those of the value that stood before it.
func TestRebuildMatchesStoredBytesByPosition(t *testing.T) {
	orig := []Comment{{Name: "TITLE", Value: "Caf\xe9"}, {Name: "TITLE", Value: "Caf\xff"}}
	base, _ := Project(orig)
	for name, tc := range map[string]struct {
		vals []string
		want []string
	}{
		"the first replaced":   {[]string{"new", "Caf\uFFFD"}, []string{"new", "Caf\xff"}},
		"the second replaced":  {[]string{"Caf\uFFFD", "new"}, []string{"Caf\xe9", "new"}},
		"one added":            {[]string{"Caf\uFFFD", "Caf\uFFFD", "new"}, []string{"Caf\xe9", "Caf\xff", "new"}},
		"moved behind another": {[]string{"new", "other", "Caf\uFFFD"}, []string{"new", "other", "Caf\xe9"}},
	} {
		edited := base.Clone()
		edited.Set(tag.Title, tc.vals...)
		got, _ := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
		var vals []string
		for _, cm := range got {
			vals = append(vals, cm.Value)
		}
		if !slices.Equal(vals, tc.want) {
			t.Errorf("%s: wrote %q, want %q", name, vals, tc.want)
		}
	}
}

// TestCommentCountConstraint: the limit is grouped in thousands whatever its size.
func TestCommentCountConstraint(t *testing.T) {
	old := bits.DefaultLimits.MaxElements
	defer func() { bits.DefaultLimits.MaxElements = old }()
	for n, want := range map[int]string{
		500: "at most 500 comments", 100000: "at most 100,000 comments", 1000000: "at most 1,000,000 comments", 1234567: "at most 1,234,567 comments",
	} {
		bits.DefaultLimits.MaxElements = n
		if got := CommentCountConstraint(); got != want {
			t.Errorf("limit %d: %q, want %q", n, got, want)
		}
	}
}

// TestRebuildKeepsInvalidUTF8Bytes: a value read with U+FFFD in place of invalid bytes is written
// back as the file holds it when an edit of its key keeps it.
func TestRebuildKeepsInvalidUTF8Bytes(t *testing.T) {
	orig := []Comment{{Name: "TITLE", Value: "caf\xe9"}, {Name: "ALBUM", Value: "A"}}
	base, _ := Project(orig)

	edited := base.Clone()
	edited.Add(tag.Title, "second")
	got, _ := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
	want := []Comment{{Name: "TITLE", Value: "caf\xe9"}, {Name: "TITLE", Value: "second"}, {Name: "ALBUM", Value: "A"}}
	if !slices.Equal(got, want) {
		t.Errorf("an added value: rebuild = %v, want %v", got, want)
	}

	edited = base.Clone()
	edited.Set(tag.Artist, "Band")
	got, _ = Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
	if want := append(slices.Clone(orig), Comment{Name: "ARTIST", Value: "Band"}); !slices.Equal(got, want) {
		t.Errorf("an unrelated edit: rebuild = %v, want %v", got, want)
	}

	edited = base.Clone()
	edited.Set(tag.Title, "replaced")
	got, _ = Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
	if want := []Comment{{Name: "TITLE", Value: "replaced"}, {Name: "ALBUM", Value: "A"}}; !slices.Equal(got, want) {
		t.Errorf("a replaced value: rebuild = %v, want %v", got, want)
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
// ID3/FLAC role space reads as PicOther instead of wrapping into a valid role (259 & 0xFF
// == 3, "Front cover"), with the image bytes preserved. FLAC PICTURE blocks and Ogg
// METADATA_BLOCK_PICTURE comments share the decoder.
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

// TestProjectSkipsPictureComment checks that picture comments, including malformed ones
// the parser keeps opaque, appear in neither the tag view nor the family values.
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

// TestRebuildPreservesPictureComment checks that an opaque picture comment survives an
// unrelated tag edit as an ordinary preserved comment.
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

// TestNeutralizeVendorCodecStamp checks that libavcodec and libavformat vendor stamps
// are both neutralized, and an encoder's own vendor string survives untouched, since no
// canonical key can restore it.
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

// TestNonConformingNamesStayOpaque: a field name with a byte outside ASCII or whitespace at an edge
// is no key's spelling. It is not read as the key that Unicode case folding or trimming would make
// of it, it is reported by name, and an edit of that key leaves its bytes alone and writes the field.
func TestNonConformingNamesStayOpaque(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tag.Key
	}{
		{"tıtle", tag.Title},
		{"ARTıST", tag.Artist},
		{" ALBUM", tag.Album},
		{"GENRE ", tag.Genre},
	} {
		orig := []Comment{{Name: tc.name, Value: "theirs"}}
		if ts, fams := Project(orig); ts.Len() != 0 || len(fams) != 0 {
			t.Errorf("%q: projected %v with %d family entries, want nothing", tc.name, ts.Keys(), len(fams))
		}
		ws := InvalidKeyWarnings(orig)
		if len(ws) != 1 || ws[0].Code != core.WarnInvalidTagKey || !strings.Contains(ws[0].Message, `"`+tc.name+`"`) {
			t.Errorf("%q: warnings = %v, want one invalid-tag-key quoting the name", tc.name, ws)
		}
		edited := tag.NewTagSet()
		edited.Set(tc.key, "ours")
		got, _ := Rebuild(orig, edited, map[tag.Key]bool{tc.key: true}, nil, false, nil, false)
		if want := []Comment{orig[0], {Name: string(tc.key), Value: "ours"}}; !slices.Equal(got, want) {
			t.Errorf("%q: rebuild = %v, want %v", tc.name, got, want)
		}
	}

	// An ASCII spelling in another case is still the key.
	orig := []Comment{{Name: "title", Value: "theirs"}}
	if ts, _ := Project(orig); !ts.AnyValue(tag.Title, func(v string) bool { return v == "theirs" }) {
		t.Errorf("title: projected %v, want TITLE", ts.Keys())
	}
	if ws := InvalidKeyWarnings(orig); len(ws) != 0 {
		t.Errorf("title: warnings = %v, want none", ws)
	}
}

// TestOwnedNamesFoldASCIIOnly: the owned comment names match in any ASCII case, and a letter that
// only Unicode case folding equates with one of theirs (long s, the Kelvin sign) is another name.
func TestOwnedNamesFoldASCIIOnly(t *testing.T) {
	for name, want := range map[string]bool{
		"syncedlyrics": true, "SyncedLyrics": true, "ſYNCEDLYRICS": false,
	} {
		if got := isSyncedLyricsComment(name); got != want {
			t.Errorf("isSyncedLyricsComment(%q) = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{
		"metadata_block_picture": true, "Metadata_Block_Picture": true, "METADATA_BLOCK_PICTURE": false,
	} {
		if got := IsPictureComment(name); got != want {
			t.Errorf("IsPictureComment(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestDecodePictureCommentUnpadded: a cover whose base64 lacks its padding, as some writers leave
// it, decodes like the padded form; text that is not base64 either way is still refused.
func TestDecodePictureCommentUnpadded(t *testing.T) {
	for _, n := range []int{1, 2, 3} { // one of each padding length
		want := core.Picture{Type: core.PicFrontCover, MIME: "image/png", Data: make([]byte, n)}
		padded := base64.StdEncoding.EncodeToString(RenderPicture(want))
		bare := strings.TrimRight(padded, "=")
		// A line break after either form, or inside it, is not part of the encoding.
		for _, value := range []string{padded, bare, padded + "\n", padded + "\r\n", bare + "\n", padded[:8] + "\n" + padded[8:]} {
			got, err := DecodePictureComment(value, 1<<20)
			if err != nil || got.MIME != want.MIME || len(got.Data) != n {
				t.Errorf("%d data byte(s), %q: picture %+v, err %v", n, value, got, err)
			}
		}
	}
	if _, err := DecodePictureComment("not~base64", 1<<20); err == nil {
		t.Error("text that is not base64 decoded")
	}
}

// TestRebuildDropsKeyWithEdgeWhitespace: a key that passes Key.Valid with a space at its edge would
// be written under a name the reader takes for no key, so it is dropped and reported instead.
func TestRebuildDropsKeyWithEdgeWhitespace(t *testing.T) {
	orig := []Comment{{Name: "TITLE", Value: "x"}}
	for _, key := range []tag.Key{" EDGE", "EDGE "} {
		if !key.Valid() {
			t.Fatalf("setup: %q is not a valid key", key)
		}
		base, _ := Project(orig)
		edited := base.Clone()
		edited.Set(key, "v")
		got, info := Rebuild(orig, edited, DiffKeys(base, edited), nil, false, nil, false)
		if !slices.Equal(got, orig) || !slices.Equal(info.UnreadableKeys, []tag.Key{key}) {
			t.Errorf("%q: rebuild = %v, dropped %q; want the list unchanged and the key recorded", key, got, info.UnreadableKeys)
		}
		ws := RebuildWarnings(nil, info)
		if len(ws) != 1 || ws[0].Code != core.WarnValueDropped || !slices.Equal(ws[0].Keys, []tag.Key{key}) {
			t.Errorf("%q: warnings = %+v, want one value-dropped keyed to it", key, ws)
		}
	}
}
