package id3

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestSizeErrHumanized checks the size-limit messages report humanized binary magnitudes
// rather than raw byte counts.
func TestSizeErrHumanized(t *testing.T) {
	apic := sizeErr(Frame{ID: "APIC"}, 60*1024*1024)
	if !strings.Contains(apic.Error(), "MiB") {
		t.Errorf("APIC size error = %q, want a humanized MiB message", apic.Error())
	}
	frame := sizeErr(Frame{ID: "TIT2"}, 60*1024*1024)
	if !strings.Contains(frame.Error(), "MiB") {
		t.Errorf("frame size error = %q, want a humanized MiB message", frame.Error())
	}
}

func TestSyncSafeRoundTrip(t *testing.T) {
	for _, v := range []int64{0, 1, 127, 128, 16384, 0xFFFFFFF} {
		var b [4]byte
		putSyncSafe(b[:], v)
		for _, x := range b {
			if x&0x80 != 0 {
				t.Errorf("sync-safe byte 0x%02x has the high bit set", x)
			}
		}
		if got := syncSafe(b[:]); got != v {
			t.Errorf("syncSafe round-trip: got %d, want %d", got, v)
		}
	}
}

func TestEncodingRoundTrip(t *testing.T) {
	cases := []struct {
		enc byte
		s   string
	}{
		{encLatin1, "Hello"},
		{encLatin1, "Café"}, // é is Latin-1
		{encUTF16, "Hello"},
		{encUTF16, "日本語"}, // Japanese
		{encUTF16BE, "Test éè"},
		{encUTF8, "日本 \U0001F3B5"}, // includes an astral codepoint
	}
	for _, c := range cases {
		enc := encodeString(c.enc, c.s)
		got := decodeString(c.enc, enc)
		if got != c.s {
			t.Errorf("enc %d: round-trip %q -> %q", c.enc, c.s, got)
		}
	}
}

func TestDecodeStringsMultiValue(t *testing.T) {
	body := encodeTextFrame(encUTF8, []string{"One", "Two", "Three"})
	got := decodeTextFrame(body)
	if !slices.Equal(got, []string{"One", "Two", "Three"}) {
		t.Errorf("multi-value decode = %v", got)
	}
	// UTF-16 multi-value (two-byte terminator).
	body16 := encodeTextFrame(encUTF16, []string{"A", "B"})
	if got := decodeTextFrame(body16); !slices.Equal(got, []string{"A", "B"}) {
		t.Errorf("utf16 multi-value decode = %v", got)
	}
}

func TestDecodeStringsTrailingPadding(t *testing.T) {
	// A foreign frame ending in a double NUL must not yield a phantom trailing empty,
	// which would defeat no-op detection. Matches TagLib/mutagen.
	if got := decodeStrings(encLatin1, []byte("Hello\x00\x00")); !slices.Equal(got, []string{"Hello"}) {
		t.Errorf("double-NUL latin1 decode = %v, want [Hello]", got)
	}
	if got := decodeStrings(encUTF8, []byte("Hello\x00\x00")); !slices.Equal(got, []string{"Hello"}) {
		t.Errorf("double-NUL utf8 decode = %v, want [Hello]", got)
	}
	// Trailing-only: an interior present-empty value in a genuine multi-value frame is
	// preserved; only the trailing padding empty is dropped.
	if got := decodeStrings(encLatin1, []byte("A\x00\x00B\x00\x00")); !slices.Equal(got, []string{"A", "", "B"}) {
		t.Errorf("interior-empty decode = %v, want [A,\"\",B]", got)
	}
	// A frame that is nothing but terminators still decodes to a single empty value.
	if got := decodeStrings(encLatin1, []byte("\x00\x00")); !slices.Equal(got, []string{""}) {
		t.Errorf("all-terminator decode = %v, want [\"\"]", got)
	}
	// The pre-existing single-trailing-terminator case is unchanged.
	if got := decodeStrings(encLatin1, []byte("Solo\x00")); !slices.Equal(got, []string{"Solo"}) {
		t.Errorf("single-terminator decode = %v, want [Solo]", got)
	}
}

func TestReducesDatePrecisionSeconds(t *testing.T) {
	// v2.3 TIME stores only HHMM, so seconds past a full minute are dropped, like the
	// month/hour reductions.
	truthy := []string{
		"2020-07-04T13:05:45",       // seconds dropped
		"2020-07-04T13:05:45+05:00", // seconds present even with a trailing zone -> dropped
		"2020-07-04 13:05:45",       // a space date-time separator is accepted too
	}
	for _, iso := range truthy {
		if !reducesDatePrecision(iso) {
			t.Errorf("reducesDatePrecision(%q) = false, want true (v2.3 TIME drops the seconds)", iso)
		}
	}
	falsy := []string{
		"2020-07-04T13:05",       // minute precision: stored losslessly, no over-warn
		"2020-07-04T13:05+05:00", // a zone but no seconds: the documented seconds-only-scope gap
		"2020-07-04",             // a full date
		"2020",                   // a bare year
	}
	for _, iso := range falsy {
		if reducesDatePrecision(iso) {
			t.Errorf("reducesDatePrecision(%q) = true, want false (no seconds to drop)", iso)
		}
	}
}

func TestDeunsync(t *testing.T) {
	in := []byte{0xFF, 0x00, 0xFB, 0x10, 0xFF, 0x00, 0x00}
	want := []byte{0xFF, 0xFB, 0x10, 0xFF, 0x00}
	if got := deunsync(in); !bytes.Equal(got, want) {
		t.Errorf("deunsync = % x, want % x", got, want)
	}
	// No 0xFF: returned unchanged.
	plain := []byte{1, 2, 3}
	if got := deunsync(plain); !bytes.Equal(got, plain) {
		t.Errorf("deunsync plain = % x", got)
	}
}

// TestTagLevelUnsyncOpaqueFrame covers opaque v2.4 frames whose bodies need unsync
// normalization: tag-level and frame-level unsync both normalize the body, and a frame
// with no unsync flag keeps its bytes and flags.
func TestTagLevelUnsyncOpaqueFrame(t *testing.T) {
	rawBody := []byte{0xFF, 0x00, 0x42, 0xFF, 0x00} // FF 00 stuffing; de-unsyncs to:
	deunsynced := []byte{0xFF, 0x42, 0xFF}

	build := func(tagUnsync bool, frameFlag1 byte) []byte {
		var fsz [4]byte
		putSyncSafe(fsz[:], int64(len(rawBody)))
		frame := append([]byte("TIT2"), fsz[:]...)
		frame = append(frame, 0x00, frameFlag1)
		frame = append(frame, rawBody...)
		var tsz [4]byte
		putSyncSafe(tsz[:], int64(len(frame)))
		hdrFlags := byte(0)
		if tagUnsync {
			hdrFlags = hdrUnsync
		}
		data := append([]byte{'I', 'D', '3', 4, 0, hdrFlags}, tsz[:]...)
		return append(data, frame...)
	}

	for _, c := range []struct {
		name          string
		tagUnsync     bool
		frameFlag1    byte
		wantBody      []byte
		wantUnsyncBit bool
	}{
		// Tag-level unsync, frame bit clear: body normalized and no frame bit to clear.
		{"tag-level", true, v24Compression, deunsynced, false},
		// Frame-level unsync bit set: body normalized and the bit cleared.
		{"frame-level", false, v24Compression | v24Unsync, deunsynced, false},
		// No unsync anywhere: the FF 00 is genuine payload, preserved verbatim.
		{"none", false, v24Compression, rawBody, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tg, err := ParseTag(build(c.tagUnsync, c.frameFlag1), 0)
			if err != nil {
				t.Fatalf("ParseTag: %v", err)
			}
			if len(tg.frames) != 1 || !tg.frames[0].Opaque {
				t.Fatalf("want 1 opaque frame, got %+v", tg.frames)
			}
			f := tg.frames[0]
			if !bytes.Equal(f.Body, c.wantBody) {
				t.Errorf("body = % x, want % x", f.Body, c.wantBody)
			}
			if (f.Flags[1]&v24Unsync != 0) != c.wantUnsyncBit {
				t.Errorf("unsync bit set = %v, want %v (flags=% x)", f.Flags[1]&v24Unsync != 0, c.wantUnsyncBit, f.Flags)
			}
			if f.Flags[1]&v24Compression == 0 {
				t.Errorf("compression bit was not preserved, flags=% x", f.Flags)
			}
		})
	}
}

func TestResolveGenres(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		numeric bool
	}{
		{"(17)", []string{"Rock"}, true},
		{"17", []string{"Rock"}, true},
		{"7", []string{"Hip-Hop"}, true}, // bare canonical index resolves
		{"0", []string{"Blues"}, true},   // 0 is a valid index, not "empty"
		{"Rock", []string{"Rock"}, false},
		{"(51)(39)", []string{"Techno-Industrial", "Noise"}, true}, // multiple numeric references
		{"(17)Hardcore", []string{"Rock", "Hardcore"}, true},       // reference + refinement
		{"(17)Rock", []string{"Rock"}, true},                       // refinement repeating the reference name is folded
		{"(17)rock", []string{"Rock", "rock"}, true},               // case-folded repeat is out of scope: kept as authored
		{"(Indie)Refined", []string{"(Indie)", "Refined"}, false},  // non-numeric parenthetical kept verbatim
		{"(RX)", []string{"Remix"}, true},
		{"(CR)", []string{"Cover"}, true},
		{"RX", []string{"Remix"}, true},     // bare special reference resolves like (RX)
		{"CR", []string{"Cover"}, true},     // bare special reference resolves like (CR)
		{"rx", []string{"rx"}, false},       // case-sensitive (the spec form is uppercase): lowercase stays a literal name
		{"(rx)", []string{"(rx)"}, false},   // parenthesized lowercase is literal too, parens preserved
		{"(255)", []string{"(255)"}, false}, // out of range: kept literal AND no longer flagged numeric
		{"Custom Genre", []string{"Custom Genre"}, false},
		// Only a canonical integer is a bare ID3v1 index; a padded or signed form stays literal.
		{"007", []string{"007"}, false},
		{"+7", []string{"+7"}, false},
		{"08", []string{"08"}, false},
		{"-5", []string{"-5"}, false}, // passes the Itoa guard, rejected by genreName's range check
		// A parenthesized zero-padded index still resolves as an explicit reference, while an
		// out-of-range one and a foreign name or empty token stay literal and non-numeric.
		{"(07)", []string{"Hip-Hop"}, true}, // parenthesized zero-pad is an explicit index reference
		{"(192)", []string{"(192)"}, false},
		{"(Pop)", []string{"(Pop)"}, false},
		{"()", []string{"()"}, false},
		// An unterminated reference's literal text keeps its lone "(" verbatim rather than dropping
		// it (matching the paren-token preservation above); the "((" escape still unescapes, even
		// after a space before the escaped paren.
		{"(hello", []string{"(hello"}, false},
		{"(17)(hello", []string{"Rock", "(hello"}, true},
		{"(17) ((Live)", []string{"Rock", "(Live)"}, true},
		// A name keeps its whitespace, as a Vorbis GENRE does; a reference reads through it, and
		// an all-whitespace value is nothing.
		{"  padded  ", []string{"  padded  "}, false},
		{" 17 ", []string{"Rock"}, true},
		{" (17) ", []string{"Rock"}, true},
		{"   ", nil, false},
	}
	for _, c := range cases {
		got, num := resolveGenres(c.in)
		if !slices.Equal(got, c.want) || num != c.numeric {
			t.Errorf("resolveGenres(%q) = (%v, %v), want (%v, %v)", c.in, got, num, c.want, c.numeric)
		}
	}
}

// buildTag renders frames into a tag and reparses it, exercising Render+ParseTag.
func buildTag(t *testing.T, version byte, frames []Frame) *Tag {
	t.Helper()
	data := Render(version, frames, 0)
	tg, err := ParseTag(data, 0)
	if err != nil {
		t.Fatalf("ParseTag: %v", err)
	}
	return tg
}

// TestRewriteBase covers the shared WAV/AIFF ID3 diff base: tagless files use an empty
// base, a legacy strip uses only the parsed ID3 frames so native-only values get written
// into ID3, and normal rewrites use the merged projection.
func TestRewriteBase(t *testing.T) {
	// srcTag carries only TITLE from the ID3 chunk. base is the merged projection:
	// that TITLE plus ARTIST promoted from the native container.
	srcTag := buildTag(t, 4, []Frame{{ID: "TIT2", Body: encodeTextFrame(encLatin1, []string{"SrcTitle"})}})
	base := tag.NewTagSet()
	base.Set(tag.Title, "SrcTitle")
	base.Set(tag.Artist, "NativeArtist")

	// No existing ID3 chunk: the full promoted set renders into a new chunk.
	if got := RewriteBase(base, srcTag, false, false); got.Len() != 0 {
		t.Errorf("!id3Present: base has %d keys, want 0 (empty)", got.Len())
	}
	// !id3Present takes precedence even when stripNative is set.
	if got := RewriteBase(base, srcTag, false, true); got.Len() != 0 {
		t.Errorf("!id3Present+strip: base has %d keys, want 0", got.Len())
	}
	// ID3 present, not stripping: use the merged base, including native ARTIST.
	if got := RewriteBase(base, srcTag, true, false); !got.Has(tag.Artist) {
		t.Error("id3Present, no strip: want the merged base (with native-only ARTIST)")
	}
	// ID3 present, stripping the native container: compare against the ID3 chunk
	// only, so native ARTIST is treated as an addition.
	stripped := RewriteBase(base, srcTag, true, true)
	if v, _ := stripped.First(tag.Title); v != "SrcTitle" {
		t.Errorf("strip: Title = %q, want SrcTitle (from the id3 chunk's own frame)", v)
	}
	if stripped.Has(tag.Artist) {
		t.Error("strip: native-only ARTIST must be absent from the rebuild base so it renders as an addition")
	}
}

