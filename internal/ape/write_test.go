package ape

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// parseRendered renders items and reads them back through the real parser, which is
// the only way to know Render emitted a tag the reader accepts.
func parseRendered(t *testing.T, items []Item) *Tag {
	t.Helper()
	raw, err := Render(items, writeVersion, true)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	tg, ok, err := ParseAt(core.BytesSource(raw), int64(len(raw)), 1<<20, 1000)
	if err != nil || !ok {
		t.Fatalf("ParseAt on rendered tag: ok=%v err=%v", ok, err)
	}
	if tg.Offset != 0 || tg.Size != int64(len(raw)) {
		t.Fatalf("extent = offset %d size %d, want 0 and %d (header included)", tg.Offset, tg.Size, len(raw))
	}
	return tg
}

func TestRenderRoundTrip(t *testing.T) {
	items := []Item{
		{Key: "Title", Value: "Hello"},
		{Key: "Artist", Value: "First\x00Second"},
	}
	tg := parseRendered(t, items)
	if tg.Version != writeVersion {
		t.Errorf("version = %d, want %d", tg.Version, writeVersion)
	}
	if !slices.EqualFunc(tg.Items, items, func(a, b Item) bool { return a.Key == b.Key && a.Value == b.Value }) {
		t.Errorf("items = %+v, want %+v", tg.Items, items)
	}
	pr := Project(tg)
	if v, _ := pr.Tags.Get(tag.Artist); !slices.Equal(v, []string{"First", "Second"}) {
		t.Errorf("artists = %v, want the NUL-separated pair", v)
	}
}

// TestRenderHeaderAndFooterFlags pins the record layout: a header and a footer with
// the same version, size, and count, differing only in the header marker, and a size
// field that counts the items plus the footer (what a reader scanning back from the
// end of a file needs) rather than the whole tag.
func TestRenderHeaderAndFooterFlags(t *testing.T) {
	raw, err := Render([]Item{{Key: "Title", Value: "x"}}, writeVersion, true)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	head, foot := raw[:footerLen], raw[len(raw)-footerLen:]
	if string(head[0:8]) != preamble || string(foot[0:8]) != preamble {
		t.Fatal("missing APETAGEX preamble on the header or footer")
	}
	if got := binary.LittleEndian.Uint32(head[20:24]); got != flagHasHeader|flagIsHeader {
		t.Errorf("header flags = %#x, want has-header and is-header", got)
	}
	if got := binary.LittleEndian.Uint32(foot[20:24]); got != flagHasHeader {
		t.Errorf("footer flags = %#x, want has-header only", got)
	}
	if got, want := binary.LittleEndian.Uint32(head[12:16]), uint32(len(raw)-footerLen); got != want {
		t.Errorf("tag size = %d, want %d (items plus footer, header excluded)", got, want)
	}
	if !bytes.Equal(head[24:32], make([]byte, 8)) {
		t.Error("the reserved field must be zero")
	}
}

// TestRebuildPreservesItemFlags is the byte-level preserve-unknown contract: an item
// WaxLabel did not edit keeps its flags, including the read-only bit and the type
// bits. A rebuild that re-rendered every item from its decoded value would clear
// them silently.
func TestRebuildPreservesItemFlags(t *testing.T) {
	orig := []Item{
		{Key: "Title", Value: "Old"},
		{Key: "Locked", Value: "keep", Flags: flagReadOnly},
		{Key: "Blob", Data: []byte{9, 8, 7}, Flags: itemTypeBinary << itemTypeShift},
	}
	base := tag.NewTagSet()
	base.Add(tag.Title, "Old")
	base.Add(tag.Key("LOCKED"), "keep")
	edited := tag.NewTagSet()
	edited.Add(tag.Title, "New")
	edited.Add(tag.Key("LOCKED"), "keep")

	got := Rebuild(orig, base, edited, nil, false)
	tg := parseRendered(t, got)
	if len(tg.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(tg.Items))
	}
	if tg.Items[0].Value != "New" {
		t.Errorf("edited item = %q, want New", tg.Items[0].Value)
	}
	if !tg.Items[1].ReadOnly() {
		t.Error("the read-only bit was cleared on an untouched item")
	}
	if !tg.Items[2].NonText() || !bytes.Equal(tg.Items[2].Data, []byte{9, 8, 7}) {
		t.Errorf("binary item = %+v, want its bytes and type preserved", tg.Items[2])
	}
}

