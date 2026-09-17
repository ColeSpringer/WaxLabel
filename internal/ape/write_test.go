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

// parseRendered: Render then ParseAt (only check the reader accepts).
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

// TestRenderHeaderAndFooterFlags: same version/size/count; size = items+footer.
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

// TestRebuildPreservesItemFlags: untouched items keep flags (preserve-unknown).
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

	got, _ := Rebuild(orig, base, edited, nil, false)
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

// TestRebuildKeepsSourceSpelling: keep existing names; new keys use convention.
func TestRebuildKeepsSourceSpelling(t *testing.T) {
	orig := []Item{{Key: "ALBUM ARTIST", Value: "Old"}}
	base := tag.NewTagSet()
	base.Add(tag.AlbumArtist, "Old")
	edited := tag.NewTagSet()
	edited.Add(tag.AlbumArtist, "New")
	edited.Add(tag.TrackNumber, "3")

	got, _ := Rebuild(orig, base, edited, nil, false)
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

// TestRebuildClearRemovesItem: cleared key writes no item.
func TestRebuildClearRemovesItem(t *testing.T) {
	orig := []Item{{Key: "Title", Value: "Gone"}, {Key: "Artist", Value: "Kept"}}
	base := tag.NewTagSet()
	base.Add(tag.Title, "Gone")
	base.Add(tag.Artist, "Kept")
	edited := tag.NewTagSet()
	edited.Add(tag.Artist, "Kept")

	got, _ := Rebuild(orig, base, edited, nil, false)
	if len(got) != 1 || got[0].Key != "Artist" {
		t.Errorf("items = %+v, want only Artist", got)
	}
}

// TestCoverRoundTrip: cover convention including NUL file name.
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

// TestProjectMalformedCoverWarns: warn; item survives for rewrite.
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

// TestRebuildPictures: covers rewrite only when picturesChanged.
func TestRebuildPictures(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")
	old := EncodeCover(core.Picture{Type: core.PicFrontCover, Data: []byte("old-bytes")})
	orig := []Item{old, {Key: "Title", Value: "T"}}
	ts := tag.NewTagSet()
	ts.Add(tag.Title, "T")

	unchanged, _ := Rebuild(orig, ts, ts, nil, false)
	if !bytes.Equal(unchanged[0].Data, old.Data) {
		t.Error("a tag-only edit re-encoded the cover item")
	}

	replaced, _ := Rebuild(orig, ts, ts, []core.Picture{{Type: core.PicFrontCover, Data: png}}, true)
	if len(replaced) != 2 {
		t.Fatalf("items = %+v, want the cover replaced in place", replaced)
	}
	got, err := DecodeCover(replaced[0].Key, replaced[0].Data)
	if err != nil || !bytes.Equal(got.Data, png) {
		t.Errorf("replaced cover = %+v (err %v), want the new bytes", got, err)
	}

	cleared, _ := Rebuild(orig, ts, ts, nil, true)
	if len(cleared) != 1 || cleared[0].Key != "Title" {
		t.Errorf("items after clearing pictures = %+v, want only Title", cleared)
	}
}

// TestProjectNumberPairSplit: slashed track -> same canonical pair as text codecs.
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

// TestRebuildSlashPair: edit either half drops slash; mark both emitted.
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
		// Untouched slash stays; no stray total.
		{"unrelated edit", func(ts *tag.TagSet) { ts.Set(tag.Title, "New") }, []string{"Track=3/12", "Title=New"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, _ := Rebuild(orig, base, edit(c.mut), nil, false)
			if !slices.Equal(names(got), c.want) {
				t.Errorf("items = %v, want %v", names(got), c.want)
			}
		})
	}
}

// TestRebuildEditedItemKeepsFlags: rewritten items keep flag word.
func TestRebuildEditedItemKeepsFlags(t *testing.T) {
	orig := []Item{{Key: "Title", Value: "Old", Flags: flagReadOnly | 1<<20}}
	base := tag.NewTagSet()
	base.Add(tag.Title, "Old")
	edited := tag.NewTagSet()
	edited.Add(tag.Title, "New")

	got, _ := Rebuild(orig, base, edited, nil, false)
	if len(got) != 1 || got[0].Value != "New" {
		t.Fatalf("items = %+v, want the value rewritten", got)
	}
	if got[0].Flags != orig[0].Flags {
		t.Errorf("flags = %#x, want %#x preserved across the rewrite", got[0].Flags, orig[0].Flags)
	}
}