func TestProjectTextFrames(t *testing.T) {
	frames := []Frame{
		{ID: "TIT2", Body: encodeTextFrame(encLatin1, []string{"My Title"})},
		{ID: "TPE1", Body: encodeTextFrame(encUTF8, []string{"Artist One", "Artist Two"})},
		{ID: "TRCK", Body: encodeTextFrame(encLatin1, []string{"4/10"})},
		{ID: "TCON", Body: encodeTextFrame(encLatin1, []string{"(17)"})},
	}
	proj := Project(buildTag(t, 4, frames))
	ts := proj.Tags
	if v, _ := ts.First(tag.Title); v != "My Title" {
		t.Errorf("Title = %q", v)
	}
	if v, _ := ts.Get(tag.Artist); !slices.Equal(v, []string{"Artist One", "Artist Two"}) {
		t.Errorf("Artist = %v", v)
	}
	if v, _ := ts.First(tag.TrackNumber); v != "4" {
		t.Errorf("TrackNumber = %q", v)
	}
	if v, _ := ts.First(tag.TrackTotal); v != "10" {
		t.Errorf("TrackTotal = %q", v)
	}
	if v, _ := ts.First(tag.Genre); v != "Rock" {
		t.Errorf("Genre = %q", v)
	}
	if !proj.NumericGenre {
		t.Error("expected NumericGenre to be reported")
	}
}

// A pre-existing bare TCON of RX or CR, as another tool might write, projects to
// Remix or Cover and sets NumericGenre. The frame is built directly instead of
// through the writer so the test covers read-side behavior.
func TestProjectBareSpecialGenreReinterpreted(t *testing.T) {
	for _, c := range []struct{ raw, want string }{{"RX", "Remix"}, {"CR", "Cover"}} {
		frames := []Frame{{ID: "TCON", Body: encodeTextFrame(encLatin1, []string{c.raw})}}
		proj := Project(buildTag(t, 4, frames))
		if v, _ := proj.Tags.First(tag.Genre); v != c.want {
			t.Errorf("bare TCON %q projected Genre = %q, want %q", c.raw, v, c.want)
		}
		if !proj.NumericGenre {
			t.Errorf("bare TCON %q should set NumericGenre so the read-side warning fires", c.raw)
		}
	}
}

// TestNumericGenreWarningSurfaces checks the two numeric-genre surfaces agree: the read
// projection's NumericGenre flag and detectNumericGenres. Both consume resolveGenres, so
// a zero-padded "007" and an out-of-range "(192)" warn on neither, while a canonical "7"
// warns on both.
func TestNumericGenreWarningSurfaces(t *testing.T) {
	// Read surface: NumericGenre must be false for the newly-literal forms, true for a real index.
	for _, c := range []struct {
		raw  string
		want bool
	}{{"007", false}, {"(192)", false}, {"7", true}} {
		frames := []Frame{{ID: "TCON", Body: encodeTextFrame(encLatin1, []string{c.raw})}}
		if got := Project(buildTag(t, 4, frames)).NumericGenre; got != c.want {
			t.Errorf("read surface: TCON %q -> NumericGenre %v, want %v", c.raw, got, c.want)
		}
	}
	// Write surface: an edit setting GENRE to a newly-literal form must not be flagged; a canonical
	// index still is. changed[Genre] gates detection, so mark it.
	changed := map[tag.Key]bool{tag.Genre: true}
	for _, c := range []struct {
		val  string
		want []string
	}{{"007", nil}, {"(192)", nil}, {"7", []string{"7"}}} {
		edited := tag.NewTagSet()
		edited.Set(tag.Genre, c.val)
		if got := detectNumericGenres(changed, edited); !slices.Equal(got, c.want) {
			t.Errorf("write surface: GENRE=%q -> detectNumericGenres %v, want %v", c.val, got, c.want)
		}
	}
}

func TestV22Upgrade(t *testing.T) {
	// Hand-build a v2.2 tag: "ID3" 02 00 00 <size>, then TT2 + size(3) + body.
	body := append([]byte{encLatin1}, "v2.2 Title"...)
	frame := append([]byte("TT2"), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	frame = append(frame, body...)
	var sz [4]byte
	putSyncSafe(sz[:], int64(len(frame)))
	data := append([]byte{'I', 'D', '3', 2, 0, 0}, sz[:]...)
	data = append(data, frame...)

	tg, err := ParseTag(data, 0)
	if err != nil {
		t.Fatalf("ParseTag: %v", err)
	}
	if tg.SrcVersion() != 2 || tg.WriteVersion() != 3 {
		t.Errorf("versions = src %d write %d, want src 2 write 3", tg.SrcVersion(), tg.WriteVersion())
	}
	if len(tg.Frames()) != 1 || tg.Frames()[0].ID != "TIT2" {
		t.Fatalf("frames = %+v, want one TIT2", tg.Frames())
	}
	if v, _ := Project(tg).Tags.First(tag.Title); v != "v2.2 Title" {
		t.Errorf("Title = %q", v)
	}
}

func TestRebuildMinimalChange(t *testing.T) {
	orig := []Frame{
		{ID: "TIT2", Body: encodeTextFrame(encLatin1, []string{"Old"})},
		{ID: "TPE1", Body: encodeTextFrame(encLatin1, []string{"Keep Me"})},
		{ID: "PRIV", Body: []byte{1, 2, 3}}, // unmodelled, must be preserved verbatim
	}
	base := Project(&Tag{frames: orig}).Tags
	edited := base.Clone()
	edited.Set(tag.Title, "New")

	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})

	var ids []string
	for _, f := range out {
		ids = append(ids, f.ID)
	}
	// TIT2 re-rendered, TPE1 + PRIV preserved, order kept.
	if !slices.Equal(ids, []string{"TIT2", "TPE1", "PRIV"}) {
		t.Errorf("frame order/ids = %v", ids)
	}
	for _, f := range out {
		switch f.ID {
		case "TIT2":
			if got := decodeTextFrame(f.Body); !slices.Equal(got, []string{"New"}) {
				t.Errorf("TIT2 = %v", got)
			}
		case "TPE1":
			if got := decodeTextFrame(f.Body); !slices.Equal(got, []string{"Keep Me"}) {
				t.Errorf("TPE1 changed: %v", got)
			}
		case "PRIV":
			if !bytes.Equal(f.Body, []byte{1, 2, 3}) {
				t.Errorf("PRIV not preserved: % x", f.Body)
			}
		}
	}
}

func TestRebuildClearDropsFrame(t *testing.T) {
	orig := []Frame{{ID: "TIT2", Body: encodeTextFrame(encLatin1, []string{"Bye"})}}
	base := Project(&Tag{frames: orig}).Tags
	edited := base.Clone()
	edited.Delete(tag.Title)
	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})
	if len(out) != 0 {
		t.Errorf("cleared title should drop the frame, got %+v", out)
	}
}

func TestDateDecompositionV23(t *testing.T) {
	// Round-trip a full date through v2.3 TYER/TDAT/TIME.
	base := tag.NewTagSet()
	edited := tag.NewTagSet()
	edited.Set(tag.RecordingDate, "2021-06-15T08:30")
	out, _ := RebuildFrames(nil, base, edited, 3, StructuredEdit{}, WriteOpts{})

	byID := map[string]string{}
	for _, f := range out {
		byID[f.ID] = decodeTextFrame(f.Body)[0]
	}
	if byID["TYER"] != "2021" || byID["TDAT"] != "1506" || byID["TIME"] != "0830" {
		t.Fatalf("decomposed parts = %v", byID)
	}
	// And re-projecting recomposes the ISO date.
	if v, _ := Project(buildTag(t, 3, out)).Tags.First(tag.RecordingDate); v != "2021-06-15T08:30" {
		t.Errorf("recomposed date = %q", v)
	}
}

// TestDroppedDateDetection checks the dropped-date fate: a date key whose edited value
// has no extractable year renders no v2.3 frame and lands in RebuildInfo.DroppedDates. A
// year-bearing date never fires, and v2.4 (TDRC stores the string) never populates it.
func TestDroppedDateDetection(t *testing.T) {
	cases := []struct {
		name    string
		key     tag.Key
		value   string
		version byte
		dropped bool
	}{
		{"v23 recording no year", tag.RecordingDate, "Unknown Date", 3, true},
		{"v23 original no year", tag.OriginalDate, "Unknown", 3, true},
		// ReleaseDate maps to TXXX:RELEASEDATE on v2.3 and stores the string verbatim, so
		// it is excluded from the year-anchored drop check.
		{"v23 release no year stored verbatim", tag.ReleaseDate, "Unknown", 3, false},
		{"v23 recording year only", tag.RecordingDate, "2021", 3, false},
		// A shaped-but-invalid date still has an extractable year, so only sub-year
		// precision is lost and it is not flagged dropped.
		{"v23 recording shaped-but-invalid keeps year", tag.RecordingDate, "2021-13-45", 3, false},
		// A malformed 5-digit year and a non-canonical compact form have no valid
		// 4-digit year (they must not truncate to "1000"/"2021"), so both drop entirely.
		{"v23 recording 5-digit year dropped", tag.RecordingDate, "10000", 3, true},
		{"v23 recording compact form dropped", tag.RecordingDate, "20210503", 3, true},
		{"v24 recording no year stores string", tag.RecordingDate, "Unknown", 4, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := tag.NewTagSet()
			edited := tag.NewTagSet()
			edited.Set(c.key, c.value)
			_, info := RebuildFrames(nil, base, edited, c.version, StructuredEdit{}, WriteOpts{})
			if got := slices.Contains(info.DroppedDates, c.key); got != c.dropped {
				t.Errorf("DroppedDates contains %s = %v, want %v (DroppedDates=%v)", c.key, got, c.dropped, info.DroppedDates)
			}
		})
	}
}

// TestDroppedDateOnlyTouchedKeys checks an unchanged date key (base == edited) is never
// flagged dropped; only a key the edit touched can be.
func TestDroppedDateOnlyTouchedKeys(t *testing.T) {
	base := tag.NewTagSet()
	base.Set(tag.RecordingDate, "Unknown")
	edited := tag.NewTagSet()
	edited.Set(tag.RecordingDate, "Unknown")
	if _, info := RebuildFrames(nil, base, edited, 3, StructuredEdit{}, WriteOpts{}); len(info.DroppedDates) != 0 {
		t.Errorf("an unchanged date must not be flagged dropped, got %v", info.DroppedDates)
	}
}

// TestReleaseDateV23StoredNotDropped confirms a non-date ReleaseDate string on v2.3
// renders a TXXX:RELEASEDATE frame, so detectDateFates rightly excludes it from the drop
// check.
func TestReleaseDateV23StoredNotDropped(t *testing.T) {
	base := tag.NewTagSet()
	edited := tag.NewTagSet()
	edited.Set(tag.ReleaseDate, "Unknown")
	out, info := RebuildFrames(nil, base, edited, 3, StructuredEdit{}, WriteOpts{})
	if len(info.DroppedDates) != 0 {
		t.Errorf("ReleaseDate must not be flagged dropped on v2.3, got %v", info.DroppedDates)
	}
	hasTXXX := false
	for _, f := range out {
		if f.ID == "TXXX" {
			hasTXXX = true
		}
	}
	if !hasTXXX {
		t.Errorf("ReleaseDate=Unknown on v2.3 should render a TXXX:RELEASEDATE frame, got %d frames", len(out))
	}
}

// TestDroppedTrailingValuesByPolicy pins the trailing-empty detector to the write
// representation: a NUL-separated frame drops a trailing empty and must warn;
// repeat-frame keeps it as its own frame and slash-join collapses the whole value, so
// neither warns. v2.4 always writes NUL-separated. A lone empty and a value with no
// trailing empty never flag.
func TestDroppedTrailingValuesByPolicy(t *testing.T) {
	trailing := []string{"A", "B", ""}
	flagged := func(keys []tag.Key) bool { return len(keys) == 1 && keys[0] == tag.Artist }
	cases := []struct {
		name    string
		version byte
		pol     core.ID3MultiValuePolicy
		vals    []string
		want    bool
	}{
		{"v23-nullsep drops", 3, core.ID3MultiNullSep, trailing, true},
		{"v23-repeat preserves", 3, core.ID3MultiRepeatFrame, trailing, false},
		{"v23-slash collapses", 3, core.ID3MultiSlash, trailing, false},
		{"v24-nullsep drops", 4, core.ID3MultiNullSep, trailing, true},
		{"v24-repeat is still nullsep", 4, core.ID3MultiRepeatFrame, trailing, true},
		{"lone empty round-trips", 3, core.ID3MultiNullSep, []string{""}, false},
		{"no trailing empty", 3, core.ID3MultiNullSep, []string{"A", "B"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edited := tag.NewTagSet()
			edited.Set(tag.Artist, c.vals...)
			_, info := RebuildFrames(nil, tag.NewTagSet(), edited, c.version, StructuredEdit{}, WriteOpts{Multi: c.pol})
			if got := flagged(info.DroppedTrailingValues); got != c.want {
				t.Errorf("DroppedTrailingValues flagged=%v, want %v (keys=%v)", got, c.want, info.DroppedTrailingValues)
			}
		})
	}
}

func TestNumericGenreWrite(t *testing.T) {
	base := tag.NewTagSet()
	edited := tag.NewTagSet()
	edited.Set(tag.Genre, "Rock")
	out, _ := RebuildFrames(nil, base, edited, 4, StructuredEdit{}, WriteOpts{NumericGenre: true})
	if len(out) != 1 || out[0].ID != "TCON" {
		t.Fatalf("expected one TCON frame, got %+v", out)
	}
	if v := decodeTextFrame(out[0].Body)[0]; v != "17" {
		t.Errorf("numeric genre = %q, want 17", v)
	}
}

