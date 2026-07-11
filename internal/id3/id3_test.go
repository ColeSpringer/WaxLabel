package id3

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestSizeErrHumanized: the size-limit messages report humanized binary
// magnitudes rather than raw byte counts. sizeErr takes the count directly, so no
// oversized buffer is allocated.
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
	// A foreign frame ending in a double NUL (a padding terminator after the value's
	// own terminator) must not yield a phantom trailing empty - that would defeat no-op
	// detection on such files. Matches TagLib/mutagen.
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
	// v2.3 TIME stores only HHMM, so seconds past a full minute are dropped - the same
	// class of loss as the existing month/hour reductions.
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
// normalization. Tag-level and frame-level unsync both normalize the body; a frame with
// no unsync flag keeps its bytes and flags unchanged.
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

// TestRewriteBase covers the shared WAV/AIFF ID3 diff base: tagless files use an
// empty base, legacy strip uses only the parsed ID3 frames so native-only values
// get written into ID3, and normal rewrites use the merged projection.
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

// TestNumericGenreWarningSurfaces checks that both user-visible numeric-genre surfaces stay in
// step: the read projection's NumericGenre flag (which drives the [numeric-genre] read warning) and
// detectNumericGenres (which drives the symmetric write warning). Both consume resolveGenres, so a
// zero-padded "007" and an out-of-range "(192)" must warn on neither, while a canonical "7" warns on
// both.
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

// TestDroppedDateDetection checks detectDroppedDates: a year-anchored date key
// whose edited value has no extractable numeric year renders no v2.3 frame and so is
// dropped; the caller turns RebuildInfo.DroppedDates into a value-dropped
// warning. The detection is year-anchored and per key, so a stored or year-bearing
// date never false-fires, and v2.4 (TDRC stores the string) never populates it.
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
		// it is deliberately excluded from the year-anchored drop check.
		{"v23 release no year stored verbatim", tag.ReleaseDate, "Unknown", 3, false},
		{"v23 recording year only", tag.RecordingDate, "2021", 3, false},
		// A shaped-but-invalid date still has an extractable year, so only sub-year
		// precision is lost - not the whole value - and it is not flagged dropped.
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

// TestDroppedDateOnlyTouchedKeys: an unchanged date key (base == edited) is never
// flagged dropped - only a key the edit actually touched can be. This guards the
// per-key, anchored-on-changed half of the rule.
func TestDroppedDateOnlyTouchedKeys(t *testing.T) {
	base := tag.NewTagSet()
	base.Set(tag.RecordingDate, "Unknown")
	edited := tag.NewTagSet()
	edited.Set(tag.RecordingDate, "Unknown")
	if _, info := RebuildFrames(nil, base, edited, 3, StructuredEdit{}, WriteOpts{}); len(info.DroppedDates) != 0 {
		t.Errorf("an unchanged date must not be flagged dropped, got %v", info.DroppedDates)
	}
}

// TestReleaseDateV23StoredNotDropped confirms the exclusion is safe: a non-date
// ReleaseDate string on v2.3 renders a real TXXX:RELEASEDATE frame, so it genuinely
// is not dropped (and must not warn). detectDroppedDates excludes it for this reason.
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

// TestDroppedTrailingValuesByPolicy pins the trailing-empty detector to the write representation.
// A NUL-separated frame strips a trailing empty (so it is a real drop that must warn), while
// repeat-frame preserves the empty as its own frame and slash-join collapses the whole
// multi-value, so neither drops the trailing empty and neither must warn. v2.4 always writes
// NUL-separated regardless of policy. A lone empty and a value with no trailing empty never flag.
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

	// NumericGenre on under a multi-value Slash join: numeric conversion is skipped (a
	// mid-string "(17)" reference would re-read as a reference + slash-prefixed refinement,
	// splitting into garbage), so it stays the documented lossy "Rock / (Live)" single value.
	if got := roundTrip(t, 3, core.ID3MultiSlash, true, in); !slices.Equal(got, []string{"Rock / (Live)"}) {
		t.Errorf("numeric-genre slash round-trip = %v, want [Rock / (Live)]", got)
	}

	// Existing ID3 behavior: a bare numeric value is always a reference, so GENRE="17"
	// reads back as the numeric genre name even with NumericGenre off.
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

// TestTXXXCustomDescriptionCasePreserved verifies that editing a custom TXXX value
// preserves the original description casing instead of rewriting it as the uppercased
// canonical key. Aliased keys still write their preferred Picard spelling.
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

func TestPictureRoundTrip(t *testing.T) {
	pic := core.Picture{Type: core.PicFrontCover, MIME: "image/png", Description: "front", Data: []byte("\x89PNG-data")}
	body := encodeAPIC(pic, 4)
	got, ok := decodeAPIC(body)
	if !ok {
		t.Fatal("decodeAPIC failed")
	}
	if got.Type != core.PicFrontCover || got.MIME != "image/png" || got.Description != "front" ||
		!bytes.Equal(got.Data, pic.Data) {
		t.Errorf("picture round-trip = %+v", got)
	}
}