// TestRebuildKeepsMalformedCover: undecodable back survives front picture edit.
func TestRebuildKeepsMalformedCover(t *testing.T) {
	bad := Item{Key: coverBackKey, Data: []byte("no-nul-terminator"), Flags: itemTypeBinary << itemTypeShift}
	ts := tag.NewTagSet()
	got, info := Rebuild([]Item{bad}, ts, ts, []core.Picture{{Type: core.PicFrontCover, Data: tinyPNGBytes()}}, true)
	if len(got) != 2 {
		t.Fatalf("items = %d, want the new cover plus the undecodable one", len(got))
	}
	if !bytes.Equal(got[1].Data, bad.Data) {
		t.Errorf("undecodable cover = %q, want its bytes preserved", got[1].Data)
	}
	if ws := RebuildWarnings(nil, info); len(ws) != 0 {
		t.Errorf("warnings = %+v, want none for a collision-free preserve", ws)
	}
}

// TestRebuildCoverSlots: one front + one back; exact roles claim own slot; spill/drop.
func TestRebuildCoverSlots(t *testing.T) {
	ts := tag.NewTagSet()
	pic := func(pt core.PictureType, payload string) core.Picture {
		return core.Picture{Type: pt, Data: []byte(payload)}
	}
	for _, c := range []struct {
		name        string
		pics        []core.Picture
		wantKeys    []string
		wantData    []string // image bytes per written item, matching wantKeys
		wantDropped []core.PictureType
	}{
		{"front then artist spills to back", []core.Picture{pic(core.PicFrontCover, "F"), pic(core.PicArtist, "A")},
			[]string{coverFrontKey, coverBackKey}, []string{"F", "A"}, nil},
		{"artist then front", []core.Picture{pic(core.PicArtist, "A"), pic(core.PicFrontCover, "F")},
			[]string{coverBackKey, coverFrontKey}, []string{"A", "F"}, nil},
		{"lone artist takes the front", []core.Picture{pic(core.PicArtist, "A")},
			[]string{coverFrontKey}, []string{"A"}, nil},
		{"artist and back both fit", []core.Picture{pic(core.PicArtist, "A"), pic(core.PicBackCover, "B")},
			[]string{coverFrontKey, coverBackKey}, []string{"A", "B"}, nil},
		{"two spills fill both slots", []core.Picture{pic(core.PicArtist, "A"), pic(core.PicComposer, "C")},
			[]string{coverFrontKey, coverBackKey}, []string{"A", "C"}, nil},
		{"third spill has no slot", []core.Picture{pic(core.PicFrontCover, "F"), pic(core.PicArtist, "A"), pic(core.PicBand, "B")},
			[]string{coverFrontKey, coverBackKey}, []string{"F", "A"}, []core.PictureType{core.PicBand}},
		{"a losing front never fakes a back", []core.Picture{pic(core.PicFrontCover, "F1"), pic(core.PicFrontCover, "F2")},
			[]string{coverFrontKey}, []string{"F1"}, []core.PictureType{core.PicFrontCover}},
		{"two backs keep the first", []core.Picture{pic(core.PicBackCover, "B1"), pic(core.PicBackCover, "B2"), pic(core.PicFrontCover, "F")},
			[]string{coverBackKey, coverFrontKey}, []string{"B1", "F"}, []core.PictureType{core.PicBackCover}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, info := Rebuild(nil, ts, ts, c.pics, true)
			if len(got) != len(c.wantKeys) {
				t.Fatalf("items = %+v, want %d covers %v", got, len(c.wantKeys), c.wantKeys)
			}
			for i, it := range got {
				if it.Key != c.wantKeys[i] {
					t.Errorf("item %d name = %q, want %q", i, it.Key, c.wantKeys[i])
				}
				p, err := DecodeCover(it.Key, it.Data)
				if err != nil {
					t.Fatalf("DecodeCover(%q): %v", it.Key, err)
				}
				if string(p.Data) != c.wantData[i] {
					t.Errorf("item %d image = %q, want %q", i, p.Data, c.wantData[i])
				}
			}
			if !slices.Equal(info.SlotDroppedCovers, c.wantDropped) {
				t.Errorf("SlotDroppedCovers = %v, want %v", info.SlotDroppedCovers, c.wantDropped)
			}
			ws := RebuildWarnings(nil, info)
			if len(ws) != len(c.wantDropped) {
				t.Fatalf("warnings = %+v, want one per dropped picture (%d)", ws, len(c.wantDropped))
			}
			for _, w := range ws {
				if w.Code != core.WarnPictureUnsupported {
					t.Errorf("warning code = %v, want WarnPictureUnsupported", w.Code)
				}
			}
		})
	}
}