// TestRebuildKeepsSourceSpelling: an edit to a file another tagger wrote must not
// rename its items. New keys use the conventional APE spelling instead.
func TestRebuildKeepsSourceSpelling(t *testing.T) {
	orig := []Item{{Key: "ALBUM ARTIST", Value: "Old"}}
	base := tag.NewTagSet()
	base.Add(tag.AlbumArtist, "Old")
	edited := tag.NewTagSet()
	edited.Add(tag.AlbumArtist, "New")
	edited.Add(tag.TrackNumber, "3")

	got := Rebuild(orig, base, edited, nil, false)
	if len(got) != 2 {
		t.Fatalf("items = %+v, want two", got)
	}
	if got[0].Key != "ALBUM ARTIST" || got[0].Value != "New" {
		t.Errorf("edited item = %+v, want the source spelling kept", got[0])
	}
	if got[1].Key != "Track" {
		t.Errorf("added item name = %q, want the conventional %q", got[1].Key, "Track")
	}
}

// TestRebuildClearRemovesItem: a key cleared from the edited set writes no item,
// which is how a --clear removes it from the file.
func TestRebuildClearRemovesItem(t *testing.T) {
	orig := []Item{{Key: "Title", Value: "Gone"}, {Key: "Artist", Value: "Kept"}}
	base := tag.NewTagSet()
	base.Add(tag.Title, "Gone")
	base.Add(tag.Artist, "Kept")
	edited := tag.NewTagSet()
	edited.Add(tag.Artist, "Kept")

	got := Rebuild(orig, base, edited, nil, false)
	if len(got) != 1 || got[0].Key != "Artist" {
		t.Errorf("items = %+v, want only Artist", got)
	}
}

// TestCoverRoundTrip covers the cover-art convention end to end, including the
// NUL-terminated file name the payload begins with.
func TestCoverRoundTrip(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + "\x00\x00\x00\rIHDR\x00\x00\x00\x02\x00\x00\x00\x03\x08\x06\x00\x00\x00")
	for _, c := range []struct {
		typ  core.PictureType
		key  string
		file string
	}{
		{core.PicFrontCover, coverFrontKey, "cover_art_(front).png"},
		{core.PicBackCover, coverBackKey, "cover_art_(back).png"},
	} {
		it := EncodeCover(core.Picture{Type: c.typ, Data: png})
		if it.Key != c.key {
			t.Errorf("item name = %q, want %q", it.Key, c.key)
		}
		if !it.NonText() {
			t.Error("a cover item must be typed binary")
		}
		if name, _, ok := bytes.Cut(it.Data, []byte{0}); !ok || string(name) != c.file {
			t.Errorf("stored file name = %q, want %q", name, c.file)
		}
		got, err := DecodeCover(it.Key, it.Data)
		if err != nil {
			t.Fatalf("DecodeCover: %v", err)
		}
		if got.Type != c.typ || got.MIME != "image/png" || !bytes.Equal(got.Data, png) {
			t.Errorf("decoded = %+v, want type %v and the PNG bytes back", got, c.typ)
		}
		if got.Width != 2 || got.Height != 3 {
			t.Errorf("geometry = %dx%d, want 2x3 sniffed from the header", got.Width, got.Height)
		}
	}
}