// TestRebuildBreadthV24 exercises a wide spread of render units (simple text,
// AlbumArtist, Disc number/total, Comment, Lyrics, the MusicBrainz UFID + TXXX
// long tail, v2.4 dates, and a raw pass-through frame) and confirms every value
// survives a rebuild -> render -> parse -> project cycle.
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
	edited.Set(tag.Key("TBPM"), "128")

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

	// Repeat-frame policy: two TXXX frames, no v2.3-extension flag. ID3's "one
	// frame per description" convention discourages repeated same-description
	// frames, but Repeat is an explicit caller opt-in applied uniformly with
	// plain text frames.
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

// TestFrameRenderIDUnmanaged confirms frames we do not model are treated as
// unmanaged (preserved verbatim, never re-rendered).
func TestFrameRenderIDUnmanaged(t *testing.T) {
	unmanaged := []Frame{
		{ID: "WXXX", Body: []byte{0}},                                      // URL frame
		{ID: "PRIV", Body: []byte{1, 2}},                                   // binary
		{ID: "APIC", Body: []byte{0}},                                      // picture (handled separately)
		{ID: "TIT2", Body: []byte{0}, Opaque: true},                        // opaque
		{ID: "UFID", Body: append([]byte("other.example"), 0)},             // non-MusicBrainz UFID
		{ID: "COMM", Body: encodeComment(4, "eng", "desc", []string{"x"})}, // described comment
	}
	for _, f := range unmanaged {
		if _, managed := frameRenderID(f); managed {
			t.Errorf("frame %q (opaque=%v) should be unmanaged", f.ID, f.Opaque)
		}
	}
	// A MusicBrainz UFID and an empty-description COMM are managed.
	mb := Frame{ID: "UFID", Body: encodeUFID(musicBrainzOwner, "id")}
	if _, managed := frameRenderID(mb); !managed {
		t.Error("MusicBrainz UFID should be managed")
	}
}

// TestRebuildDropsStaleAlias confirms that editing a canonical value stored
// under a non-canonical representation does not duplicate it or lose the edit:
// the stale frame is dropped and only the write-version target is written.
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

// TestRebuildDeterministicNewFrames confirms that adding several fields produces
// byte-identical output across runs (the leftover render-ids are sorted, not
// emitted in map order).
func TestRebuildDeterministicNewFrames(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Set(tag.Title, "T")
	edited.Set(tag.Artist, "A")
	edited.Set(tag.Album, "Al")
	edited.Set(tag.Genre, "Rock")
	edited.Set(tag.Barcode, "X")
	edited.Set(tag.ISRC, "Y")

	var first []byte
	for i := 0; i < 25; i++ {
		out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 4, StructuredEdit{}, WriteOpts{})
		data := Render(4, out, 0)
		if i == 0 {
			first = data
		} else if !bytes.Equal(first, data) {
			t.Fatalf("nondeterministic rebuild output on run %d", i)
		}
	}
}

// TestSpacePaddedFrameID confirms a non-conformant space-padded frame ID does
// not end the scan: the padded frame is preserved and a following valid frame is
// still parsed (rather than dropped).
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

// TestNonConformantFrameIDMarkedOpaque covers the re-read half of the unknown-v2.2-frame
// preservation: a space-padded "TXY " ID (the form an unknown v2.2 frame takes once modernized
// to v2.3 and read back) is marked opaque at decode, the single decode entry point. That one
// flag - not a set of scattered per-projection gates - is what keeps the frame preserved
// verbatim and out of the canonical model, and it closes the DecodeText path a text-frame
// scan would otherwise leak through.
func TestNonConformantFrameIDMarkedOpaque(t *testing.T) {
	frame := func(id, text string) []byte {
		body := encodeTextFrame(encLatin1, []string{text})
		h := append([]byte(id), byte(len(body)>>24), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)), 0, 0)
		return append(h, body...)
	}
	txy := frame("TXY ", "leak?") // non-conformant (trailing space) ID
	tit2 := frame("TIT2", "Real")
	var sz [4]byte
	putSyncSafe(sz[:], int64(len(txy)+len(tit2)))
	data := append([]byte{'I', 'D', '3', 3, 0, 0}, sz[:]...)
	data = append(append(data, txy...), tit2...)

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

// TestHugeFrameSizeNoPanic guards the 32-bit overflow: a v2.3 frame header
// declaring size 0xFFFFFFFF must be rejected (the scan stops) rather than
// wrapping to a negative length and panicking on the slice.
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

// TestRenderNumTotalNoTripleSlash verifies that a canonical number already
// carrying a total ("5/12") plus an explicit total never composes "5/12/20",
// which re-reads as TRACKTOTAL="12/20". The explicit total wins; an embedded one
// is kept when no explicit total is set; leading zeros survive because this uses
// SplitNumberTotal, not ParseNumPair.
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