// TestRebuildReplacesNonTextItemOnAuthoredName: authored text displaces same-name non-text.
func TestRebuildReplacesNonTextItemOnAuthoredName(t *testing.T) {
	for _, spelling := range []string{"Title", "TITLE"} { // readers compare names case-insensitively
		t.Run(spelling, func(t *testing.T) {
			orig := []Item{{Key: spelling, Data: []byte{1, 2, 3}, Flags: itemTypeBinary << itemTypeShift}}
			base := tag.NewTagSet()
			edited := tag.NewTagSet()
			edited.Add(tag.Title, "New")

			items, info := Rebuild(orig, base, edited, nil, false)
			if len(items) != 1 || items[0].NonText() || items[0].Value != "New" {
				t.Fatalf("items = %+v, want only the new text Title", items)
			}
			if !slices.Equal(info.NonTextReplaced, []tag.Key{tag.Title}) {
				t.Errorf("NonTextReplaced = %v, want [TITLE]", info.NonTextReplaced)
			}
			ws := RebuildWarnings(nil, info)
			if len(ws) != 1 || ws[0].Code != core.WarnTagStructureDropped || !slices.Contains(ws[0].Keys, tag.Title) {
				t.Errorf("warnings = %+v, want one tag-structure-dropped keyed TITLE", ws)
			}
		})
	}
}

// TestRebuildKeepsPreexistingNameCollision: source name collision preserved.
func TestRebuildKeepsPreexistingNameCollision(t *testing.T) {
	orig := []Item{
		{Key: "Title", Value: "Old"},
		{Key: "Title", Data: []byte{9}, Flags: itemTypeBinary << itemTypeShift},
	}
	base := tag.NewTagSet()
	base.Add(tag.Title, "Old")
	edited := base.Clone()
	edited.Set(tag.Title, "New")

	items, info := Rebuild(orig, base, edited, nil, false)
	if len(items) != 2 || items[0].Value != "New" || !items[1].NonText() {
		t.Fatalf("items = %+v, want the rewritten text item plus the preserved binary one", items)
	}
	if len(info.NonTextReplaced) != 0 {
		t.Errorf("NonTextReplaced = %v, want none for a pre-existing collision", info.NonTextReplaced)
	}
}

// TestRebuildRefusesCoverNameTextItem: text under Cover Art name refused; prior kept.
func TestRebuildRefusesCoverNameTextItem(t *testing.T) {
	base := tag.NewTagSet()
	edited := tag.NewTagSet()
	key := tag.Key("COVER ART (FRONT)")
	edited.Add(key, "x")

	items, info := Rebuild(nil, base, edited, nil, false)
	if len(items) != 0 {
		t.Fatalf("items = %+v, want none: a text cover-name item must not be authored", items)
	}
	if !slices.Equal(info.CoverNameKeys, []tag.Key{key}) {
		t.Errorf("CoverNameKeys = %v, want [%s]", info.CoverNameKeys, key)
	}
	ws := RebuildWarnings(nil, info)
	if len(ws) != 1 || ws[0].Code != core.WarnValueDropped || !slices.Contains(ws[0].Keys, key) {
		t.Errorf("warnings = %+v, want one value-dropped keyed %s", ws, key)
	}

	// Prior text under Cover Art name kept on refused set.
	orig := []Item{{Key: "Cover Art (Front)", Value: "old"}}
	preserved := tag.NewTagSet()
	preserved.Add(key, "old")
	changed := preserved.Clone()
	changed.Set(key, "new")
	items, _ = Rebuild(orig, preserved, changed, nil, false)
	if len(items) != 1 || items[0].Value != "old" {
		t.Errorf("items = %+v, want the original text item preserved on a refused set", items)
	}
}