func TestDecodeCoverMalformed(t *testing.T) {
	if _, err := DecodeCover(coverFrontKey, []byte("no-terminator")); err == nil {
		t.Error("a payload with no NUL file-name terminator must be rejected")
	}
	if _, err := DecodeCover(coverFrontKey, []byte("name\x00")); err == nil {
		t.Error("a payload with no image bytes must be rejected")
	}
}

// TestProjectMalformedCoverWarns: a bad cover is surfaced, not silently dropped, and
// its item survives for the rewrite to preserve.
func TestProjectMalformedCoverWarns(t *testing.T) {
	tg := &Tag{Items: []Item{{Key: coverFrontKey, Data: []byte("junk"), Flags: itemTypeBinary << itemTypeShift}}}
	pr := Project(tg)
	if len(pr.Pictures) != 0 {
		t.Errorf("pictures = %+v, want none decoded", pr.Pictures)
	}
	if len(pr.Warnings) != 1 || pr.Warnings[0].Code != core.WarnInvalidPicture {
		t.Errorf("warnings = %+v, want one invalid-picture warning", pr.Warnings)
	}
}

// TestRebuildPictures replaces the cover set only on a picture edit, so a tag-only
// edit does not re-encode an untouched cover.
func TestRebuildPictures(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")
	old := EncodeCover(core.Picture{Type: core.PicFrontCover, Data: []byte("old-bytes")})
	orig := []Item{old, {Key: "Title", Value: "T"}}
	ts := tag.NewTagSet()
	ts.Add(tag.Title, "T")

	unchanged := Rebuild(orig, ts, ts, nil, false)
	if !bytes.Equal(unchanged[0].Data, old.Data) {
		t.Error("a tag-only edit re-encoded the cover item")
	}

	replaced := Rebuild(orig, ts, ts, []core.Picture{{Type: core.PicFrontCover, Data: png}}, true)
	if len(replaced) != 2 {
		t.Fatalf("items = %+v, want the cover replaced in place", replaced)
	}
	got, err := DecodeCover(replaced[0].Key, replaced[0].Data)
	if err != nil || !bytes.Equal(got.Data, png) {
		t.Errorf("replaced cover = %+v (err %v), want the new bytes", got, err)
	}

	cleared := Rebuild(orig, ts, ts, nil, true)
	if len(cleared) != 1 || cleared[0].Key != "Title" {
		t.Errorf("items after clearing pictures = %+v, want only Title", cleared)
	}
}

// TestProjectNumberPairSplit: APE stores a slashed track the way the text codecs do,
// so it must read back as the same canonical pair.
func TestProjectNumberPairSplit(t *testing.T) {
	tg := &Tag{Items: []Item{{Key: "Track", Value: "4/9"}}}
	pr := Project(tg)
	if v, _ := pr.Tags.Get(tag.TrackNumber); !slices.Equal(v, []string{"4"}) {
		t.Errorf("TrackNumber = %v, want [4]", v)
	}
	if v, _ := pr.Tags.Get(tag.TrackTotal); !slices.Equal(v, []string{"9"}) {
		t.Errorf("TrackTotal = %v, want [9]", v)
	}
}

func TestProjectNilTag(t *testing.T) {
	pr := Project(nil)
	if pr.Tags.Len() != 0 || len(pr.Pictures) != 0 || len(pr.Families) != 0 {
		t.Errorf("nil projection = %+v, want empty", pr)
	}
}