// TestGenreParenEscapeRoundTrip checks that literal genre names beginning with "("
// survive a writer-to-reader round trip instead of being parsed as genre references.
func TestGenreParenEscapeRoundTrip(t *testing.T) {
	roundTrip := func(t *testing.T, version byte, pol core.ID3MultiValuePolicy, numeric bool, in []string) []string {
		t.Helper()
		edited := tag.NewTagSet()
		edited.Set(tag.Genre, in...)
		out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, version, StructuredEdit{},
			WriteOpts{Multi: pol, NumericGenre: numeric})
		got, _ := Project(buildTag(t, version, out)).Tags.Get(tag.Genre)
		return got
	}

	// A single literal name beginning with "(" stays one value, parens intact.
	for _, name := range []string{"(Live)", "(Remastered)", "(2020) Best Of"} {
		for _, version := range []byte{3, 4} {
			if got := roundTrip(t, version, core.ID3MultiNullSep, false, []string{name}); !slices.Equal(got, []string{name}) {
				t.Errorf("v2.%d single %q round-trip = %v, want [%q]", version, name, got, name)
			}
		}
	}

	// Multi-value: a standard name plus a paren-leading literal name across every policy.
	in := []string{"Rock", "(Live)"}
	for _, c := range []struct {
		name    string
		version byte
		pol     core.ID3MultiValuePolicy
		want    []string
	}{
		{"v23-repeat", 3, core.ID3MultiRepeatFrame, []string{"Rock", "(Live)"}},
		{"v23-nullsep", 3, core.ID3MultiNullSep, []string{"Rock", "(Live)"}},
		{"v24-nullsep", 4, core.ID3MultiNullSep, []string{"Rock", "(Live)"}},
		// Slash is a lossy multi-to-single join. The second value is mid-string, so it is
		// not escaped and reads back as one joined value.
		{"v23-slash", 3, core.ID3MultiSlash, []string{"Rock / (Live)"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := roundTrip(t, c.version, c.pol, false, in); !slices.Equal(got, c.want) {
				t.Errorf("round-trip = %v, want %v", got, c.want)
			}
		})
	}

	// NumericGenre on: the standard name folds to a numeric reference (never escaped),
	// the paren-leading literal name is still escaped and round-trips.
	if got := roundTrip(t, 4, core.ID3MultiNullSep, true, in); !slices.Equal(got, []string{"Rock", "(Live)"}) {
		t.Errorf("numeric-genre round-trip = %v, want [Rock (Live)]", got)
	}

	// NumericGenre on under a Slash join: numeric conversion is skipped (a mid-string
	// "(17)" would re-read as a reference plus a slash-prefixed refinement), so the value
	// stays the lossy "Rock / (Live)".
	if got := roundTrip(t, 3, core.ID3MultiSlash, true, in); !slices.Equal(got, []string{"Rock / (Live)"}) {
		t.Errorf("numeric-genre slash round-trip = %v, want [Rock / (Live)]", got)
	}

	// A bare numeric value is always a reference, so GENRE="17" reads back as the genre
	// name even with NumericGenre off.
	if got := roundTrip(t, 4, core.ID3MultiNullSep, false, []string{"17"}); !slices.Equal(got, []string{"Rock"}) {
		t.Errorf("bare-number genre 17 = %v, want [Rock]", got)
	}
}

func TestTXXXLongTailRoundTrip(t *testing.T) {
	// A Picard-style TXXX description folds onto the canonical MusicBrainz key and
	// writes back under its preferred spelling.
	frames := []Frame{{ID: "TXXX", Body: encodeUserText(4, "MusicBrainz Album Id", []string{"abc-123"})}}
	ts := Project(buildTag(t, 4, frames)).Tags
	if v, _ := ts.First(tag.MBReleaseID); v != "abc-123" {
		t.Fatalf("MBReleaseID = %q", v)
	}
	base := tag.NewTagSet()
	out, _ := RebuildFrames(nil, base, ts, 4, StructuredEdit{}, WriteOpts{})
	if len(out) != 1 || out[0].ID != "TXXX" {
		t.Fatalf("expected one TXXX, got %+v", out)
	}
	desc, vals, _ := decodeUserText(out[0].Body)
	if desc != "MusicBrainz Album Id" || !slices.Equal(vals, []string{"abc-123"}) {
		t.Errorf("rewritten TXXX = desc %q vals %v", desc, vals)
	}
}

// TestLyricistTXXXUpgradesToTEXT covers the legacy upgrade: a TXXX:LYRICIST user frame
// reads onto canonical LYRICIST, and an edit drops the stale TXXX and re-renders the
// value as one conformant TEXT frame.
func TestLyricistTXXXUpgradesToTEXT(t *testing.T) {
	orig := []Frame{{ID: "TXXX", Body: encodeUserText(4, "LYRICIST", []string{"Old"})}}

	base := Project(buildTag(t, 4, orig)).Tags
	if v, _ := base.First(tag.Lyricist); v != "Old" {
		t.Fatalf("legacy TXXX:LYRICIST projected %q, want Old", v)
	}

	edited := base.Clone()
	edited.Set(tag.Lyricist, "New")

	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})

	var textFrames, txxxLyricist int
	for _, f := range out {
		switch f.ID {
		case "TEXT":
			textFrames++
			if vals := decodeTextFrame(f.Body); !slices.Equal(vals, []string{"New"}) {
				t.Errorf("TEXT frame values = %v, want [New]", vals)
			}
		case "TXXX":
			if desc, _, ok := decodeUserText(f.Body); ok && strings.EqualFold(strings.TrimSpace(desc), "LYRICIST") {
				txxxLyricist++
			}
		}
	}
	if textFrames != 1 {
		t.Errorf("TEXT frame count = %d, want exactly 1 (%+v)", textFrames, out)
	}
	if txxxLyricist != 0 {
		t.Errorf("TXXX:LYRICIST frame count = %d, want 0 (stale user frame not dropped)", txxxLyricist)
	}
}

// TestTXXXCustomDescriptionCasePreserved checks editing a custom TXXX value keeps the
// original description casing rather than the uppercased canonical key. Aliased keys
// still write their preferred Picard spelling.
func TestTXXXCustomDescriptionCasePreserved(t *testing.T) {
	orig := []Frame{{ID: "TXXX", Body: encodeUserText(4, "MyMoodTag", []string{"happy"})}}
	base := Project(buildTag(t, 4, orig)).Tags
	key := tag.Key("MYMOODTAG")
	if v, _ := base.First(key); v != "happy" {
		t.Fatalf("custom TXXX projected %q, want happy", v)
	}

	edited := base.Clone()
	edited.Set(key, "sad") // edit only the value

	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})
	var txxx *Frame
	for i := range out {
		if out[i].ID == "TXXX" {
			txxx = &out[i]
		}
	}
	if txxx == nil {
		t.Fatalf("no TXXX frame in rebuild output (%d frames)", len(out))
	}
	desc, vals, _ := decodeUserText(txxx.Body)
	if desc != "MyMoodTag" {
		t.Errorf("description = %q, want MyMoodTag (original casing preserved on a value edit)", desc)
	}
	if !slices.Equal(vals, []string{"sad"}) {
		t.Errorf("values = %v, want [sad]", vals)
	}
}

// TestPictureRoundTrip checks an APIC survives encode and decode with its role,
// description and bytes intact. The payload is a real PNG: decodeAPIC sniffs
// authoritatively, so a declared type only survives when the bytes back it.
func TestPictureRoundTrip(t *testing.T) {
	pic := core.Picture{Type: core.PicFrontCover, MIME: "image/png", Description: "front", Data: testfile.PNG()}
	body := encodeAPIC(pic, 4)
	got, ok := decodeAPIC(body)
	if !ok {
		t.Fatal("decodeAPIC failed")
	}
	if got.Type != core.PicFrontCover || got.MIME != "image/png" || got.Description != "front" ||
		!bytes.Equal(got.Data, pic.Data) {
		t.Errorf("picture round-trip = %+v", got)
	}

	// The same frame over bytes no decoder can read: the declared label does not survive,
	// since it describes nothing.
	junk := core.Picture{Type: core.PicFrontCover, MIME: "image/png", Description: "front", Data: []byte("\x89PNG-data")}
	got, ok = decodeAPIC(encodeAPIC(junk, 4))
	if !ok {
		t.Fatal("decodeAPIC failed on the junk payload")
	}
	if got.MIME != core.UnrecognizedMIME || !bytes.Equal(got.Data, junk.Data) {
		t.Errorf("junk picture = %+v, want %s with the bytes preserved", got, core.UnrecognizedMIME)
	}
}

// TestRebuildBreadthV24 checks a wide spread of render units (simple text, AlbumArtist,
// Disc number/total, Comment, Lyrics, the MusicBrainz UFID + TXXX long tail, v2.4 dates,
// and a raw pass-through frame) survive a rebuild -> render -> parse -> project cycle.
func TestRebuildBreadthV24(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Set(tag.AlbumArtist, "AA")
	edited.Set(tag.Composer, "Comp")
	edited.Set(tag.DiscNumber, "1")
	edited.Set(tag.DiscTotal, "2")
	edited.Set(tag.Comment, "a note")
	edited.Set(tag.Lyrics, "la la")
	edited.Set(tag.MBRecordingID, "rec-id")
	edited.Set(tag.MBReleaseID, "rel-id")
	edited.Set(tag.Barcode, "0123456789")
	edited.Set(tag.ReleaseDate, "2020-01-02")
	edited.Set(tag.OriginalDate, "1999")
	edited.Set(tag.BPM, "128")
	edited.Set(tag.Key("TKEY"), "Am")

	out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{}, WriteOpts{})
	got := Project(buildTag(t, 4, out)).Tags

	for _, k := range edited.Keys() {
		want, _ := edited.Get(k)
		gotv, ok := got.Get(k)
		if !ok || !slices.Equal(want, gotv) {
			t.Errorf("key %s round-trip = %v (ok=%v), want %v", k, gotv, ok, want)
		}
	}
}

// A custom key spelled like a frame ID this package reads as something else (a mapped text frame,
// a date, number-pair, genre, involved-people or user frame) is stored as a TXXX frame described by
// that ID, so it reads back unchanged, whether or not the tag holds the canonical key's own frame,
// which the edit leaves alone. TCMP is the exception: TXXX:TCMP is how ffmpeg stores COMPILATION,
// so it reads back as that.
func TestCustomFrameIDKeysGoToTXXX(t *testing.T) {
	canonical := map[string][2]string{ // the key the frame stores and a value for it
		"TIT1": {"GROUPING", "g"}, "TIT2": {"TITLE", "t"}, "TPE1": {"ARTIST", "a"}, "TPE2": {"ALBUMARTIST", "aa"},
		"TPE3": {"CONDUCTOR", "c"}, "TPE4": {"REMIXER", "r"}, "TALB": {"ALBUM", "al"}, "TCOM": {"COMPOSER", "co"},
		"TEXT": {"LYRICIST", "ly"}, "TCOP": {"COPYRIGHT", "cp"}, "TPUB": {"LABEL", "lb"}, "TMED": {"MEDIA", "m"},
		"TSST": {"DISCSUBTITLE", "ds"}, "TSRC": {"ISRC", "USRC17607839"}, "TENC": {"ENCODEDBY", "e"},
		"TSSE": {"ENCODER", "en"}, "TSOT": {"TITLESORT", "ts"}, "TSOP": {"ARTISTSORT", "as"}, "TSOA": {"ALBUMSORT", "als"},
		"TSO2": {"ALBUMARTISTSORT", "aas"}, "TSOC": {"COMPOSERSORT", "cs"}, "TCMP": {"COMPILATION", "1"},
		"TBPM": {"BPM", "120"}, "TCON": {"GENRE", "Rock"}, "TRCK": {"TRACKNUMBER", "3"}, "TPOS": {"DISCNUMBER", "1"},
		"TYER": {"RECORDINGDATE", "2001-02-03T10:20"}, "TDAT": {"RECORDINGDATE", "2001-02-03T10:20"},
		"TIME": {"RECORDINGDATE", "2001-02-03T10:20"}, "TDRC": {"RECORDINGDATE", "2001-02-03T10:20"},
		"TORY": {"ORIGINALDATE", "1999"}, "TDOR": {"ORIGINALDATE", "1999"}, "TDRL": {"RELEASEDATE", "2005"},
		"TIPL": {"PRODUCER", "p"}, "TXXX": {"MYKEY", "mine"},
	}
	for id, canon := range canonical {
		for _, version := range []byte{3, 4} {
			for _, holding := range []bool{false, true} {
				var orig []Frame
				if holding {
					seed := tag.NewTagSet()
					seed.Set(tag.Key(canon[0]), canon[1])
					orig, _ = RebuildFrames(nil, tag.NewTagSet(), seed, version, StructuredEdit{}, WriteOpts{})
				}
				base := Project(buildTag(t, version, orig)).Tags
				edited := base.Clone()
				edited.Set(tag.Key(id), "custom")
				out, _ := RebuildFrames(orig, base, edited, version, StructuredEdit{}, WriteOpts{})
				name := fmt.Sprintf("%s v2.%d holding=%v", id, version, holding)
				var txxx []string
				for _, f := range out {
					if desc, vals, ok := decodeUserText(f.Body); f.ID == "TXXX" && ok && desc == id {
						txxx = append(txxx, vals...)
					}
				}
				if !slices.Equal(txxx, []string{"custom"}) {
					t.Errorf("%s: TXXX:%s values %v, want one frame holding [custom]", name, id, txxx)
				}
				for _, f := range orig {
					if !slices.ContainsFunc(out, func(o Frame) bool { return o.ID == f.ID && bytes.Equal(o.Body, f.Body) }) {
						t.Errorf("%s: the edit changed or dropped the %s frame", name, f.ID)
					}
				}
				// TXXX:TCMP is how ffmpeg stores a compilation flag, so it reads back as that.
				want := tag.Key(id)
				if id == "TCMP" {
					want = tag.Compilation
				}
				if v, _ := Project(buildTag(t, version, out)).Tags.Get(want); !slices.Contains(v, "custom") {
					t.Errorf("%s: %s reads back %v, want it to hold custom", name, want, v)
				}
			}
		}
	}
}

// A custom key spelled like a T frame this package does not read as anything else is stored in a
// frame of its own, which reads back as that key; any other custom key goes to TXXX.
func TestCustomFrameIDOwnFrame(t *testing.T) {
	for key, wantFrame := range map[tag.Key]string{
		"TFOO": "TFOO", "TMCL": "TMCL", "MYKEY": "TXXX", "TIT": "TXXX", "COMM": "TXXX", tag.ISRC: "TSRC",
	} {
		edited := tag.NewTagSet()
		edited.Set(key, "x")
		out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{}, WriteOpts{})
		if len(out) != 1 || out[0].ID != wantFrame {
			t.Errorf("%s: frames %+v, want one %s frame", key, out, wantFrame)
			continue
		}
		if v, _ := Project(buildTag(t, 4, out)).Tags.Get(key); !slices.Equal(v, []string{"x"}) {
			t.Errorf("%s reads back %v, want [x]", key, v)
		}
	}
}