// TestRebuildPictureEditReplacesCoverNameTextItem: cover write displaces squatting text.
func TestRebuildPictureEditReplacesCoverNameTextItem(t *testing.T) {
	orig := []Item{{Key: "Cover Art (Front)", Value: "junk-text"}}
	base := Project(&Tag{Items: orig}).Tags
	edited := base.Clone()

	items, info := Rebuild(orig, base, edited, []core.Picture{{Type: core.PicFrontCover, Data: tinyPNGBytes()}}, true)
	if len(items) != 1 || !items[0].NonText() || items[0].Key != coverFrontKey {
		t.Fatalf("items = %+v, want only the written cover item", items)
	}
	if !slices.Equal(info.CoverTextReplaced, []tag.Key{"COVER ART (FRONT)"}) {
		t.Errorf("CoverTextReplaced = %v, want the squatting key", info.CoverTextReplaced)
	}
	ws := RebuildWarnings(nil, info)
	if len(ws) != 1 || ws[0].Code != core.WarnValueDropped {
		t.Errorf("warnings = %+v, want one value-dropped", ws)
	}

	items, info = Rebuild(orig, base, edited, []core.Picture{{Type: core.PicBackCover, Data: tinyPNGBytes()}}, true)
	if len(items) != 2 {
		t.Fatalf("items = %+v, want the back cover plus the untouched front-name text item", items)
	}
	if len(info.CoverTextReplaced) != 0 {
		t.Errorf("CoverTextReplaced = %v, want none when the name is not written", info.CoverTextReplaced)
	}
}

// TestRebuildReplacesMalformedCoverOnNameCollision: cover write replaces junk same name.
func TestRebuildReplacesMalformedCoverOnNameCollision(t *testing.T) {
	bad := Item{Key: "COVER ART (FRONT)", Data: []byte("no-nul-terminator"), Flags: itemTypeBinary << itemTypeShift}
	ts := tag.NewTagSet()
	got, info := Rebuild([]Item{bad}, ts, ts, []core.Picture{{Type: core.PicFrontCover, Data: tinyPNGBytes()}}, true)
	if len(got) != 1 || got[0].Key != coverFrontKey {
		t.Fatalf("items = %+v, want a single front-cover item", got)
	}
	p, err := DecodeCover(got[0].Key, got[0].Data)
	if err != nil || !bytes.Equal(p.Data, tinyPNGBytes()) {
		t.Errorf("written cover = %+v (err %v), want the new image bytes", p, err)
	}
	if !slices.Equal(info.MalformedCoversReplaced, []string{"COVER ART (FRONT)"}) {
		t.Errorf("MalformedCoversReplaced = %v, want the original item name", info.MalformedCoversReplaced)
	}
	ws := RebuildWarnings(nil, info)
	if len(ws) != 1 || ws[0].Code != core.WarnMalformedTagEntryDropped {
		t.Errorf("warnings = %+v, want one malformed-tag-entry-dropped warning", ws)
	}
}

// TestRebuildSpillAvoidsMalformedSlot: spill prefers free over junk-held slot.
func TestRebuildSpillAvoidsMalformedSlot(t *testing.T) {
	badFront := Item{Key: coverFrontKey, Data: []byte("no-nul"), Flags: itemTypeBinary << itemTypeShift}
	badBack := Item{Key: coverBackKey, Data: []byte("no-nul"), Flags: itemTypeBinary << itemTypeShift}
	ts := tag.NewTagSet()
	artist := []core.Picture{{Type: core.PicArtist, Data: tinyPNGBytes()}}

	got, info := Rebuild([]Item{badFront}, ts, ts, artist, true)
	if len(got) != 2 || got[0].Key != coverBackKey || !bytes.Equal(got[1].Data, badFront.Data) {
		t.Fatalf("items = %+v, want the artist written as the back cover and the junk front preserved", got)
	}
	if len(info.MalformedCoversReplaced) != 0 || len(info.SlotDroppedCovers) != 0 {
		t.Errorf("info = %+v, want no replacement and no drop", info)
	}

	got, info = Rebuild([]Item{badFront, badBack}, ts, ts, artist, true)
	if len(got) != 2 || got[0].Key != coverFrontKey || !bytes.Equal(got[1].Data, badBack.Data) {
		t.Fatalf("items = %+v, want the artist written as the front cover and the junk back preserved", got)
	}
	if !slices.Equal(info.MalformedCoversReplaced, []string{coverFrontKey}) {
		t.Errorf("MalformedCoversReplaced = %v, want the displaced front", info.MalformedCoversReplaced)
	}
}

