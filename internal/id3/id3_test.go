package id3

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestSizeErrHumanized (M2): the size-limit messages report humanized binary
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

func TestResolveGenres(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		numeric bool
	}{
		{"(17)", []string{"Rock"}, true},
		{"17", []string{"Rock"}, true},
		{"Rock", []string{"Rock"}, false},
		{"(51)(39)", []string{"Techno-Industrial", "Noise"}, true}, // multiple numeric references
		{"(17)Hardcore", []string{"Rock", "Hardcore"}, true},       // reference + refinement
		{"(Indie)Refined", []string{"Indie", "Refined"}, false},    // non-numeric parenthetical
		{"(RX)", []string{"Remix"}, true},
		{"(CR)", []string{"Cover"}, true},
		{"(255)", []string{"(255)"}, true}, // out of range, kept literal, still numeric syntax
		{"Custom Genre", []string{"Custom Genre"}, false},
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
	tg, err := ParseTag(data)
	if err != nil {
		t.Fatalf("ParseTag: %v", err)
	}
	return tg
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

func TestV22Upgrade(t *testing.T) {
	// Hand-build a v2.2 tag: "ID3" 02 00 00 <size>, then TT2 + size(3) + body.
	body := append([]byte{encLatin1}, "v2.2 Title"...)
	frame := append([]byte("TT2"), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	frame = append(frame, body...)
	var sz [4]byte
	putSyncSafe(sz[:], int64(len(frame)))
	data := append([]byte{'I', 'D', '3', 2, 0, 0}, sz[:]...)
	data = append(data, frame...)

	tg, err := ParseTag(data)
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

	out, _ := RebuildFrames(orig, base, edited, 4, nil, false, WriteOpts{})

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
	out, _ := RebuildFrames(orig, base, edited, 4, nil, false, WriteOpts{})
	if len(out) != 0 {
		t.Errorf("cleared title should drop the frame, got %+v", out)
	}
}

func TestDateDecompositionV23(t *testing.T) {
	// Round-trip a full date through v2.3 TYER/TDAT/TIME.
	base := tag.NewTagSet()
	edited := tag.NewTagSet()
	edited.Set(tag.RecordingDate, "2021-06-15T08:30")
	out, _ := RebuildFrames(nil, base, edited, 3, nil, false, WriteOpts{})

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

func TestNumericGenreWrite(t *testing.T) {
	base := tag.NewTagSet()
	edited := tag.NewTagSet()
	edited.Set(tag.Genre, "Rock")
	out, _ := RebuildFrames(nil, base, edited, 4, nil, false, WriteOpts{NumericGenre: true})
	if len(out) != 1 || out[0].ID != "TCON" {
		t.Fatalf("expected one TCON frame, got %+v", out)
	}
	if v := decodeTextFrame(out[0].Body)[0]; v != "17" {
		t.Errorf("numeric genre = %q, want 17", v)
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
	out, _ := RebuildFrames(nil, base, ts, 4, nil, false, WriteOpts{})
	if len(out) != 1 || out[0].ID != "TXXX" {
		t.Fatalf("expected one TXXX, got %+v", out)
	}
	desc, vals, _ := decodeUserText(out[0].Body)
	if desc != "MusicBrainz Album Id" || !slices.Equal(vals, []string{"abc-123"}) {
		t.Errorf("rewritten TXXX = desc %q vals %v", desc, vals)
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

	out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 4, nil, false, WriteOpts{})
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
	out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 3, nil, false, WriteOpts{})

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
	out, info := RebuildFrames(nil, tag.NewTagSet(), edited, 3, nil, false,
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
	out2, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 3, nil, false,
		WriteOpts{Multi: core.ID3MultiSlash})
	if len(out2) != 1 || decodeTextFrame(out2[0].Body)[0] != "A / B" {
		t.Errorf("slash-join = %+v", out2)
	}

	// Null-sep on v2.3 flags the compatibility impact.
	_, info3 := RebuildFrames(nil, tag.NewTagSet(), edited, 3, nil, false,
		WriteOpts{Multi: core.ID3MultiNullSep})
	if !info3.UsedV23Multi {
		t.Error("null-sep on v2.3 should report UsedV23Multi")
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
	out, _ := RebuildFrames(orig, base, edited, 4, nil, false, WriteOpts{})
	if got, _ := Project(buildTag(t, 4, out)).Tags.Get(tag.ReleaseDate); !slices.Equal(got, []string{"2022"}) {
		t.Errorf("v2.4 ReleaseDate after edit = %v, want [2022] (no duplicate/stale)", got)
	}

	// v2.3: a stray TDRC must not shadow a RecordingDate edit (dateParts.emit
	// would otherwise prefer the stale TDRC and lose the new TYER value).
	orig2 := []Frame{{ID: "TDRC", Body: encodeTextFrame(encLatin1, []string{"2000-01-01"})}}
	base2 := Project(&Tag{frames: orig2}).Tags
	edited2 := base2.Clone()
	edited2.Set(tag.RecordingDate, "2022")
	out2, _ := RebuildFrames(orig2, base2, edited2, 3, nil, false, WriteOpts{})
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
		out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 4, nil, false, WriteOpts{})
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

	tg, err := ParseTag(data)
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

// TestHugeFrameSizeNoPanic guards the 32-bit overflow: a v2.3 frame header
// declaring size 0xFFFFFFFF must be rejected (the scan stops) rather than
// wrapping to a negative length and panicking on the slice.
func TestHugeFrameSizeNoPanic(t *testing.T) {
	frame := append([]byte("TIT2"), 0xFF, 0xFF, 0xFF, 0xFF, 0, 0) // size = 4294967295
	var sz [4]byte
	putSyncSafe(sz[:], int64(len(frame)+4)) // claim a bit more than present
	data := append([]byte{'I', 'D', '3', 3, 0, 0}, sz[:]...)
	data = append(data, frame...)

	tg, err := ParseTag(data) // must not panic on any platform
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
			out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 4, nil, false, WriteOpts{})
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

// TestRenderNumTotalPathologicalResidual pins the ID3 behavior for a malformed
// number pair the editor deliberately leaves unsplit. The writer renders
// "1/2/3" verbatim instead of composing another slash, and a re-read sees number
// "1" with total "2/3". The input is already flagged malformed at set time, so
// the typed projection still matches MP4.
func TestRenderNumTotalPathologicalResidual(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Set(tag.TrackNumber, "1/2/3")
	out, _ := RebuildFrames(nil, tag.NewTagSet(), edited, 4, nil, false, WriteOpts{})
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
	if n, _ := ts.First(tag.TrackNumber); n != "1" {
		t.Errorf("re-read TrackNumber = %q, want 1", n)
	}
	if tot, _ := ts.First(tag.TrackTotal); tot != "2/3" {
		t.Errorf("re-read TrackTotal = %q, want 2/3", tot)
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
	out, _ := RebuildFrames(orig, base, edited, 4, nil, false, WriteOpts{})
	if got := lang(find(out, "COMM")); got != "deu" {
		t.Errorf("edited COMM language = %q, want deu", got)
	}
	if got := lang(find(out, "USLT")); got != "deu" {
		t.Errorf("edited USLT language = %q, want deu", got)
	}

	// A brand-new comment (no original frame) defaults to eng.
	fresh := tag.NewTagSet()
	fresh.Set(tag.Comment, "Added")
	outNew, _ := RebuildFrames(nil, tag.NewTagSet(), fresh, 4, nil, false, WriteOpts{})
	if got := lang(find(outNew, "COMM")); got != "eng" {
		t.Errorf("new COMM language = %q, want eng", got)
	}

	// A garbage (non-ASCII) 3-byte language round-trips verbatim, not normalized.
	garbage := string([]byte{0x01, 0x02, 0x03})
	origG := []Frame{{ID: "COMM", Body: encodeComment(4, garbage, "", []string{"x"})}}
	baseG := Project(&Tag{frames: origG}).Tags
	editedG := baseG.Clone()
	editedG.Set(tag.Comment, "y")
	outG, _ := RebuildFrames(origG, baseG, editedG, 4, nil, false, WriteOpts{})
	if got := lang(find(outG, "COMM")); got != garbage {
		t.Errorf("edited COMM language = % x, want % x (garbage round-trips)", got, garbage)
	}
}