// TestRebuildV23Fallbacks covers the v2.3-specific routes: ReleaseDate via TXXX,
// OriginalDate via TORY, and the disc compound.
func TestRebuildV23Fallbacks(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Set(tag.ReleaseDate, "2018")
	edited.Set(tag.OriginalDate, "1995")
	out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 3, StructuredEdit{}, WriteOpts{})

	ids := map[string]bool{}
	for _, f := range out {
		ids[f.ID] = true
	}
	if !ids["TORY"] {
		t.Errorf("v2.3 OriginalDate should write TORY; frames = %v", ids)
	}
	if !ids["TXXX"] {
		t.Errorf("v2.3 ReleaseDate should fall back to TXXX; frames = %v", ids)
	}
	got := Project(buildTag(t, 3, out)).Tags
	if v, _ := got.First(tag.OriginalDate); v != "1995" {
		t.Errorf("OriginalDate = %q", v)
	}
	if v, _ := got.First(tag.ReleaseDate); v != "2018" {
		t.Errorf("ReleaseDate = %q", v)
	}
}

func TestRebuildMultiValuePolicies(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Set(tag.Artist, "A", "B")

	// Repeat-frame: two TPE1 frames, no v2.3-extension flag.
	out, info := RebuildFrames(nil, tag.NewTagSet(), edited, 3, StructuredEdit{},
		WriteOpts{Multi: core.ID3MultiRepeatFrame})
	count := 0
	for _, f := range out {
		if f.ID == "TPE1" {
			count++
		}
	}
	if count != 2 || info.UsedV23Multi {
		t.Errorf("repeat-frame: %d TPE1 frames, v23multi=%v, want 2 frames, false", count, info.UsedV23Multi)
	}

	// Slash-join: one frame, single joined value.
	out2, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 3, StructuredEdit{},
		WriteOpts{Multi: core.ID3MultiSlash})
	if len(out2) != 1 || decodeTextFrame(out2[0].Body)[0] != "A / B" {
		t.Errorf("slash-join = %+v", out2)
	}

	// Null-sep on v2.3 flags the compatibility impact.
	_, info3 := RebuildFrames(nil, tag.NewTagSet(), edited, 3, StructuredEdit{},
		WriteOpts{Multi: core.ID3MultiNullSep})
	if !info3.UsedV23Multi {
		t.Error("null-sep on v2.3 should report UsedV23Multi")
	}
}

// Custom TXXX fields use the same v2.3 multi-value policy as named text frames.
// MOOD has no dedicated frame, so it routes through TXXX here.
func TestRebuildTXXXMultiValuePolicies(t *testing.T) {
	mood := tag.Key("MOOD")
	edited := tag.NewTagSet()
	edited.Set(mood, "Happy", "Energetic")

	txxxValues := func(frames []Frame) [][]string {
		var out [][]string
		for _, f := range frames {
			if f.ID != "TXXX" {
				continue
			}
			if _, vals, ok := decodeUserText(f.Body); ok {
				out = append(out, vals)
			}
		}
		return out
	}

	// NUL-separated v2.3 extension: one TXXX frame and a compatibility warning.
	out, info := RebuildFrames(nil, tag.NewTagSet(), edited, 3, StructuredEdit{},
		WriteOpts{Multi: core.ID3MultiNullSep})
	if got := txxxValues(out); len(got) != 1 || !slices.Equal(got[0], []string{"Happy", "Energetic"}) {
		t.Errorf("v2.3 null-sep TXXX = %v, want one frame [Happy Energetic]", got)
	}
	if !info.UsedV23Multi {
		t.Error("v2.3 null-sep multi-value TXXX should report UsedV23Multi")
	}

	// Repeat-frame policy: two TXXX frames, no v2.3-extension flag. ID3 discourages
	// repeated same-description frames, but Repeat is an explicit caller opt-in.
	out, info = RebuildFrames(nil, tag.NewTagSet(), edited, 3, StructuredEdit{},
		WriteOpts{Multi: core.ID3MultiRepeatFrame})
	if got := txxxValues(out); len(got) != 2 || info.UsedV23Multi {
		t.Errorf("v2.3 repeat TXXX = %v, v23multi=%v, want 2 frames, false", got, info.UsedV23Multi)
	}

	// Slash-join: one frame carrying the " / "-joined value, no flag.
	out, info = RebuildFrames(nil, tag.NewTagSet(), edited, 3, StructuredEdit{},
		WriteOpts{Multi: core.ID3MultiSlash})
	if got := txxxValues(out); len(got) != 1 || !slices.Equal(got[0], []string{"Happy / Energetic"}) {
		t.Errorf("v2.3 slash TXXX = %v, want one frame [Happy / Energetic]", got)
	}
	if info.UsedV23Multi {
		t.Error("v2.3 slash-join must not report UsedV23Multi")
	}

	// v2.4 always NUL-separates cleanly, no v2.3-extension flag.
	out, info = RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{},
		WriteOpts{Multi: core.ID3MultiNullSep})
	if got := txxxValues(out); len(got) != 1 || !slices.Equal(got[0], []string{"Happy", "Energetic"}) {
		t.Errorf("v2.4 TXXX = %v, want one frame [Happy Energetic]", got)
	}
	if info.UsedV23Multi {
		t.Error("v2.4 must not report UsedV23Multi")
	}
}

// COMM is normally single-valued, but the shared policy helper still handles a
// multi-value TagSet. A v2.3 multi-value comment raises the compatibility flag,
// and a single-value comment stays one frame.
func TestRebuildCOMMMultiValuePolicy(t *testing.T) {
	multi := tag.NewTagSet()
	multi.Set(tag.Comment, "first", "second")
	if _, info := RebuildFrames(nil, tag.NewTagSet(), multi, 3, StructuredEdit{},
		WriteOpts{Multi: core.ID3MultiNullSep}); !info.UsedV23Multi {
		t.Error("v2.3 null-sep multi-value COMM should report UsedV23Multi")
	}

	single := tag.NewTagSet()
	single.Set(tag.Comment, "only")
	out, info := RebuildFrames(nil, tag.NewTagSet(), single, 3, StructuredEdit{}, WriteOpts{})
	count := 0
	for _, f := range out {
		if f.ID == "COMM" {
			count++
		}
	}
	if count != 1 || info.UsedV23Multi {
		t.Errorf("single COMM = %d frames, v23multi=%v, want 1 frame, false", count, info.UsedV23Multi)
	}
}

// TestFrameRenderIDUnmanaged confirms unmodelled frames are unmanaged (preserved
// verbatim, never re-rendered).
func TestFrameRenderIDUnmanaged(t *testing.T) {
	unmanaged := []Frame{
		{ID: "WXXX", Body: []byte{0}},                          // URL frame
		{ID: "PRIV", Body: []byte{1, 2}},                       // binary
		{ID: "APIC", Body: []byte{0}},                          // picture (handled separately)
		{ID: "TIT2", Body: []byte{0}, Opaque: true},            // opaque
		{ID: "UFID", Body: append([]byte("other.example"), 0)}, // non-MusicBrainz UFID
		// A machine-described comment is iTunes state, not something a person wrote;
		// projecting one would let an unrelated COMMENT edit consume it.
		{ID: "COMM", Body: encodeComment(4, "eng", "iTunNORM", []string{"x"})},
		{ID: "COMM", Body: encodeComment(4, "eng", "replaygain_track_gain", []string{"x"})},
	}
	for _, f := range unmanaged {
		if _, managed := frameRenderID(f); managed {
			t.Errorf("frame %q (opaque=%v) should be unmanaged", f.ID, f.Opaque)
		}
	}
	// A MusicBrainz UFID is managed, and so is a COMM with an ordinary human description:
	// managed exactly when projected, or an edit would append a second frame beside the
	// one it preserved.
	managed := []Frame{
		{ID: "UFID", Body: encodeUFID(musicBrainzOwner, "id")},
		{ID: "COMM", Body: encodeComment(4, "eng", "", []string{"x"})},
		{ID: "COMM", Body: encodeComment(4, "eng", "desc", []string{"x"})},
	}
	for _, f := range managed {
		if _, ok := frameRenderID(f); !ok {
			t.Errorf("frame %q should be managed", f.ID)
		}
	}
}

// TestRebuildDropsStaleAlias confirms editing a canonical value stored under a
// non-canonical representation drops the stale frame and writes only the write-version
// target, so the value is neither duplicated nor lost.
func TestRebuildDropsStaleAlias(t *testing.T) {
	// v2.4: ReleaseDate held in a TXXX frame; editing it must drop the TXXX and
	// write a single TDRL.
	orig := []Frame{{ID: "TXXX", Body: encodeUserText(4, "RELEASEDATE", []string{"2001"})}}
	base := Project(&Tag{frames: orig}).Tags
	edited := base.Clone()
	edited.Set(tag.ReleaseDate, "2022")
	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})
	if got, _ := Project(buildTag(t, 4, out)).Tags.Get(tag.ReleaseDate); !slices.Equal(got, []string{"2022"}) {
		t.Errorf("v2.4 ReleaseDate after edit = %v, want [2022] (no duplicate/stale)", got)
	}

	// v2.3: a stray TDRC must not shadow a RecordingDate edit (dateParts.emit
	// would otherwise prefer the stale TDRC and lose the new TYER value).
	orig2 := []Frame{{ID: "TDRC", Body: encodeTextFrame(encLatin1, []string{"2000-01-01"})}}
	base2 := Project(&Tag{frames: orig2}).Tags
	edited2 := base2.Clone()
	edited2.Set(tag.RecordingDate, "2022")
	out2, _ := RebuildFrames(orig2, base2, edited2, 3, StructuredEdit{}, WriteOpts{})
	if got, _ := Project(buildTag(t, 3, out2)).Tags.Get(tag.RecordingDate); !slices.Equal(got, []string{"2022"}) {
		t.Errorf("v2.3 RecordingDate after edit = %v, want [2022] (edit not lost)", got)
	}
}

// TestRebuildDeterministicNewFrames confirms adding several fields produces
// byte-identical output across runs: the leftover render-ids are sorted, not emitted in
// map order.
func TestRebuildDeterministicNewFrames(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Set(tag.Title, "T")
	edited.Set(tag.Artist, "A")
	edited.Set(tag.Album, "Al")
	edited.Set(tag.Genre, "Rock")
	edited.Set(tag.Barcode, "X")
	edited.Set(tag.ISRC, "Y")

	var first []byte
	for i := range 25 {
		out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{}, WriteOpts{})
		data := Render(4, out, 0)
		if i == 0 {
			first = data
		} else if !bytes.Equal(first, data) {
			t.Fatalf("nondeterministic rebuild output on run %d", i)
		}
	}
}

// TestSpacePaddedFrameID confirms a non-conformant space-padded frame ID does not end the
// scan: the padded frame is preserved and a following valid frame is still parsed.
func TestSpacePaddedFrameID(t *testing.T) {
	// A space-padded "XXX " frame (4 bytes, 1-byte body), then a real TIT2.
	pad := append([]byte("XXX "), 0, 0, 0, 1, 0, 0, 0x42)
	tit2 := append([]byte("TIT2"), 0, 0, 0, 6, 0, 0)
	tit2 = append(tit2, encodeTextFrame(encLatin1, []string{"After"})...)
	var sz [4]byte
	putSyncSafe(sz[:], int64(len(pad)+len(tit2)))
	data := append([]byte{'I', 'D', '3', 3, 0, 0}, sz[:]...)
	data = append(data, pad...)
	data = append(data, tit2...)

	tg, err := ParseTag(data, 0)
	if err != nil {
		t.Fatalf("ParseTag: %v", err)
	}
	if len(tg.Frames()) != 2 {
		t.Fatalf("expected 2 frames (padded + TIT2), got %d: %+v", len(tg.Frames()), tg.Frames())
	}
	if v, _ := Project(tg).Tags.First(tag.Title); v != "After" {
		t.Errorf("Title after a space-padded frame = %q, want After", v)
	}
}