// TestPartitionCoverSlotsAddedPriority: added beats pre-existing; else earlier wins.
func TestPartitionCoverSlotsAddedPriority(t *testing.T) {
	pics := []core.Picture{
		{Type: core.PicFrontCover, Data: []byte("old")},
		{Type: core.PicFrontCover, Data: []byte("new")},
	}
	kept, dropped := PartitionCoverSlots(pics, []bool{false, true})
	if !slices.Equal(kept, []int{1}) || !slices.Equal(dropped, []int{0}) {
		t.Errorf("added-aware partition kept %v dropped %v, want the added picture kept", kept, dropped)
	}
	kept, dropped = PartitionCoverSlots(pics, nil)
	if !slices.Equal(kept, []int{0}) || !slices.Equal(dropped, []int{1}) {
		t.Errorf("plain partition kept %v dropped %v, want the first picture kept", kept, dropped)
	}
}

// TestRenderRejectsOversizedTag: refuse tags past reader size cap.
func TestRenderRejectsOversizedTag(t *testing.T) {
	huge := Item{Key: coverFrontKey, Data: make([]byte, maxTagBytes+1), Flags: itemTypeBinary << itemTypeShift}
	if _, err := Render([]Item{huge}, writeVersion, true); !errors.Is(err, waxerr.ErrPictureTooLarge) {
		t.Errorf("err = %v, want ErrPictureTooLarge", err)
	}
}

// TestRenderPreservesVersionAndShape: keep APEv1 and footer-only shape.
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

// TestParseAtRejectsUnbackedHeaderFlag: has-header bit needs real APETAGEX.
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

// TestParseTruncatedItemList: Truncated set when element cap cuts Items.
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

// TestDecodeTextLatin1Fallback: non-UTF-8 -> Latin-1; raw bytes survive untouched rewrite.
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
	got, _ := Rebuild(tg.Items, base, edited, nil, false)
	if !bytes.Equal(got[0].Data, latin1) {
		t.Errorf("untouched item = %q, want its original bytes", got[0].Data)
	}
}

// tinyPNGBytes: minimal PNG for sniff.
func tinyPNGBytes() []byte {
	return []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")
}

// TestRebuildEmptyValueRoundTrips: [""] writes empty item; nil slice clears.
// Shared APEv2 writer covers WavPack/MAC/Musepack.
func TestRebuildEmptyValueRoundTrips(t *testing.T) {
	orig := []Item{{Key: "Title", Value: "Old"}}
	base := tag.NewTagSet()
	base.Add(tag.Title, "Old")

	edited := tag.NewTagSet()
	edited.Add(tag.Title, "")
	items, _ := Rebuild(orig, base, edited, nil, false)
	tg := parseRendered(t, items)
	if len(tg.Items) != 1 || tg.Items[0].Value != "" {
		t.Fatalf("items = %+v, want one empty-valued Title", tg.Items)
	}
	if vals, ok := Project(tg).Tags.Get(tag.Title); !ok || len(vals) != 1 || vals[0] != "" {
		t.Errorf("projected TITLE = %q ok=%v, want one empty value", vals, ok)
	}

	cleared := tag.NewTagSet()
	if got, _ := Rebuild(orig, base, cleared, nil, false); len(got) != 0 {
		t.Errorf("a cleared key should emit no item, got %+v", got)
	}
}

// TestRebuildDropsEmptyWithinMultiValue: empty within multi dropped (report==write).
func TestRebuildDropsEmptyWithinMultiValue(t *testing.T) {
	base := tag.NewTagSet()
	for _, c := range []struct {
		name string
		vals []string
		want string // "" means: no item at all
	}{
		{"all empty", []string{"", ""}, ""},
		{"one empty", []string{"A", ""}, "A"},
		{"leading empty", []string{"", "B"}, "B"},
		{"both present", []string{"A", "B"}, "A\x00B"},
	} {
		t.Run(c.name, func(t *testing.T) {
			edited := tag.NewTagSet()
			for _, v := range c.vals {
				edited.Add(tag.Artist, v)
			}
			got, _ := Rebuild(nil, base, edited, nil, false)
			if c.want == "" {
				if len(got) != 0 {
					t.Fatalf("items = %+v, want none: every value was empty", got)
				}
				return
			}
			if len(got) != 1 || got[0].Value != c.want {
				t.Fatalf("items = %+v, want one item valued %q", got, c.want)
			}
			// Round-trip value set.
			tg := parseRendered(t, got)
			vals, _ := Project(tg).Tags.Get(tag.Artist)
			if want := splitItemValues(c.want); !slices.Equal(vals, want) {
				t.Errorf("read back %q, want %q", vals, want)
			}
		})
	}
}