// TestRebuildSlashPair pins the number/total convention: an APE "Track" item holds both
// halves, so an edit to either must rewrite the number without the slash - otherwise a
// cleared total resurfaces when the preserved "3/12" is re-projected, and an unrelated
// edit appends a total item the file never had.
func TestRebuildSlashPair(t *testing.T) {
	orig := []Item{{Key: "Track", Value: "3/12"}, {Key: "Title", Value: "T"}}
	base := Project(&Tag{Items: orig}).Tags

	edit := func(mut func(*tag.TagSet)) tag.TagSet {
		ts := base.Clone()
		mut(&ts)
		return ts
	}
	names := func(items []Item) []string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.Key + "=" + it.Value
		}
		return out
	}

	for _, c := range []struct {
		name string
		mut  func(*tag.TagSet)
		want []string
	}{
		{"set the total", func(ts *tag.TagSet) { ts.Set(tag.TrackTotal, "20") }, []string{"Track=3", "TRACKTOTAL=20", "Title=T"}},
		{"clear the total", func(ts *tag.TagSet) { ts.Delete(tag.TrackTotal) }, []string{"Track=3", "Title=T"}},
		{"set the number", func(ts *tag.TagSet) { ts.Set(tag.TrackNumber, "5") }, []string{"Track=5", "TRACKTOTAL=12", "Title=T"}},
		// An edit that touches neither half leaves the slash alone and adds nothing.
		{"unrelated edit", func(ts *tag.TagSet) { ts.Set(tag.Title, "New") }, []string{"Track=3/12", "Title=New"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Rebuild(orig, base, edit(c.mut), nil, false)
			if !slices.Equal(names(got), c.want) {
				t.Errorf("items = %v, want %v", names(got), c.want)
			}
		})
	}
}

// TestRebuildEditedItemKeepsFlags: an item the edit REWRITES must keep its flag word,
// not just an untouched one. The read-only bit and every undefined bit survive.
func TestRebuildEditedItemKeepsFlags(t *testing.T) {
	orig := []Item{{Key: "Title", Value: "Old", Flags: flagReadOnly | 1<<20}}
	base := tag.NewTagSet()
	base.Add(tag.Title, "Old")
	edited := tag.NewTagSet()
	edited.Add(tag.Title, "New")

	got := Rebuild(orig, base, edited, nil, false)
	if len(got) != 1 || got[0].Value != "New" {
		t.Fatalf("items = %+v, want the value rewritten", got)
	}
	if got[0].Flags != orig[0].Flags {
		t.Errorf("flags = %#x, want %#x preserved across the rewrite", got[0].Flags, orig[0].Flags)
	}
}

// TestRebuildKeepsMalformedCover: a picture edit re-emits the decoded cover set, which
// by definition excludes a cover whose payload did not decode. Dropping it would destroy
// bytes the read path reported as preserved.
func TestRebuildKeepsMalformedCover(t *testing.T) {
	bad := Item{Key: coverFrontKey, Data: []byte("no-nul-terminator"), Flags: itemTypeBinary << itemTypeShift}
	ts := tag.NewTagSet()
	got := Rebuild([]Item{bad}, ts, ts, []core.Picture{{Type: core.PicFrontCover, Data: tinyPNGBytes()}}, true)
	if len(got) != 2 {
		t.Fatalf("items = %d, want the new cover plus the undecodable one", len(got))
	}
	if !bytes.Equal(got[1].Data, bad.Data) {
		t.Errorf("undecodable cover = %q, want its bytes preserved", got[1].Data)
	}
}

// TestRenderRejectsOversizedTag: a tag past the size readers accept is refused, rather
// than written as one they drop whole - taking the title and artist with it.
func TestRenderRejectsOversizedTag(t *testing.T) {
	huge := Item{Key: coverFrontKey, Data: make([]byte, maxTagBytes+1), Flags: itemTypeBinary << itemTypeShift}
	if _, err := Render([]Item{huge}, writeVersion, true); !errors.Is(err, waxerr.ErrPictureTooLarge) {
		t.Errorf("err = %v, want ErrPictureTooLarge", err)
	}
}