// A frame whose ID is printable but not A-Z and 0-9 is read by its size and kept verbatim, so the
// frames after it still read, and the warning names it. The frame renders back byte for byte.
func TestPrintableNonStandardFrameIDReadBySize(t *testing.T) {
	low := testfile.TextFrame(3, "Tlow", "odd")
	data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Title"), low,
		testfile.TextFrame(3, "TPE1", "Artist"), testfile.TextFrame(3, "TALB", "Album"))
	tg, err := ParseTag(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	frames := tg.Frames()
	if len(frames) != 4 || frames[1].ID != "Tlow" || !frames[1].Opaque {
		t.Fatalf("frames = %+v, want four with Tlow second and opaque", frames)
	}
	if id, n := tg.MalformedTail(); id != "" || n != 0 || tg.Padding() != 0 {
		t.Errorf("malformed tail (%q, %d), padding %d; want neither", id, n, tg.Padding())
	}
	proj := Project(tg)
	for k, want := range map[tag.Key]string{tag.Title: "Title", tag.Artist: "Artist", tag.Album: "Album"} {
		if v, _ := proj.Tags.First(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	if len(proj.Tags.Keys()) != 3 {
		t.Errorf("projected keys %v, want the three standard frames only", proj.Tags.Keys())
	}
	ws := core.WarningsWithCode(proj.Warnings, core.WarnMalformedTagEntry)
	if len(ws) != 1 || !strings.Contains(ws[0].Message, "Tlow") {
		t.Errorf("warnings %v, want one malformed-tag-entry naming Tlow", proj.Warnings)
	}
	if !bytes.Contains(Render(3, frames, 0), low) {
		t.Error("the Tlow frame does not render back byte for byte")
	}
}

// A frame ID of spaces is printable, so it is read by its size, and the warnings quote it: bare, it
// would read as a gap in the sentence.
func TestSpaceFrameIDIsQuoted(t *testing.T) {
	for _, c := range []struct {
		name  string
		frame []byte
	}{
		{"read by its size", testfile.TextFrame(3, "    ", "odd")},
		{"running past the tag", testfile.OverrunFrame("    ")},
	} {
		tg, err := ParseTag(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Title"), c.frame), 0)
		if err != nil {
			t.Fatal(err)
		}
		ws := core.WarningsWithCode(Project(tg).Warnings, core.WarnMalformedTagEntry)
		if len(ws) != 1 || !strings.Contains(ws[0].Message, `"    "`) {
			t.Errorf("%s: warnings %v, want the ID quoted", c.name, ws)
		}
	}
}

// A frame ID holding a control or high byte ends the frame walk: what follows is an unreadable
// tail, not padding, and a rewrite says it is not carried.
func TestControlByteFrameIDEndsTheWalk(t *testing.T) {
	for _, id := range []string{"T\x01LW", "TI\x00\x00", "T\xe9LW"} {
		bad := testfile.TextFrame(3, id, "junk")
		after := testfile.TextFrame(3, "TPE1", "Lost")
		tg, err := ParseTag(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Title"), bad, after), 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(tg.Frames()) != 1 || tg.Padding() != 0 {
			t.Errorf("%q: %d frames, padding %d; want the title alone and no padding", id, len(tg.Frames()), tg.Padding())
		}
		if got, n := tg.MalformedTail(); got == "" || n != int64(len(bad)+len(after)) {
			t.Errorf("%q: malformed tail (%q, %d), want the %d bytes from the bad header", id, got, n, len(bad)+len(after))
		}
		if ws := core.WarningsWithCode(Project(tg).Warnings, core.WarnMalformedTagEntry); len(ws) != 1 {
			t.Errorf("%q: warnings %v, want one malformed-tag-entry", id, Project(tg).Warnings)
		}
		if ws := AppendMalformedTailDropped(nil, tg, true); len(ws) != 1 || ws[0].Code != core.WarnMalformedTagEntryDropped {
			t.Errorf("%q: rewrite warnings %v, want malformed-tag-entry-dropped", id, ws)
		}
	}
}

// plainV24Frame is an ID3v2.4 frame whose size is a plain 32-bit integer, as iTunes writes them.
func plainV24Frame(id string, body []byte) []byte {
	return slices.Concat([]byte(id), testfile.BE32(len(body)), []byte{0, 0}, body)
}

// apicOfLen is an APIC body of exactly n bytes: a PNG front cover whose description fills it out.
func apicOfLen(n int) []byte {
	head := slices.Concat([]byte{0}, []byte("image/png\x00"), []byte{3})
	img := testfile.PNG()
	desc := bytes.Repeat([]byte("d"), n-len(head)-1-len(img))
	return slices.Concat(head, desc, []byte{0}, img)
}

// An ID3v2.4 tag whose frame sizes are plain 32-bit integers, as iTunes writes them, reads every
// frame and its padding, and says so; a conformant tag with a frame over 127 bytes still reads its
// sizes sync-safe. Frames under 128 bytes read the same either way, so the one larger frame decides.
func TestV24PlainFrameSizes(t *testing.T) {
	apic := apicOfLen(201)
	title := testfile.TextFrame(4, "TIT2", "Title")
	artist := testfile.TextFrame(4, "TPE1", "Artist")
	pad := make([]byte, 100)
	for _, c := range []struct {
		name  string
		frame []byte
		plain bool
	}{
		{"plain", plainV24Frame("APIC", apic), true},
		{"sync-safe", testfile.ID3Frame(4, "APIC", apic), false},
	} {
		tg, err := ParseTag(testfile.ID3v2(4, title, c.frame, artist, pad), 0)
		if err != nil {
			t.Fatal(err)
		}
		frames := tg.Frames()
		if len(frames) != 3 || frames[1].ID != "APIC" || !bytes.Equal(frames[1].Body, apic) || frames[2].ID != "TPE1" {
			t.Errorf("%s: frames %+v, want TIT2, the 201-byte APIC, TPE1", c.name, frames)
		}
		if id, n := tg.MalformedTail(); id != "" || n != 0 || tg.Padding() != 100 {
			t.Errorf("%s: malformed tail (%q, %d), padding %d; want 100 bytes of padding", c.name, id, n, tg.Padding())
		}
		if got := strings.Contains(FramesNote(tg), "non-sync-safe frame sizes"); got != c.plain {
			t.Errorf("%s: note %q, plain sizes noted %v, want %v", c.name, FramesNote(tg), got, c.plain)
		}
	}
}

// A v2.4 tag whose sizes all read alike is sync-safe whatever its walk meets: here its last frame
// runs a byte past the tag, which both readings see.
func TestV24FrameSizesReadingAlikeAreSyncSafe(t *testing.T) {
	last := testfile.TextFrame(4, "TPE1", "Artist")
	data := testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Title"), last[:len(last)-1])
	tg, err := ParseTag(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if note := FramesNote(tg); strings.Contains(note, "non-sync-safe") {
		t.Errorf("note %q; want no plain-size note where no size reads differently", note)
	}
}

// A plain-size v2.4 tag holding one frame whose size is sync-safe, as no writer means to but a tag
// edited by two can, reads that frame by the size that lands on the frame after it.
func TestV24PlainTagWithASyncSafeFrame(t *testing.T) {
	apic := apicOfLen(200)
	txxx := slices.Concat([]byte{0}, []byte("DESC\x00"), bytes.Repeat([]byte("x"), 194))
	tg, err := ParseTag(testfile.ID3v2(4, plainV24Frame("APIC", apic), testfile.ID3Frame(4, "TXXX", txxx),
		testfile.TextFrame(4, "TPE1", "Artist"), make([]byte, 200)), 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, f := range tg.Frames() {
		ids = append(ids, f.ID)
	}
	if !slices.Equal(ids, []string{"APIC", "TXXX", "TPE1"}) || !bytes.Equal(tg.Frames()[1].Body, txxx) || tg.Padding() != 200 {
		t.Errorf("frames %v, padding %d; want APIC, the 200-byte TXXX, TPE1 and 200 bytes of padding", ids, tg.Padding())
	}
	if v, _ := Project(tg).Tags.First(tag.Artist); v != "Artist" {
		t.Errorf("ARTIST = %q, want Artist", v)
	}
}

// A genuine plain-size frame ending where the padding begins reads whole even when its own body
// holds a known frame ID at the offset its size reads as sync-safe: that ID's frame would not fit.
func TestV24PlainFrameBeforePaddingReadsWhole(t *testing.T) {
	lyrics := []byte(strings.Repeat("la ", 65))
	copy(lyrics[67:], "TIME") // body offset 72: where 200 read sync-safe (0x48) lands
	uslt := slices.Concat([]byte{0}, []byte("eng\x00"), lyrics)
	tg, err := ParseTag(testfile.ID3v2(4, plainV24Frame("APIC", apicOfLen(418)), testfile.TextFrame(4, "TIT2", "Title"),
		plainV24Frame("USLT", uslt), make([]byte, 300)), 0)
	if err != nil {
		t.Fatal(err)
	}
	frames := tg.Frames()
	if len(frames) != 3 || !bytes.Equal(frames[2].Body, uslt) || tg.Padding() != 300 {
		t.Errorf("%d frames, padding %d; want the APIC, TIT2 and the whole 200-byte USLT before 300 bytes of padding", len(frames), tg.Padding())
	}
}

// A v2.4 tag with sync-safe sizes whose padding holds junk, or the stale bytes an in-place shrink leaves, keeps
// its sync-safe sizes: read plain, a large frame would jump past the frames after it, or over the
// stale bytes, into the zeros beyond, a walk that also ends cleanly but swallows them.
func TestV24SyncSafeTagsWithStalePaddingStaySyncSafe(t *testing.T) {
	text := func(id, v string) []byte { return testfile.TextFrame(4, id, v) }
	apic := slices.Concat([]byte{0}, []byte("image/jpeg\x00"), []byte{3, 0}, bytes.Repeat([]byte("A"), 285))
	uslt := slices.Concat([]byte{0}, []byte("eng\x00"), bytes.Repeat([]byte("l"), 1494))
	junkPad := make([]byte, 600)
	copy(junkPad[20:], "JUNK")
	stale := append([]byte("TXX"), make([]byte, 3000)...)
	// Read plain, the APIC (sync-safe 299, plain 555) ends exactly on the tag's last byte, inside this junk.
	endJunk := make([]byte, 223)
	copy(endJunk[219:], "JUNK")
	for _, c := range []struct {
		name string
		body [][]byte
		ids  []string
	}{
		{"junk in the padding", [][]byte{text("TIT2", "Title"), testfile.ID3Frame(4, "APIC", apic), text("TPE1", "Artist"),
			text("TALB", "Album"), junkPad}, []string{"TIT2", "APIC", "TPE1", "TALB"}},
		{"junk ending the padding", [][]byte{text("TIT2", "Title"), testfile.ID3Frame(4, "APIC", apic), text("TPE1", "Artist"),
			text("TALB", "Album"), endJunk}, []string{"TIT2", "APIC", "TPE1", "TALB"}},
		{"stale bytes after the frames", [][]byte{text("TIT2", "Title"), testfile.ID3Frame(4, "USLT", uslt), text("TPE1", "Artist"),
			text("TALB", "Album"), stale}, []string{"TIT2", "USLT", "TPE1", "TALB"}},
		{"stale bytes after a large last frame", [][]byte{text("TIT2", "Title"), text("TPE1", "Artist"), text("TALB", "Album"),
			testfile.ID3Frame(4, "USLT", uslt), stale}, []string{"TIT2", "TPE1", "TALB", "USLT"}},
	} {
		tg, err := ParseTag(testfile.ID3v2(4, c.body...), 0)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, f := range tg.Frames() {
			ids = append(ids, f.ID)
			if f.ID == "USLT" && !bytes.Equal(f.Body, uslt) || f.ID == "APIC" && !bytes.Equal(f.Body, apic) {
				t.Errorf("%s: the %s frame reads %d bytes, want its own", c.name, f.ID, len(f.Body))
			}
		}
		if !slices.Equal(ids, c.ids) || strings.Contains(FramesNote(tg), "non-sync-safe") {
			t.Errorf("%s: frames %v, note %q; want %v read sync-safe", c.name, ids, FramesNote(tg), c.ids)
		}
	}
}

// Where the two dry walks tie, both meeting the same frames and neither running past the tag, the
// tag reads sync-safe, as mutagen, ffmpeg and TagLib read it. When that reading is wrong it stops at
// a zero run inside a plain-size frame, and the frame's rest is non-zero: it is reported unreadable,
// never taken for padding. Where it is right, junk or stale frames after the padding are reported
// the same way, and the live frames read, not stale ones a plain reading would land on.
func TestV24TiedVoteReadsSyncSafeAndReportsTheRest(t *testing.T) {
	apicHead := slices.Concat([]byte{0}, []byte("image/png\x00"), []byte{3, 0})
	cut := slices.Concat(apicHead, bytes.Repeat([]byte{0xAB}, 73-len(apicHead)), make([]byte, 10), bytes.Repeat([]byte{0xCD}, 201-83))
	geob := slices.Concat([]byte("\x00application/octet-stream\x00\x00\x00"), bytes.Repeat([]byte{0x11}, 172-30),
		make([]byte, 10), bytes.Repeat([]byte{0x22}, 300-182-10), make([]byte, 10))
	apic := slices.Concat([]byte{0}, []byte("image/jpeg\x00"), []byte{3, 0}, bytes.Repeat([]byte("A"), 285)) // sync-safe 299, plain 555
	title := testfile.TextFrame(4, "TIT2", "Title")
	junkEnd := make([]byte, 256)
	copy(junkEnd[252:], "JUNK")
	// Read plain, the APIC's 555 bytes land on these stale frames, after 223 zeros.
	stale := slices.Concat(make([]byte, 223), testfile.TextFrame(4, "TPE1", "OldArtist"), testfile.TextFrame(4, "TALB", "OldAlbum"), make([]byte, 40))
	for _, c := range []struct {
		name   string
		frames [][]byte
		ids    []string
		artist string
		tail   bool
	}{
		{"a plain APIC cut at a zero run", [][]byte{title, plainV24Frame("APIC", cut)}, []string{"TIT2", "APIC"}, "", true},
		{"a plain GEOB cut at a zero run", [][]byte{title, plainV24Frame("GEOB", geob)}, []string{"TIT2", "GEOB"}, "", true},
		{"a sync-safe APIC before padding ending in junk", [][]byte{title, testfile.ID3Frame(4, "APIC", apic), junkEnd},
			[]string{"TIT2", "APIC"}, "", true},
		{"stale frames where the plain reading lands", [][]byte{title, testfile.ID3Frame(4, "APIC", apic),
			testfile.TextFrame(4, "TPE1", "Artist"), testfile.TextFrame(4, "TALB", "Album"), stale},
			[]string{"TIT2", "APIC", "TPE1", "TALB"}, "Artist", true},
	} {
		tg, err := ParseTag(testfile.ID3v2(4, c.frames...), 0)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, f := range tg.Frames() {
			ids = append(ids, f.ID)
		}
		id, n := tg.MalformedTail()
		if !slices.Equal(ids, c.ids) || strings.Contains(FramesNote(tg), "non-sync-safe") || (id != "" && n > 0) != c.tail || tg.Padding() != 0 {
			t.Errorf("%s: frames %v, note %q, tail (%q, %d), padding %d; want %v read sync-safe and the rest unreadable",
				c.name, ids, FramesNote(tg), id, n, tg.Padding(), c.ids)
		}
		if v, _ := Project(tg).Tags.First(tag.Artist); v != c.artist {
			t.Errorf("%s: ARTIST = %q, want %q", c.name, v, c.artist)
		}
	}
}

// A frame header cut short by the end of the tag is an unreadable tail, not padding; zeros there
// are still padding.
func TestCutFrameHeaderIsAnUnreadableTail(t *testing.T) {
	for _, c := range []struct {
		name    string
		major   byte
		tail    []byte
		padding int64
	}{
		{"v2.3 cut header", 3, []byte("TPE1\x00\x00"), 0},
		{"v2.2 cut header", 2, []byte("TP"), 0},
		{"short padding holding a byte", 3, []byte("\x00\x00AB"), 0},
		{"zero padding", 3, make([]byte, 6), 6},
	} {
		first := testfile.TextFrame(c.major, "TIT2", "Title")
		if c.major == 2 {
			first = slices.Concat([]byte("TT2"), []byte{0, 0, 6}, []byte("\x00Title"))
		}
		tg, err := ParseTag(testfile.ID3v2(c.major, first, c.tail), 0)
		if err != nil {
			t.Fatal(err)
		}
		id, n := tg.MalformedTail()
		if c.padding > 0 {
			if id != "" || tg.Padding() != c.padding {
				t.Errorf("%s: malformed tail %q, padding %d; want %d bytes of padding", c.name, id, tg.Padding(), c.padding)
			}
			continue
		}
		if id == "" || n != int64(len(c.tail)) || tg.Padding() != 0 {
			t.Errorf("%s: malformed tail (%q, %d), padding %d; want the %d cut bytes unreadable", c.name, id, n, tg.Padding(), len(c.tail))
		}
		if ws := AppendMalformedTailDropped(nil, tg, true); len(ws) != 1 {
			t.Errorf("%s: rewrite warnings %v, want malformed-tag-entry-dropped", c.name, ws)
		}
	}
}

// TestNonConformantFrameIDMarkedOpaque covers the re-read half of unknown-v2.2-frame
// preservation: a space-padded "TXY " ID (an unknown v2.2 frame modernized to v2.3 and
// read back) is marked opaque at decode, which keeps it preserved verbatim, out of the
// canonical model, and out of DecodeText.
func TestNonConformantFrameIDMarkedOpaque(t *testing.T) {
	frame := func(id, text string) []byte {
		return testfile.ID3Frame(3, id, encodeTextFrame(encLatin1, []string{text}))
	}
	txy := frame("TXY ", "leak?") // non-conformant (trailing space) ID
	tit2 := frame("TIT2", "Real")
	data := testfile.ID3v2(3, txy, tit2)

	tg, err := ParseTag(data, 0)
	if err != nil {
		t.Fatalf("ParseTag: %v", err)
	}
	var txyFrame Frame
	found := false
	for _, f := range tg.Frames() {
		if f.ID == "TXY " {
			txyFrame, found = f, true
		}
	}
	if !found {
		t.Fatal("the space-padded TXY frame was dropped rather than preserved")
	}
	if !txyFrame.Opaque {
		t.Error("a non-conformant (space-padded) frame ID must be marked opaque on decode")
	}
	// The opaque frame must not leak its bytes as a text value through DecodeText.
	if got := DecodeText(txyFrame); len(got) != 0 {
		t.Errorf("DecodeText on an opaque non-conformant frame = %v, want empty (no text leak)", got)
	}
	// It must not surface as a phantom canonical tag; the real TIT2 still projects.
	if v, ok := Project(tg).Tags.First(tag.Key("TXY")); ok {
		t.Errorf("phantom canonical TXY = %q surfaced from a non-conformant frame", v)
	}
	if v, _ := Project(tg).Tags.First(tag.Title); v != "Real" {
		t.Errorf("Title = %q, want Real (conformant frames still project)", v)
	}
}

// TestHugeFrameSizeNoPanic covers the 32-bit overflow: a v2.3 frame header declaring size
// 0xFFFFFFFF stops the scan rather than wrapping to a negative length and panicking.
func TestHugeFrameSizeNoPanic(t *testing.T) {
	frame := append([]byte("TIT2"), 0xFF, 0xFF, 0xFF, 0xFF, 0, 0) // size = 4294967295
	var sz [4]byte
	putSyncSafe(sz[:], int64(len(frame)+4)) // claim a bit more than present
	data := append([]byte{'I', 'D', '3', 3, 0, 0}, sz[:]...)
	data = append(data, frame...)

	tg, err := ParseTag(data, 0) // must not panic on any platform
	if err != nil {
		t.Fatalf("ParseTag: %v", err)
	}
	if len(tg.Frames()) != 0 {
		t.Errorf("a frame with an absurd size should be dropped, got %d frames", len(tg.Frames()))
	}
}

// ReadFrontRaw returns the bytes of a leading tag that fits the source, nil when there is none, and
// refuses a header or tag over the allocation limit.
func TestReadFrontRaw(t *testing.T) {
	lead := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Lead"))
	stream := []byte("fLaC\x00\x00\x00\x22")
	past := slices.Clone(lead)
	past[9]++ // the declared size runs one byte past the tag
	for _, c := range []struct {
		name  string
		src   []byte
		limit int64
		want  []byte
		err   error
	}{
		{"tag before a stream", slices.Concat(lead, stream), 1 << 20, lead, nil},
		{"tag filling the source", lead, 1 << 20, lead, nil},
		{"no tag", stream, 1 << 20, nil, nil},
		{"shorter than a header", lead[:6], 1 << 20, nil, nil},
		{"size past the source", past, 1 << 20, nil, nil},
		{"header over the limit", slices.Concat(lead, stream), 5, nil, waxerr.ErrSizeTooLarge},
		{"tag over the limit", slices.Concat(lead, stream), int64(len(lead)) - 1, nil, waxerr.ErrSizeTooLarge},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := core.BytesSource(c.src)
			got, err := ReadFrontRaw(src, src.Size(), c.limit)
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if !bytes.Equal(got, c.want) || (got == nil) != (c.want == nil) {
				t.Errorf("raw = %q, want %q", got, c.want)
			}
		})
	}
}

// ReadFrontAll reads the chain of tags a file opens with. The kept tag is the first that holds frames,
// the rest are duplicates in file order, and a tag after a gap is not one. An empty tag, or one that
// does not read, yields to a tag behind it that holds frames; with none, the first is kept. A
// duplicate that cannot be parsed still is one; one over the limit is refused.
func TestReadFrontAll(t *testing.T) {
	first := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "First"))
	second := testfile.ID3v2(4, testfile.TextFrame(4, "TALB", "Second"), testfile.TextFrame(4, "TIT2", "Again"))
	third := testfile.ID3v2(3, testfile.FrontCover("image/png", testfile.PNG()))
	empty := testfile.ID3v2(3)
	unreadable := testfile.ID3v2(3, testfile.OverrunFrame("TIT2")) // parses, holding only an unreadable tail
	broken := slices.Clone(second)
	broken[3] = 1 // a major version no tag has
	audio := []byte{0xFF, 0xFB, 0x90, 0x00, 1, 2, 3}
	type dup struct {
		off, n  int
		version byte
		holds   bool // parsed, with frames
	}
	n1, n2 := len(first), len(second)
	for _, c := range []struct {
		name         string
		src          []byte
		limit        int64
		title        string // the kept tag's
		keptOff, end int
		dups         []dup
		err          error
	}{
		{"one tag", slices.Concat(first, audio), 1 << 20, "First", 0, n1, nil, nil},
		{"two tags", slices.Concat(first, second, audio), 1 << 20, "First", 0, n1 + n2, []dup{{n1, n2, 4, true}}, nil},
		{"three tags", slices.Concat(first, second, third, audio), 1 << 20, "First", 0, n1 + n2 + len(third),
			[]dup{{n1, n2, 4, true}, {n1 + n2, len(third), 3, true}}, nil},
		{"an unparseable duplicate", slices.Concat(first, broken, audio), 1 << 20, "First", 0, n1 + len(broken),
			[]dup{{n1, len(broken), 1, false}}, nil},
		{"an empty tag ahead", slices.Concat(empty, first, audio), 1 << 20, "First", 10, 10 + n1, []dup{{0, 10, 3, false}}, nil},
		{"an unreadable tag ahead", slices.Concat(broken, first, audio), 1 << 20, "First", len(broken), len(broken) + n1,
			[]dup{{0, len(broken), 1, false}}, nil},
		{"empty tags alone", slices.Concat(empty, empty, audio), 1 << 20, "", 0, 20, []dup{{10, 10, 3, false}}, nil},
		{"a tag of unreadable bytes ahead", slices.Concat(unreadable, first, audio), 1 << 20, "First", len(unreadable),
			len(unreadable) + n1, []dup{{0, len(unreadable), 3, false}}, nil},
		{"a tag after a gap", slices.Concat(first, []byte{0}, second, audio), 1 << 20, "First", 0, n1, nil, nil},
		{"a duplicate over the limit", slices.Concat(first, second, audio), int64(n2) - 1, "", 0, 0, nil, waxerr.ErrSizeTooLarge},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := core.BytesSource(c.src)
			front, ws, err := ReadFrontAll(src, src.Size(), c.limit, 0)
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if err != nil {
				return
			}
			if v, _ := Project(front.Tag).Tags.First(tag.Title); v != c.title || front.Off != int64(c.keptOff) || front.End != int64(c.end) {
				t.Errorf("kept tag title %q at %d, chain end %d; want %q at %d, end %d", v, front.Off, front.End, c.title, c.keptOff, c.end)
			}
			if len(front.Dups) != len(c.dups) {
				t.Fatalf("duplicates %+v, want %+v", front.Dups, c.dups)
			}
			for i, d := range front.Dups {
				w := c.dups[i]
				g := d.digest
				holds := g.readable && (g.tags.Len() > 0 || len(g.pictures) > 0 || len(g.chapters) > 0 || len(g.lyrics) > 0 || len(g.frames) > 0)
				if d.Off != int64(w.off) || d.Len != int64(w.n) || d.Version != w.version || holds != w.holds {
					t.Errorf("duplicate %d = %+v, want %+v", i, d, w)
				}
			}
			if got := len(core.WarningsWithCode(ws, core.WarnDuplicateTagBlock)) == 1; got != (len(c.dups) > 0) {
				t.Errorf("warnings %v, duplicate-tag-block reported %v, want %v", ws, got, len(c.dups) > 0)
			}
		})
	}
}