// TestRenderNumTotalPathologicalResidual checks the ID3 behavior for a malformed
// number pair the editor deliberately leaves unsplit. The writer renders "1/2/3"
// verbatim, and the read path now leaves it verbatim on TrackNumber too - the same
// validity gate (tag.NumberTotalSplit) the editor and every other text codec apply -
// instead of composing a non-numeric number and a "2/3" total. The value is already
// flagged malformed at set time, and the typed projection still matches MP4
// (tag.ParseNumPair reads TrackNumber 1 from either shape).
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

// TestDecodeFrameDeunsyncBeforeStrip verifies that a v2.4 grouped frame under
// tag-level unsynchronisation is de-unsynchronised before the group byte is
// stripped. Otherwise a 0x00 stuffing byte that followed the stripped 0xFF stays
// in the payload and splits "Hi" into the corrupt ["","Hi"] pair.
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

// TestRebuildPreservesCommentLanguage verifies that the read path discards the
// COMM/USLT 3-byte language, so an edit of the value must recover it from the
// original frame rather than resetting to "eng". A brand-new comment defaults to
// "eng", and a garbage 3-byte language round-trips verbatim.
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

	// A COMM and a USLT carried in German; editing the value keeps the language.
	orig := []Frame{
		{ID: "COMM", Body: encodeComment(4, "deu", "", []string{"Hallo"})},
		{ID: "USLT", Body: encodeLangText(4, "deu", "", "Strophe")},
	}
	base := Project(&Tag{frames: orig}).Tags
	edited := base.Clone()
	edited.Set(tag.Comment, "Hallo Welt")
	edited.Set(tag.Lyrics, "Neue Strophe")
	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})
	if got := lang(find(out, "COMM")); got != "deu" {
		t.Errorf("edited COMM language = %q, want deu", got)
	}
	if got := lang(find(out, "USLT")); got != "deu" {
		t.Errorf("edited USLT language = %q, want deu", got)
	}

	// A brand-new comment (no original frame) defaults to eng.
	fresh := tag.NewTagSet()
	fresh.Set(tag.Comment, "Added")
	outNew, _ := RebuildFrames(nil, tag.NewTagSet(), fresh, 4, StructuredEdit{}, WriteOpts{})
	if got := lang(find(outNew, "COMM")); got != "eng" {
		t.Errorf("new COMM language = %q, want eng", got)
	}

	// A garbage (non-ASCII) 3-byte language round-trips verbatim, not normalized.
	garbage := string([]byte{0x01, 0x02, 0x03})
	origG := []Frame{{ID: "COMM", Body: encodeComment(4, garbage, "", []string{"x"})}}
	baseG := Project(&Tag{frames: origG}).Tags
	editedG := baseG.Clone()
	editedG.Set(tag.Comment, "y")
	outG, _ := RebuildFrames(origG, baseG, editedG, 4, StructuredEdit{}, WriteOpts{})
	if got := lang(find(outG, "COMM")); got != garbage {
		t.Errorf("edited COMM language = % x, want % x (garbage round-trips)", got, garbage)
	}
}

// TestRebuildKeepsMultiLanguageCommentsOnUnrelatedEdit checks that an unrelated edit
// preserves a v2.3 tag with two managed COMM frames in different languages. Because the
// Comment field is not edited, both original frames and their language codes should carry
// through verbatim.
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

// FuzzParseTag exercises the top-level ID3 parse chain - ParseTag -> parseFrames -> deunsync ->
// decodeFrame, plus skipExtHeader and the footer/unsync bounds - that the CHAP/CTOC/SYLT body
// fuzzers do not reach. Beyond "no panic," a successful parse is pushed through the canonical
// projector and every canonical key and value must be valid UTF-8: a stronger invariant that
// catches a projector leaking raw frame bytes into the canonical model. Values pass SanitizeUTF8,
// so the value assertion is safe; a key hit would be a real projector leak to fix (e.g. a
// TXXX-derived key carrying raw bytes), not a wrong test.
func FuzzParseTag(f *testing.F) {
	// Minimal empty tags at each major version.
	f.Add([]byte("ID3\x03\x00\x00\x00\x00\x00\x00")) // empty v2.3
	f.Add([]byte("ID3\x04\x00\x00\x00\x00\x00\x00")) // empty v2.4
	f.Add([]byte("ID3\x02\x00\x00\x00\x00\x00\x00")) // empty v2.2 (3-char frame IDs)

	// A tag carrying one TIT2 text frame ("hi", ISO-8859-1) at v2.3 and v2.4.
	textFrameTag := func(major byte) []byte {
		body := append([]byte{0x00}, "hi"...) // encoding byte 0x00 (ISO-8859-1) + text
		var fsz [4]byte
		putSyncSafe(fsz[:], int64(len(body)))
		frame := append([]byte("TIT2"), fsz[:]...)
		frame = append(frame, 0x00, 0x00) // frame flags
		frame = append(frame, body...)
		var tsz [4]byte
		putSyncSafe(tsz[:], int64(len(frame)))
		out := append([]byte{'I', 'D', '3', major, 0, 0}, tsz[:]...)
		return append(out, frame...)
	}
	f.Add(textFrameTag(4))
	f.Add(textFrameTag(3))

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
		proj := Project(tg) // named tg, not tag - a tag var would shadow the tag package
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