// TestRenderPreservesVersionAndShape: an APEv1 tag is not relabelled APEv2 (whose UTF-8
// requirement its preserved bytes may not meet) and a footer-only tag stays footer-only.
func TestRenderPreservesVersionAndShape(t *testing.T) {
	raw, err := Render([]Item{{Key: "Title", Value: "x"}}, 1000, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(raw, []byte(preamble)) {
		t.Error("a footer-only tag must not gain a header record")
	}
	tg, ok, err := ParseAt(core.BytesSource(raw), int64(len(raw)), 1<<20, 1000)
	if err != nil || !ok {
		t.Fatalf("ParseAt: ok=%v err=%v", ok, err)
	}
	if tg.Version != 1000 || tg.HasHeader {
		t.Errorf("read back version %d hasHeader=%v, want 1000 and false", tg.Version, tg.HasHeader)
	}
}

// TestParseAtRejectsUnbackedHeaderFlag is the audio-safety case: the has-header bit
// decides where the tag starts, which for the codecs that own an APE tag is where the
// verbatim audio copy ends. A file that merely sets the bit must not move that boundary
// back into the audio.
func TestParseAtRejectsUnbackedHeaderFlag(t *testing.T) {
	raw, err := Render([]Item{{Key: "Title", Value: "x"}}, writeVersion, false)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(raw[len(raw)-12:len(raw)-8], flagHasHeader) // claim a header

	audio := []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	file := append(slices.Clone(audio), raw...)
	tg, ok, err := ParseAt(core.BytesSource(file), int64(len(file)), 1<<20, 1000)
	if err != nil || !ok {
		t.Fatalf("ParseAt: ok=%v err=%v", ok, err)
	}
	if tg.Offset != int64(len(audio)) {
		t.Errorf("tag offset = %d, want %d: an unbacked header flag must not move the start into the audio",
			tg.Offset, len(audio))
	}
	if tg.HasHeader {
		t.Error("HasHeader should be false when no header record is actually present")
	}
}

// TestParseTruncatedItemList records the element cap so a codec rebuilding the whole tag
// from Items can refuse rather than delete what it never read.
func TestParseTruncatedItemList(t *testing.T) {
	raw, err := Render([]Item{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}, {Key: "C", Value: "3"}}, writeVersion, true)
	if err != nil {
		t.Fatal(err)
	}
	tg, ok, err := ParseAt(core.BytesSource(raw), int64(len(raw)), 1<<20, 2)
	if err != nil || !ok {
		t.Fatalf("ParseAt: ok=%v err=%v", ok, err)
	}
	if len(tg.Items) != 2 || !tg.Truncated {
		t.Errorf("items = %d truncated = %v, want 2 and true", len(tg.Items), tg.Truncated)
	}
}

// TestDecodeTextLatin1Fallback: an item whose bytes are not valid UTF-8 (APEv1's code
// page, and out-of-spec APEv2 items) yields a usable value, and its raw bytes survive a
// rewrite that does not touch it.
func TestDecodeTextLatin1Fallback(t *testing.T) {
	latin1 := []byte("Bj\xf8rk")
	tg := &Tag{Items: []Item{{Key: "Artist", Data: latin1, Value: decodeText(latin1)}, {Key: "Title", Value: "T"}}}
	if v, _ := Project(tg).Tags.Get(tag.Artist); len(v) != 1 || v[0] != "Bjørk" {
		t.Errorf("ARTIST = %v, want the Latin-1 bytes decoded", v)
	}
	if ws := InvalidUTF8Warnings(tg); len(ws) != 1 || ws[0].Code != core.WarnInvalidText {
		t.Errorf("warnings = %+v, want one invalid-text warning", ws)
	}

	base := Project(tg).Tags
	edited := base.Clone()
	edited.Set(tag.Title, "New")
	got := Rebuild(tg.Items, base, edited, nil, false)
	if !bytes.Equal(got[0].Data, latin1) {
		t.Errorf("untouched item = %q, want its original bytes", got[0].Data)
	}
}

// tinyPNGBytes is a minimal PNG header, enough for the cover codec to sniff.
func tinyPNGBytes() []byte {
	return []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")
}