// A front tag of a major version no specification defines is ignored, not a parse failure: it
// spans its declared length, reads nothing, and is reported, and a rewrite says it is not carried.
// ParseTag itself still refuses it; the tolerance is the front tag's alone.
func TestReadFrontAllUnknownVersion(t *testing.T) {
	audio := []byte{0xFF, 0xFB, 0x90, 0x00, 1, 2, 3}
	for _, major := range []byte{0, 1, 5} {
		tagBytes := testfile.ID3v2(major, testfile.TextFrame(4, "TIT2", "Unknown"))
		src := core.BytesSource(slices.Concat(tagBytes, audio))
		front, _, err := ReadFrontAll(src, src.Size(), 1<<20, 0)
		if err != nil {
			t.Fatalf("v2.%d: %v", major, err)
		}
		tg, n, dups := front.Tag, front.Len, front.Dups
		if tg.Ignored() == "" || n != int64(len(tagBytes)) || len(dups) != 0 || len(tg.Frames()) != 0 {
			t.Errorf("v2.%d: ignored %q, length %d, %d frames; want an ignored tag of %d bytes", major, tg.Ignored(), n, len(tg.Frames()), len(tagBytes))
		}
		if ws := core.WarningsWithCode(Project(tg).Warnings, core.WarnMalformedTagEntry); len(ws) != 1 {
			t.Errorf("v2.%d: warnings %v, want one malformed-tag-entry", major, Project(tg).Warnings)
		}
		ws := AppendMalformedTailDropped(nil, tg, true)
		if len(ws) != 1 || !strings.Contains(ws[0].Message, fmt.Sprintf("ID3v2.%d", major)) {
			t.Errorf("v2.%d: rewrite warnings %v, want malformed-tag-entry-dropped naming the version", major, ws)
		}
		if _, err := ParseTag(tagBytes, 0); !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("v2.%d: ParseTag err = %v, want invalid data", major, err)
		}
	}
}

// A front tag that declares more bytes than the file holds is an unreadable region, not essence: the
// tag is ignored and reported, its extent past the header unknown, so it is unbounded and the codec
// ends it where the stream starts; no duplicate is looked for behind it.
func TestReadFrontAllTagPastTheEnd(t *testing.T) {
	tagBytes := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Cut"))
	copy(tagBytes[6:10], testfile.Syncsafe(1<<20))
	src := core.BytesSource(slices.Concat(tagBytes, testfile.ID3v2(3), []byte{0xFF, 0xFB, 0x90, 0x00}))
	front, ws, err := ReadFrontAll(src, src.Size(), 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	tg, n, dups := front.Tag, front.Len, front.Dups
	if !tg.Unbounded() || !strings.Contains(tg.Ignored(), "declares") || n != 10 || len(dups) != 0 || len(ws) != 0 {
		t.Errorf("unbounded %v, ignored %q, length %d, %d duplicates, warnings %v; want an unbounded ignored tag past its 10-byte header",
			tg.Unbounded(), tg.Ignored(), n, len(dups), ws)
	}
	if got := core.WarningsWithCode(Project(tg).Warnings, core.WarnMalformedTagEntry); len(got) != 1 {
		t.Errorf("warnings %v, want one malformed-tag-entry", Project(tg).Warnings)
	}
}

// markerCodec is a stream that opens with "MARK"; ReadFrontStray only sniffs and resyncs it.
type markerCodec struct{ core.Codec }

func (markerCodec) Sniff(b []byte) bool { return bytes.HasPrefix(b, []byte("MARK")) }

func (markerCodec) SkipsLeadingID3() bool { return true }

func (markerCodec) Resync(window []byte) (int, bool) {
	i := bytes.Index(window, []byte("MARK"))
	return i, i >= 0
}

// ReadFrontStray returns the first leading tag and where the stream starts behind it: past chained
// tags and a gap, which go with the tag, or at the tag's end when no stream is found there. The
// windows it searches are refused over the limit.
func TestReadFrontStray(t *testing.T) {
	lead := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Lead"))
	second := testfile.ID3v2(3, testfile.TextFrame(3, "TALB", "Second"))
	gap := []byte{1, 2, 3}
	stray := []core.WarningCode{core.WarnStrayLeadingID3}
	strayAndGap := []core.WarningCode{core.WarnStrayLeadingID3, core.WarnLeadingBytes}
	for _, c := range []struct {
		name     string
		src      []byte
		limit    int64
		raw      []byte
		streamAt int
		codes    []core.WarningCode
		err      error
	}{
		{"no tag", []byte("MARK"), 1 << 20, nil, 0, nil, nil},
		{"tag then stream", slices.Concat(lead, []byte("MARK")), 1 << 20, lead, len(lead), stray, nil},
		{"tag, gap, stream", slices.Concat(lead, gap, []byte("MARK")), 1 << 20, lead, len(lead) + len(gap), strayAndGap, nil},
		{"two tags then stream", slices.Concat(lead, second, []byte("MARK")), 1 << 20, lead, len(lead) + len(second), strayAndGap, nil},
		{"tag, no stream", slices.Concat(lead, gap), 1 << 20, lead, len(lead), stray, nil},
		{"sniff window over the limit", slices.Concat(lead, []byte("MARK"), make([]byte, 600)), 512, nil, 0, nil, waxerr.ErrSizeTooLarge},
		{"resync window over the limit", slices.Concat(lead, gap, make([]byte, 2000), []byte("MARK")), 1024, nil, 0, nil, waxerr.ErrSizeTooLarge},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := core.BytesSource(c.src)
			raw, streamAt, ws, err := ReadFrontStray(src, src.Size(), c.limit, markerCodec{}, "marker")
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			var codes []core.WarningCode
			for _, w := range ws {
				codes = append(codes, w.Code)
			}
			if !bytes.Equal(raw, c.raw) || streamAt != int64(c.streamAt) || !slices.Equal(codes, c.codes) {
				t.Errorf("raw %d bytes, streamAt %d, warnings %v; want %d bytes, %d, %v",
					len(raw), streamAt, codes, len(c.raw), c.streamAt, c.codes)
			}
		})
	}
	// The windows lie inside the file, so a read of one that fails is an error, not a missing stream.
	big := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", strings.Repeat("x", 2000))) // past the first window
	for _, c := range []struct {
		name string
		src  []byte
		from int
	}{
		{"the first window, past a small tag", slices.Concat(lead, []byte("MARK"), make([]byte, 600)), len(lead) + 1},
		{"the window behind the tag", slices.Concat(big, []byte("MARK")), len(big)},
		{"the resync window", slices.Concat(big, gap, make([]byte, 2000), []byte("MARK")), len(big) + 1100},
	} {
		src := &testfile.Faulty{Src: core.BytesSource(c.src), Fails: testfile.From(int64(c.from))}
		if _, _, _, err := ReadFrontStray(src, src.Size(), 1<<20, markerCodec{}, "marker"); !errors.Is(err, syscall.EIO) {
			t.Errorf("a read failing in %s: err = %v, want the read's own failure", c.name, err)
		}
	}
}

func TestParseV1(t *testing.T) {
	b := make([]byte, 128)
	copy(b[0:3], "TAG")
	copy(b[3:33], "The Title")
	copy(b[33:63], "The Artist")
	copy(b[63:93], "The Album")
	copy(b[93:97], "2020")
	copy(b[97:125], "A comment")
	b[125] = 0  // v1.1 marker
	b[126] = 7  // track 7
	b[127] = 17 // genre Rock
	v1, ok := ParseV1(b)
	if !ok {
		t.Fatal("ParseV1 failed")
	}
	if v1.Title != "The Title" || v1.Artist != "The Artist" || v1.Album != "The Album" ||
		v1.Year != "2020" || v1.Comment != "A comment" || v1.Track != 7 || v1.Genre != "Rock" {
		t.Errorf("v1 = %+v", v1)
	}
}

// TestLegacyV1FamiliesRun: every 128-byte tag of a stacked run projects once, in file order.
func TestLegacyV1FamiliesRun(t *testing.T) {
	auth := tag.NewTagSet()
	auth.Set(tag.Title, "Second")
	run := slices.Concat(testfile.ID3v1("First", "", "", "", "", 255), testfile.ID3v1("Second", "", "", "", "", 255))
	type entry struct {
		value    string
		selected bool
	}
	var got []entry
	for _, f := range LegacyV1Families(auth, run) {
		if f.Family != core.FamilyID3v1 || f.Key != tag.Title || !f.Legacy || len(f.Values) != 1 {
			t.Fatalf("family %+v, want one legacy ID3v1 title", f)
		}
		got = append(got, entry{f.Values[0], f.Selected})
	}
	if want := []entry{{"First", false}, {"Second", true}}; !slices.Equal(got, want) {
		t.Errorf("families = %+v, want %+v", got, want)
	}
}

// TestRenderNumTotalNoTripleSlash checks a number already carrying a total ("5/12") plus
// an explicit total never composes "5/12/20", which re-reads as TRACKTOTAL="12/20". The
// explicit total wins, an embedded one is kept when no explicit total is set, and leading
// zeros survive (SplitNumberTotal, not ParseNumPair).
func TestRenderNumTotalNoTripleSlash(t *testing.T) {
	cases := []struct{ num, total, want string }{
		{"5/12", "20", "5/20"}, // explicit total wins over the embedded one
		{"5/12", "", "5/12"},   // embedded total kept when no explicit total
		{"03", "09", "03/09"},  // leading zeros preserved, not renumbered to 3/9
	}
	for _, frame := range []struct {
		id             string
		numKey, totKey tag.Key
	}{
		{"TRCK", tag.TrackNumber, tag.TrackTotal},
		{"TPOS", tag.DiscNumber, tag.DiscTotal},
	} {
		for _, c := range cases {
			edited := tag.NewTagSet()
			edited.Set(frame.numKey, c.num)
			if c.total != "" {
				edited.Set(frame.totKey, c.total)
			}
			out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{}, WriteOpts{})
			var body []byte
			for _, f := range out {
				if f.ID == frame.id {
					body = f.Body
				}
			}
			if body == nil {
				t.Fatalf("%s num=%q total=%q: no %s frame emitted", frame.id, c.num, c.total, frame.id)
			}
			if got := decodeTextFrame(body); !slices.Equal(got, []string{c.want}) {
				t.Errorf("%s num=%q total=%q => %v, want %q", frame.id, c.num, c.total, got, c.want)
			}
		}
	}
}

// TestRenderNumTotalPathologicalResidual checks a malformed number pair the editor leaves
// unsplit: the writer renders "1/2/3" verbatim and the read path keeps it verbatim on
// TrackNumber, through the same tag.NumberTotalSplit gate the editor and every other text
// codec apply, rather than composing a non-numeric number and a "2/3" total.
func TestRenderNumTotalPathologicalResidual(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Set(tag.TrackNumber, "1/2/3")
	out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{}, WriteOpts{})
	var body []byte
	for _, f := range out {
		if f.ID == "TRCK" {
			body = f.Body
		}
	}
	if got := decodeTextFrame(body); !slices.Equal(got, []string{"1/2/3"}) {
		t.Fatalf("TRCK = %v, want [1/2/3] rendered verbatim", got)
	}
	ts := Project(buildTag(t, 4, out)).Tags
	if n, _ := ts.First(tag.TrackNumber); n != "1/2/3" {
		t.Errorf("re-read TrackNumber = %q, want 1/2/3 (malformed pair kept verbatim)", n)
	}
	if ts.Has(tag.TrackTotal) {
		tot, _ := ts.First(tag.TrackTotal)
		t.Errorf("re-read TrackTotal = %q, want absent (no total composed from a malformed pair)", tot)
	}
}

// TestDecodeFrameDeunsyncBeforeStrip checks a v2.4 grouped frame under tag-level
// unsynchronisation is de-unsynchronised before the group byte is stripped; otherwise a
// 0x00 stuffing byte after the stripped 0xFF splits "Hi" into ["","Hi"].
func TestDecodeFrameDeunsyncBeforeStrip(t *testing.T) {
	// Clean region: group byte 0xFF, then a Latin-1 "Hi" text body {00 'H' 'i'}.
	// Unsynchronising {FF 00 48 69} stuffs a 0x00 after the FF -> on-disk {FF 00 00 48 69}.
	raw := []byte{0xFF, 0x00, 0x00, 0x48, 0x69}
	// Intermediate: de-unsync recovers {FF 00 48 69} (group byte still leading).
	if got := deunsync(raw); !bytes.Equal(got, []byte{0xFF, 0x00, 0x48, 0x69}) {
		t.Fatalf("deunsync(raw) = % x, want FF 00 48 69", got)
	}
	flags := [2]byte{0, v24Grouping}
	f := decodeFrame("TIT2", flags, raw, 4, true /* tagUnsync */)
	// After the group strip the body is {00 48 69} = Latin-1 "Hi", a single value.
	if !bytes.Equal(f.Body, []byte{0x00, 0x48, 0x69}) {
		t.Errorf("frame body = % x, want 00 48 69 (group stripped after de-unsync)", f.Body)
	}
	if got := decodeTextFrame(f.Body); !slices.Equal(got, []string{"Hi"}) {
		t.Errorf("decoded TIT2 = %v, want [Hi] (not the [\"\",\"Hi\"] corruption)", got)
	}
}

// TestRebuildPreservesCommentLanguage: a COMM or USLT whose text an edit replaces keeps its
// language, the item's qualifiers following the value as the editor aligns them. A new comment
// defaults to "eng", and a garbage 3-byte language round-trips verbatim.
func TestRebuildPreservesCommentLanguage(t *testing.T) {
	lang := func(body []byte) string {
		if len(body) < 4 {
			return ""
		}
		return string(body[1:4])
	}
	find := func(out []Frame, id string) []byte {
		for _, f := range out {
			if f.ID == id {
				return f.Body
			}
		}
		return nil
	}
	// rebuild edits the comment and lyrics text of orig as Editor.Set does.
	rebuild := func(orig []Frame, comment, lyrics string) []Frame {
		proj := Project(&Tag{frames: orig})
		edited := proj.Tags.Clone()
		edited.Set(tag.Comment, comment)
		if lyrics != "" {
			edited.Set(tag.Lyrics, lyrics)
		}
		out, _ := RebuildFrames(orig, proj.Tags, edited, 4, StructuredEdit{
			Comments: core.AlignTextItems(proj.Comments, edited, tag.Comment),
			Lyrics:   core.AlignTextItems(proj.Lyrics, edited, tag.Lyrics),
		}, WriteOpts{})
		return out
	}

	// A COMM and a USLT carried in German; editing the value keeps the language.
	out := rebuild([]Frame{
		{ID: "COMM", Body: encodeComment(4, "deu", "", []string{"Hallo"})},
		{ID: "USLT", Body: encodeLangText(4, "deu", "", "Strophe")},
	}, "Hallo Welt", "Neue Strophe")
	if got := lang(find(out, "COMM")); got != "deu" {
		t.Errorf("edited COMM language = %q, want deu", got)
	}
	if got := lang(find(out, "USLT")); got != "deu" {
		t.Errorf("edited USLT language = %q, want deu", got)
	}

	// A brand-new comment (no original frame) defaults to eng.
	if got := lang(find(rebuild(nil, "Added", ""), "COMM")); got != "eng" {
		t.Errorf("new COMM language = %q, want eng", got)
	}

	// A garbage (non-ASCII) 3-byte language round-trips verbatim, not normalized, and so does
	// the undefined marker.
	for _, raw := range []string{string([]byte{0x01, 0x02, 0x03}), string([]byte{0xFF, 0xFE, 0xFD}), "XXX", "\x00\x00\x00"} {
		out := rebuild([]Frame{{ID: "COMM", Body: encodeComment(4, raw, "", []string{"x"})}}, "y", "")
		if got := lang(find(out, "COMM")); got != raw {
			t.Errorf("edited COMM language = % x, want % x (round-trips)", got, raw)
		}
	}
}

// TestRebuildKeepsMultiLanguageCommentsOnUnrelatedEdit checks an unrelated edit keeps a
// v2.3 tag's two managed COMM frames, in different languages, verbatim.
func TestRebuildKeepsMultiLanguageCommentsOnUnrelatedEdit(t *testing.T) {
	orig := []Frame{
		{ID: "COMM", Body: encodeComment(3, "eng", "", []string{"English"})},
		{ID: "COMM", Body: encodeComment(3, "deu", "", []string{"Deutsch"})},
	}
	base := Project(&Tag{frames: orig}).Tags
	edited := base.Clone()
	edited.Set(tag.Artist, "New Artist") // unrelated to the comments

	out, _ := RebuildFrames(orig, base, edited, 3, StructuredEdit{}, WriteOpts{})

	var langs, texts []string
	var tpe1 []byte
	for _, f := range out {
		switch f.ID {
		case "COMM":
			langs = append(langs, string(f.Body[1:4]))
			if _, vals, ok := decodeCommentFrame(f.Body); ok && len(vals) > 0 {
				texts = append(texts, vals[0])
			}
		case "TPE1":
			tpe1 = f.Body
		}
	}
	if !slices.Equal(langs, []string{"eng", "deu"}) {
		t.Errorf("COMM languages = %v, want [eng deu] (both frames kept with their languages)", langs)
	}
	if !slices.Equal(texts, []string{"English", "Deutsch"}) {
		t.Errorf("COMM texts = %v, want [English Deutsch]", texts)
	}
	// The unrelated edit still landed.
	if got := DecodeText(Frame{ID: "TPE1", Body: tpe1}); len(got) == 0 || got[0] != "New Artist" {
		t.Errorf("TPE1 = %v, want [New Artist] (the unrelated edit landed)", got)
	}
}

// FuzzParseTag exercises the top-level parse chain (ParseTag -> parseFrames -> deunsync ->
// decodeFrame, plus skipExtHeader and the footer/unsync bounds) that the CHAP/CTOC/SYLT
// body fuzzers do not reach. A successful parse is pushed through the projector, and
// every canonical key and value must be valid UTF-8; a key hit would be a projector leak
// (a TXXX-derived key carrying raw bytes), not a wrong test.
func FuzzParseTag(f *testing.F) {
	// Minimal empty tags at each major version.
	f.Add([]byte("ID3\x03\x00\x00\x00\x00\x00\x00")) // empty v2.3
	f.Add([]byte("ID3\x04\x00\x00\x00\x00\x00\x00")) // empty v2.4
	f.Add([]byte("ID3\x02\x00\x00\x00\x00\x00\x00")) // empty v2.2 (3-char frame IDs)

	// A tag carrying one TIT2 text frame ("hi", ISO-8859-1) at v2.3 and v2.4.
	f.Add(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "hi")))
	f.Add(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "hi")))

	// A v2.4 tag whose frame body needs unsync normalization (0xFF 0x00 stuffing), tag-level unsync.
	rawBody := []byte{0xFF, 0x00, 0x42, 0xFF, 0x00}
	var ufsz [4]byte
	putSyncSafe(ufsz[:], int64(len(rawBody)))
	uframe := append([]byte("TIT2"), ufsz[:]...)
	uframe = append(uframe, 0x00, 0x00)
	uframe = append(uframe, rawBody...)
	var utsz [4]byte
	putSyncSafe(utsz[:], int64(len(uframe)))
	f.Add(append(append([]byte{'I', 'D', '3', 4, 0, hdrUnsync}, utsz[:]...), uframe...))

	// A footer-flagged v2.4 tag and an extended-header v2.4 tag (empty bodies), to walk those paths.
	f.Add([]byte{'I', 'D', '3', 4, 0, hdrFooter, 0, 0, 0, 0})
	f.Add([]byte{'I', 'D', '3', 4, 0, hdrExtHeader, 0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		tg, err := ParseTag(data, bits.DefaultLimits.MaxElements)
		if err != nil || tg == nil {
			return
		}
		proj := Project(tg) // named tg: a tag var would shadow the tag package
		for _, k := range proj.Tags.Keys() {
			if !utf8.ValidString(string(k)) {
				t.Errorf("invalid UTF-8 in canonical key: %q", k)
			}
			vals, _ := proj.Tags.Get(k) // Get returns ([]string, bool)
			for _, v := range vals {
				if !utf8.ValidString(v) {
					t.Errorf("invalid UTF-8 in canonical value: %q", v)
				}
			}
		}
	})
}

// TestV22CompressionFlagIgnoresTag: ID3v2.2 defines no compression scheme, so a tag with the
// flag set is ignored as the spec directs, and says so.
func TestV22CompressionFlagIgnoresTag(t *testing.T) {
	tg, err := ParseTag(wrapTag(2, 0x40, rawFrame(2, "TT2", []byte("\x00Compressed"))), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tg.Frames()) != 0 {
		t.Errorf("frames = %d, want none", len(tg.Frames()))
	}
	if tg.Ignored() == "" {
		t.Error("the tag should report why it was ignored")
	}
	if tg.Padding() != 0 {
		t.Errorf("padding = %d, want 0 (the region is unreadable, not free)", tg.Padding())
	}
	if n := tg.WithFrames(nil).Ignored(); n != "" {
		t.Errorf("a rewritten tag must not stay ignored: %q", n)
	}
	// The same bit on v2.3 is the extended-header flag and must keep working.
	v3, err := ParseTag(wrapTag(3, 0x40, append([]byte{0, 0, 0, 6, 0, 0, 0, 0, 0, 0}, rawFrame(3, "TIT2", []byte("\x00T"))...)), 0)
	if err != nil || len(v3.Frames()) != 1 {
		t.Errorf("v2.3 extended header: err=%v frames=%d", err, len(v3.Frames()))
	}
}

// SkipTags passes the ID3v1 and ID3v2 tags that open at an offset, each by its header, at most
// core.MaxLeadingTags of them, and counts them; a tag that runs past the end stops it, and a read
// over the limit is refused.
func TestSkipTags(t *testing.T) {
	v1 := testfile.ID3v1("T", "", "", "", "", 255)
	v2 := testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "x"))
	frame := []byte{0xFF, 0xFB, 0x90, 0x00, 0, 0, 0, 0}
	for _, c := range []struct {
		name string
		data []byte
		want int
		tags int
	}{
		{"an ID3v1 tag and two ID3v2 tags", slices.Concat(v1, v2, v2, frame), len(v1) + 2*len(v2), 3},
		{"no tag", frame, 0, 0},
		{"a tag running past the end", slices.Concat(v1, v2[:len(v2)-1]), len(v1), 1},
		{"more tags than the bound", bytes.Repeat(v2, core.MaxLeadingTags+1), core.MaxLeadingTags * len(v2), core.MaxLeadingTags},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, tags, err := SkipTags(core.BytesSource(c.data), 0, int64(len(c.data)), 1<<20)
			if err != nil || got != int64(c.want) || tags != c.tags {
				t.Errorf("SkipTags = %d, %d tags, %v; want %d, %d tags", got, tags, err, c.want, c.tags)
			}
		})
	}
	if _, _, err := SkipTags(core.BytesSource(slices.Concat(v1, frame)), 0, 200, 64); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("a header read over the limit: err = %v, want declared size too large", err)
	}
	// The headers lie inside the file, so a read of one that fails is an error; one past the end of
	// the file is no tag.
	data := slices.Concat(v1, v2, frame)
	if _, _, err := SkipTags(&testfile.Faulty{Src: core.BytesSource(data), Fails: testfile.From(int64(len(v1)))}, 0, int64(len(data)), 1<<20); !errors.Is(err, syscall.EIO) {
		t.Errorf("a header read failing inside the file: err = %v, want the read's own failure", err)
	}
	if got, tags, err := SkipTags(core.BytesSource(v1[:100]), 0, 200, 1<<20); err != nil || got != 0 || tags != 0 {
		t.Errorf("a header past the end of the file: %d, %d tags, %v; want no tag", got, tags, err)
	}
}

// An ID3v1 field echoes a canonical value when it holds what ID3v1 can store of it: the value cut to
// the field's width (30 bytes for a title, the 4-digit year for a date), trailing spaces aside.
func TestV1Echoes(t *testing.T) {
	long := "A Title Far Longer Than Thirty Characters"
	for _, c := range []struct {
		key         tag.Key
		auth, value string
		width       int
		echoes      bool
	}{
		{tag.Title, long, long[:30], 30, true},
		{tag.Title, "A Title Whose 30th Char Is: Sp ace", "A Title Whose 30th Char Is: Sp", 30, true},
		{tag.Title, long, long[:29], 30, false},
		{tag.Title, "Song (Live)", "Song", 30, false},
		{tag.Title, "Song", "song", 30, true},
		{tag.RecordingDate, "2001-05-06", "2001", 4, true},
		{tag.RecordingDate, "2001-05-06", "2002", 4, false},
		{tag.Comment, strings.Repeat("c", 40), strings.Repeat("c", 28), 28, true},
		{tag.Genre, "Rock", "Rock", 0, false},
	} {
		auth := tag.NewTagSet()
		auth.Set(c.key, c.auth)
		if got := V1Echoes(auth, c.key, c.value, c.width); got != c.echoes {
			t.Errorf("V1Echoes(%s %q, %q, %d) = %v, want %v", c.key, c.auth, c.value, c.width, got, c.echoes)
		}
	}
}

// LegacyV1Families marks an echo selected, so a cut title and a bare year are not a conflict, while a
// different value still is.
func TestLegacyV1FamiliesEchoesAreSelected(t *testing.T) {
	long := "A Title Far Longer Than Thirty Characters"
	auth := tag.NewTagSet()
	auth.Set(tag.Title, long)
	auth.Set(tag.RecordingDate, "2001-05-06")
	auth.Set(tag.Artist, "Song (Live)")
	for _, f := range LegacyV1Families(auth, testfile.ID3v1(long, "Song", "", "2001", "", 255)) {
		if want := f.Key != tag.Artist; f.Selected != want {
			t.Errorf("%s %v selected %v, want %v", f.Key, f.Values, f.Selected, want)
		}
	}
}

// Grouping bytes and status flags survive a parse and a render byte for byte: a v2.3 and a v2.4
// frame in group 7, and a v2.4 frame whose status says to discard it if the tag is altered. The
// text still reads, the group byte stripped from it, and dump notes the group.
func TestFrameGroupAndStatusRoundTrip(t *testing.T) {
	for _, c := range []struct {
		name    string
		version byte
		frame   []byte
		group   bool
	}{
		{"v2.3 grouped", 3, testfile.ID3FrameFlags(3, "TPE1", [2]byte{0, 0x20}, []byte("\x07\x00Grouped")), true},
		{"v2.4 grouped", 4, testfile.ID3FrameFlags(4, "TPE1", [2]byte{0, 0x40}, []byte("\x07\x00Grouped")), true},
		{"v2.4 status", 4, testfile.ID3FrameFlags(4, "TPE1", [2]byte{0x40, 0}, []byte("\x00Grouped")), false},
		{"v2.3 read-only", 3, testfile.ID3FrameFlags(3, "TPE1", [2]byte{0x20, 0}, []byte("\x00Grouped")), false},
	} {
		data := testfile.ID3v2(c.version, testfile.TextFrame(c.version, "TIT2", "Title"), c.frame)
		tg, err := ParseTag(data, 0)
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := Project(tg).Tags.First(tag.Artist); v != "Grouped" {
			t.Errorf("%s: ARTIST = %q, want Grouped", c.name, v)
		}
		if got := Render(c.version, tg.Frames(), 0); !bytes.Equal(got, data) {
			t.Errorf("%s: render %q, want the source %q", c.name, got, data)
		}
		if got := RenderedSize(tg.Frames()); got != int64(len(data)) {
			t.Errorf("%s: RenderedSize %d, want %d", c.name, got, len(data))
		}
		if note := FrameNote(tg.Frames()[1]); strings.Contains(note, "group 0x07") != c.group {
			t.Errorf("%s: note %q, group noted want %v", c.name, note, c.group)
		}
	}
}

// TestCommentGroupsUnderV23Policies: comments of one description and language share a frame under
// each v2.3 multi-value policy, and another description is a frame of its own.
func TestCommentGroupsUnderV23Policies(t *testing.T) {
	items := []core.TextItem{{Text: "a"}, {Text: "b", Description: "d"}, {Text: "c"}}
	edited := tag.NewTagSet()
	edited.Set(tag.Comment, core.TextsOf(items)...)
	eng := func(text, desc string) core.TextItem {
		return core.TextItem{Text: text, Description: desc, Language: "eng"}
	}
	cases := []struct {
		pol    core.ID3MultiValuePolicy
		frames int
		multi  bool
		want   []core.TextItem
	}{
		{core.ID3MultiNullSep, 2, true, []core.TextItem{eng("a", ""), eng("c", ""), eng("b", "d")}},
		{core.ID3MultiRepeatFrame, 3, false, []core.TextItem{eng("a", ""), eng("c", ""), eng("b", "d")}},
		{core.ID3MultiSlash, 2, false, []core.TextItem{eng("a / c", ""), eng("b", "d")}},
	}
	for _, c := range cases {
		out, info := RebuildFrames(nil, tag.NewTagSet(), edited, 3, StructuredEdit{Comments: items}, WriteOpts{Multi: c.pol})
		if len(out) != c.frames || info.UsedV23Multi != c.multi {
			t.Errorf("policy %v: %d frames, v2.3 multi %v; want %d, %v", c.pol, len(out), info.UsedV23Multi, c.frames, c.multi)
		}
		if got := Project(&Tag{srcVersion: 3, writeVersion: 3, frames: out}).Comments; !slices.Equal(got, c.want) {
			t.Errorf("policy %v: comments read back %+v, want %+v", c.pol, got, c.want)
		}
	}
}

// TestTextFramesTrailingEmpty: an empty comment that ends a frame of several texts is lost and
// reported; one alone in its frame, or a lyric, which has a frame to itself, is stored.
func TestTextFramesTrailingEmpty(t *testing.T) {
	rebuild := func(comments, lyrics []core.TextItem) ([]Frame, RebuildInfo) {
		edited := tag.NewTagSet()
		edited.Set(tag.Comment, core.TextsOf(comments)...)
		edited.Set(tag.Lyrics, core.TextsOf(lyrics)...)
		return RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{Comments: comments, Lyrics: lyrics}, WriteOpts{})
	}
	// The empty comment shares the described frame with "a": lost. LYRICS ends in an empty too,
	// in a frame of its own.
	_, info := rebuild([]core.TextItem{{Text: "a", Description: "d"}, {Text: "", Description: "d"}, {Text: "b"}},
		[]core.TextItem{{Text: "l"}, {Text: ""}})
	if !slices.Equal(info.DroppedTrailingValues, []tag.Key{tag.Comment}) {
		t.Errorf("dropped trailing values = %v, want [COMMENT]", info.DroppedTrailingValues)
	}
	// The empty comment is the only text of its frame: stored, though it ends the value list.
	out, info := rebuild([]core.TextItem{{Text: "a", Description: "d"}, {Text: ""}}, nil)
	if len(info.DroppedTrailingValues) != 0 {
		t.Errorf("dropped trailing values = %v, want none", info.DroppedTrailingValues)
	}
	want := []core.TextItem{{Text: "a", Description: "d", Language: "eng"}, {Text: "", Language: "eng"}}
	if got := Project(&Tag{srcVersion: 4, writeVersion: 4, frames: out}).Comments; !slices.Equal(got, want) {
		t.Errorf("comments read back %+v, want %+v", got, want)
	}
}

// TestLegacyV2FamiliesQualifiersAreOpaque: a stray tag's comment or lyric with a description, or
// a language other than the default, is content a strip cannot prove redundant.
func TestLegacyV2FamiliesQualifiersAreOpaque(t *testing.T) {
	auth := tag.NewTagSet()
	auth.Set(tag.Comment, "c")
	auth.Set(tag.Lyrics, "l")
	cases := []struct {
		name   string
		frames []Frame
		opaque bool
	}{
		{"default language", []Frame{{ID: "COMM", Body: encodeComment(4, "eng", "", []string{"c"})}, {ID: "USLT", Body: encodeLangText(4, "eng", "", "l")}}, false},
		{"undefined language", []Frame{{ID: "COMM", Body: encodeComment(4, "XXX", "", []string{"c"})}}, false},
		{"described comment", []Frame{{ID: "COMM", Body: encodeComment(4, "eng", "d", []string{"c"})}}, true},
		{"translated lyric", []Frame{{ID: "USLT", Body: encodeLangText(4, "deu", "", "l")}}, true},
	}
	for _, c := range cases {
		if _, opaque, _ := LegacyV2Families(auth, Render(4, c.frames, 0), bits.DefaultLimits.MaxElements); opaque != c.opaque {
			t.Errorf("%s: opaque = %v, want %v", c.name, opaque, c.opaque)
		}
	}
}

// TestChangedCommentUnitFollowsThePolicy: when a comment edit renders the COMM unit again, a
// frame of several texts is laid out as the multi-value policy asks, not kept NUL-separated.
func TestChangedCommentUnitFollowsThePolicy(t *testing.T) {
	orig := []Frame{
		{ID: "COMM", Body: encodeComment(3, "eng", "", []string{"a", "b"})},
		{ID: "COMM", Body: encodeComment(3, "eng", "d", []string{"x"})},
	}
	proj := Project(&Tag{srcVersion: 3, writeVersion: 3, frames: orig})
	edited := proj.Tags.Clone()
	edited.Set(tag.Comment, "a", "b", "y")
	items := core.AlignTextItems(proj.Comments, edited, tag.Comment)
	for pol, frames := range map[core.ID3MultiValuePolicy]int{core.ID3MultiNullSep: 2, core.ID3MultiRepeatFrame: 3, core.ID3MultiSlash: 2} {
		out, info := RebuildFrames(orig, proj.Tags, edited, 3, StructuredEdit{Comments: items, CommentsChanged: true}, WriteOpts{Multi: pol})
		if len(out) != frames || info.UsedV23Multi != (pol == core.ID3MultiNullSep) {
			t.Errorf("policy %v: %d frames, v2.3 multi %v; want %d, %v", pol, len(out), info.UsedV23Multi, frames, pol == core.ID3MultiNullSep)
		}
	}
}
