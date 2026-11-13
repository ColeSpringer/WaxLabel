package ape

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// The two cover item names every tagger agrees on.
const (
	coverFrontKey = "Cover Art (Front)"
	coverBackKey  = "Cover Art (Back)"
)

// parseRendered: Render then ParseAt (only check the reader accepts).
func parseRendered(t *testing.T, items []Item) *Tag {
	t.Helper()
	raw, err := Render(items, WriteVersion, true, 0)
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
	if tg.Version != WriteVersion {
		t.Errorf("version = %d, want %d", tg.Version, WriteVersion)
	}
	if !slices.EqualFunc(tg.Items, items, func(a, b Item) bool { return a.Key == b.Key && a.Value == b.Value }) {
		t.Errorf("items = %+v, want %+v", tg.Items, items)
	}
	pr := Project(tg)
	if v, _ := pr.Tags.Get(tag.Artist); !slices.Equal(v, []string{"First", "Second"}) {
		t.Errorf("artists = %v, want the NUL-separated pair", v)
	}
}

// TestRenderHeaderAndFooterFlags: same version/size/count; size = items+footer. The flags a tag
// carried go into both records, but for the three bits that state the tag's shape.
func TestRenderHeaderAndFooterFlags(t *testing.T) {
	raw, err := Render([]Item{{Key: "Title", Value: "x"}}, WriteVersion, true, 0)
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

	const carried = flagReadOnly | 1<<10
	raw, err = Render([]Item{{Key: "Title", Value: "x"}}, WriteVersion, true, carried)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := binary.LittleEndian.Uint32(raw[20:24]); got != flagHasHeader|flagIsHeader|carried {
		t.Errorf("header flags = %#x, want the carried bits beside has-header and is-header", got)
	}
	if got := binary.LittleEndian.Uint32(raw[len(raw)-12 : len(raw)-8]); got != flagHasHeader|carried {
		t.Errorf("footer flags = %#x, want the carried bits beside has-header", got)
	}

	// A footer-only tag claims no header, whatever the flags it carried said.
	raw, err = Render([]Item{{Key: "Title", Value: "x"}}, WriteVersion, false, 0xE0000000|flagReadOnly)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := binary.LittleEndian.Uint32(raw[len(raw)-12 : len(raw)-8]); got != flagReadOnly {
		t.Errorf("footer-only flags = %#x, want the read-only bit alone", got)
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

// TestCoverRoundTrip: every role is written under a name of its own, with a NUL-terminated file
// name, and reads back as that role.
func TestCoverRoundTrip(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + "\x00\x00\x00\rIHDR\x00\x00\x00\x02\x00\x00\x00\x03\x08\x06\x00\x00\x00")
	want := map[core.PictureType]string{
		core.PicFrontCover: "Cover Art (Front)",
		core.PicBackCover:  "Cover Art (Back)",
		core.PicConductor:  "Cover Art (Conductor)",
		core.PicLeadArtist: "Cover Art (Lead Artist)",
		core.PicFileIcon:   "Cover Art (Png Icon)",
	}
	seen := map[string]core.PictureType{}
	for typ := core.PicOther; typ <= core.PicPublisherLogo; typ++ {
		it := EncodeCover(core.Picture{Type: typ, Data: png})
		if name, ok := want[typ]; ok && it.Key != name {
			t.Errorf("%v: item name = %q, want %q", typ, it.Key, name)
		}
		if prev, dup := seen[strings.ToLower(it.Key)]; dup {
			t.Errorf("%v and %v share the item name %q", prev, typ, it.Key)
		}
		seen[strings.ToLower(it.Key)] = typ
		if _, bad := InvalidItemName(it.Key); bad || !IsCoverKey(it.Key) {
			t.Errorf("%v: %q is not a cover item name the specification allows", typ, it.Key)
		}
		if !it.NonText() {
			t.Errorf("%v: a cover item must be typed binary", typ)
		}
		file, _, ok := bytes.Cut(it.Data, []byte{0})
		if wantFile := strings.ToLower(strings.ReplaceAll(it.Key, " ", "_")) + ".png"; !ok || string(file) != wantFile {
			t.Errorf("%v: stored file name = %q, want %q", typ, file, wantFile)
		}
		got, err := DecodeCover(it.Key, it.Data)
		if err != nil {
			t.Fatalf("DecodeCover: %v", err)
		}
		if got.Type != typ || got.MIME != "image/png" || !bytes.Equal(got.Data, png) {
			t.Errorf("decoded = %+v, want type %v and the PNG bytes back", got, typ)
		}
		if got.Width != 2 || got.Height != 3 {
			t.Errorf("geometry = %dx%d, want 2x3 sniffed from the header", got.Width, got.Height)
		}
	}
	// A role the ID3 table does not name is stored as Other.
	if it := EncodeCover(core.Picture{Type: core.PicPublisherLogo + 1, Data: png}); it.Key != "Cover Art (Other)" {
		t.Errorf("reserved role: item name = %q, want Cover Art (Other)", it.Key)
	}
}

// TestRoleFromName: Cover Art names in any case, the spellings other taggers write, and an
// unknown role as Other; a name that only starts like one is no cover name.
func TestRoleFromName(t *testing.T) {
	for name, want := range map[string]core.PictureType{
		"Cover Art (Front)":          core.PicFrontCover,
		"COVER ART (BACK)":           core.PicBackCover,
		" cover art (artist) ":       core.PicArtist,
		"Cover Art (Media)":          core.PicMedia,
		"Cover Art (Disc)":           core.PicMedia,
		"Artist Picture":             core.PicArtist,
		"artist picture":             core.PicArtist,
		"Cover Art (lead)":           core.PicLeadArtist,
		"Cover Art (studio)":         core.PicRecordingLocation,
		"Cover Art (recording)":      core.PicDuringRecording,
		"Cover Art (performance)":    core.PicDuringPerformance,
		"Cover Art (movie scene)":    core.PicVideoScreenCapture,
		"Cover Art (Video Capture)":  core.PicVideoScreenCapture,
		"Cover Art (colored fish)":   core.PicBrightFish,
		"Cover Art (Fish)":           core.PicBrightFish,
		"Cover Art (band logo)":      core.PicBandLogo,
		"Cover Art (Band Logotype)":  core.PicBandLogo,
		"Cover Art (publisher logo)": core.PicPublisherLogo,
		"Cover Art (Png Icon)":       core.PicFileIcon,
		"Cover Art (Icon)":           core.PicOtherFileIcon,
		"Cover Art (other icon)":     core.PicOtherFileIcon,
		"Cover Art (Bogus)":          core.PicOther,
		"Cover Art(Front)":           core.PicFrontCover,
		"Cover Art":                  core.PicOther,
		"Cover Art (Front":           core.PicFrontCover,
	} {
		if got, ok := RoleFromName(name); !ok || got != want {
			t.Errorf("RoleFromName(%q) = %v,%v, want %v", name, got, ok, want)
		}
	}
	for _, name := range []string{"Cover Artist", "Cover Art Notes", "Cover", "Artist Pictures", "Title", ""} {
		if got, ok := RoleFromName(name); ok || IsCoverKey(name) {
			t.Errorf("RoleFromName(%q) = %v,true, want no cover name", name, got)
		}
	}
	// Whatever follows "Cover Art" but a parenthesis makes another name.
	for c := byte(0x21); c <= 0x7E; c++ {
		if c == '(' {
			continue
		}
		for _, name := range []string{"Cover Art" + string(c), "Cover Art " + string(c) + "Front)"} {
			if IsCoverKey(name) {
				t.Errorf("IsCoverKey(%q) = true, want no cover name", name)
			}
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

// TestRebuildSlashPairBehindATerminator: a slashed number some tagger ended with a NUL is a slash
// pair all the same, so an unrelated edit adds no item for its total.
func TestRebuildSlashPairBehindATerminator(t *testing.T) {
	orig := []Item{{Key: "Track", Value: "3/12\x00"}, {Key: "Title", Value: "T"}}
	base := Project(&Tag{Items: orig}).Tags
	if v, _ := base.Get(tag.TrackTotal); !slices.Equal(v, []string{"12"}) {
		t.Fatalf("TRACKTOTAL = %q, want the slash read", v)
	}
	edited := base.Clone()
	edited.Set(tag.Title, "New")
	got, _ := Rebuild(orig, base, edited, nil, false)
	if len(got) != 2 || !sameItem(got[0], orig[0]) || got[1].Value != "New" {
		t.Errorf("items = %+v, want the track item as it is and the new title", got)
	}
	edited = base.Clone()
	edited.Set(tag.TrackTotal, "20")
	got, _ = Rebuild(orig, base, edited, nil, false)
	if len(got) != 3 || got[0].Value != "3" || got[1].Key != "TRACKTOTAL" || got[1].Value != "20" {
		t.Errorf("items = %+v, want the number, then the new total in an item of its own", got)
	}
}

// TestRebuildMultiValuedNumberIsNoSlashPair: a number item of several values is read whole, so no
// total is taken from it: one set beside it is a new item, and the number item stays as it is.
func TestRebuildMultiValuedNumberIsNoSlashPair(t *testing.T) {
	orig := []Item{{Key: "Track", Value: "3/12\x004/12", Flags: flagReadOnly}, {Key: "Title", Value: "T"}}
	base := Project(&Tag{Items: orig}).Tags
	if base.Has(tag.TrackTotal) {
		t.Fatalf("TRACKTOTAL read from a multi-valued number: %v", base)
	}
	edited := base.Clone()
	edited.Set(tag.TrackTotal, "20")
	got, _ := Rebuild(orig, base, edited, nil, false)
	if len(got) != 3 || !sameItem(got[0], orig[0]) || got[1].Key != "Title" || got[2].Key != "TRACKTOTAL" {
		t.Errorf("items = %+v, want the number item as it is, the title, then the new total", got)
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

// TestRebuildCoverNames: each role is written under its own name; a second picture of a role
// has no name left and is dropped with a warning.
func TestRebuildCoverNames(t *testing.T) {
	ts := tag.NewTagSet()
	pic := func(pt core.PictureType, payload string) core.Picture {
		return core.Picture{Type: pt, Data: []byte(payload)}
	}
	const artistKey, bandKey, composerKey = "Cover Art (Artist)", "Cover Art (Band)", "Cover Art (Composer)"
	for _, c := range []struct {
		name        string
		pics        []core.Picture
		wantKeys    []string
		wantData    []string // image bytes per written item, matching wantKeys
		wantDropped []core.PictureType
	}{
		{"front then artist", []core.Picture{pic(core.PicFrontCover, "F"), pic(core.PicArtist, "A")},
			[]string{coverFrontKey, artistKey}, []string{"F", "A"}, nil},
		{"artist then front", []core.Picture{pic(core.PicArtist, "A"), pic(core.PicFrontCover, "F")},
			[]string{artistKey, coverFrontKey}, []string{"A", "F"}, nil},
		{"lone artist is no front", []core.Picture{pic(core.PicArtist, "A")},
			[]string{artistKey}, []string{"A"}, nil},
		{"three roles", []core.Picture{pic(core.PicFrontCover, "F"), pic(core.PicArtist, "A"), pic(core.PicBand, "B")},
			[]string{coverFrontKey, artistKey, bandKey}, []string{"F", "A", "B"}, nil},
		{"two fronts keep the first", []core.Picture{pic(core.PicFrontCover, "F1"), pic(core.PicFrontCover, "F2")},
			[]string{coverFrontKey}, []string{"F1"}, []core.PictureType{core.PicFrontCover}},
		{"two composers keep the first", []core.Picture{pic(core.PicComposer, "C1"), pic(core.PicComposer, "C2"), pic(core.PicFrontCover, "F")},
			[]string{composerKey, coverFrontKey}, []string{"C1", "F"}, []core.PictureType{core.PicComposer}},
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

// coverItem is a binary item holding img under file, as another tagger wrote it.
func coverItem(name, file string, img []byte, flags uint32) Item {
	return Item{Key: name, Data: slices.Concat([]byte(file), []byte{0}, img), Flags: flags | itemTypeBinary<<itemTypeShift}
}

// TestRebuildPictureEditKeepsUntouchedCoverItem: a picture edit writes a picture the file already
// holds as the item that holds it, name, file name and flags; the other source covers go.
func TestRebuildPictureEditKeepsUntouchedCoverItem(t *testing.T) {
	front := coverItem("COVER ART (FRONT)", "my front.png", tinyPNGBytes(), flagReadOnly|1<<20)
	back := coverItem(coverBackKey, "my back.png", append(tinyPNGBytes(), 1), 0)
	orig := []Item{{Key: "Title", Value: "T"}, front, back}
	tg := Project(&Tag{Items: orig})
	if len(tg.Pictures) != 2 {
		t.Fatalf("pictures = %+v, want the two covers read", tg.Pictures)
	}

	got, info := Rebuild(orig, tg.Tags, tg.Tags, tg.Pictures[:1], true)
	if len(got) != 2 || !sameItem(got[1], front) {
		t.Fatalf("items = %+v, want Title and the front item as the file holds it", got)
	}
	if ws := RebuildWarnings(nil, info); len(ws) != 0 {
		t.Errorf("warnings = %+v, want none", ws)
	}

	// A picture added beside them leaves both as they are and is written under its role's name.
	added := core.Picture{Type: core.PicConductor, Data: []byte("\x89PNG\r\n\x1a\nconductor")}
	got, _ = Rebuild(orig, tg.Tags, tg.Tags, append(slices.Clone(tg.Pictures), added), true)
	if len(got) != 4 || !sameItem(got[1], front) || !sameItem(got[2], back) || got[3].Key != "Cover Art (Conductor)" {
		t.Fatalf("items = %+v, want both covers as the file holds them and the conductor after", got)
	}

	// The same bytes under another role are another picture: written anew, the old item gone.
	moved := tg.Pictures[0]
	moved.Type = core.PicArtist
	got, _ = Rebuild(orig, tg.Tags, tg.Tags, []core.Picture{moved}, true)
	if len(got) != 2 || got[1].Key != "Cover Art (Artist)" || bytes.Contains(got[1].Data, []byte("my front.png")) {
		t.Fatalf("items = %+v, want the picture written under the artist name", got)
	}
}

// TestRebuildWritesAPictureOverATextItemThatSpellsIt: a text item under a cover name is no cover
// item, whatever its value: the picture is written as a binary item and the text displaced.
func TestRebuildWritesAPictureOverATextItemThatSpellsIt(t *testing.T) {
	png := tinyPNGBytes()
	orig := []Item{{Key: coverFrontKey, Value: "f.png\x00" + string(png)}}
	base := Project(&Tag{Items: orig})
	if len(base.Pictures) != 0 {
		t.Fatalf("pictures = %+v, want none read from a text item", base.Pictures)
	}
	got, info := Rebuild(orig, base.Tags, base.Tags, []core.Picture{{Type: core.PicFrontCover, Data: png}}, true)
	if len(got) != 1 || !got[0].NonText() {
		t.Fatalf("items = %+v, want one binary cover item", got)
	}
	if len(info.CoverTextReplaced) != 1 {
		t.Errorf("CoverTextReplaced = %v, want the text item reported", info.CoverTextReplaced)
	}
}

// TestCarried: the canonical view carries a text item whose name maps to a key and a cover item
// that decodes, and nothing else.
func TestCarried(t *testing.T) {
	bin := uint32(itemTypeBinary << itemTypeShift)
	for _, c := range []struct {
		name string
		it   Item
		want bool
	}{
		{"text", Item{Key: "Title", Value: "T"}, true},
		{"text under a name no key spells", Item{Key: "MOOD~X", Value: "calm"}, false},
		{"cover", coverItem(coverFrontKey, "f.png", tinyPNGBytes(), 0), true},
		{"cover under another tagger's name", coverItem("Artist Picture", "a.png", tinyPNGBytes(), 0), true},
		{"cover with no image bytes", Item{Key: coverFrontKey, Data: []byte("f.png\x00"), Flags: bin}, false},
		{"cover with no file name", Item{Key: coverFrontKey, Data: []byte("no-nul"), Flags: bin}, false},
		{"binary item", Item{Key: "Blob", Data: []byte{1, 0, 2}, Flags: bin}, false},
	} {
		if got := Carried(c.it); got != c.want {
			t.Errorf("%s: Carried = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSameItems: name, flags and payload bytes, in order; a value and the bytes that spell it are
// one payload.
func TestSameItems(t *testing.T) {
	a := []Item{{Key: "Title", Value: "T", Flags: flagReadOnly}, {Key: "Blob", Data: []byte{1, 2}, Flags: 2}}
	for name, c := range map[string]struct {
		b    []Item
		want bool
	}{
		"the same":         {[]Item{{Key: "Title", Data: []byte("T"), Flags: flagReadOnly}, {Key: "Blob", Data: []byte{1, 2}, Flags: 2}}, true},
		"another name":     {[]Item{{Key: "TITLE", Value: "T", Flags: flagReadOnly}, a[1]}, false},
		"other flags":      {[]Item{{Key: "Title", Value: "T"}, a[1]}, false},
		"another payload":  {[]Item{a[0], {Key: "Blob", Data: []byte{1, 3}, Flags: 2}}, false},
		"another order":    {[]Item{a[1], a[0]}, false},
		"one item fewer":   {a[:1], false},
		"one item further": {append(slices.Clone(a), Item{Key: "X", Value: "y"}), false},
	} {
		if got := SameItems(a, c.b); got != c.want {
			t.Errorf("%s: SameItems = %v, want %v", name, got, c.want)
		}
	}
	if !SameItems(nil, []Item{}) {
		t.Error("two empty lists differ")
	}
}

// TestUndecodableCover: a binary item under a cover name that holds no picture, and nothing else.
func TestUndecodableCover(t *testing.T) {
	bin := uint32(itemTypeBinary << itemTypeShift)
	for _, c := range []struct {
		name string
		it   Item
		want bool
	}{
		{"no file name", Item{Key: coverFrontKey, Data: []byte("no-nul"), Flags: bin}, true},
		{"no image bytes", Item{Key: "Cover Art (Booklet)", Data: []byte("f.png\x00"), Flags: bin}, true},
		{"external locator", Item{Key: coverFrontKey, Data: []byte("http://example.com/a.png"), Flags: 2 << itemTypeShift}, true},
		{"cover", coverItem(coverFrontKey, "f.png", tinyPNGBytes(), 0), false},
		{"text under a cover name", Item{Key: coverFrontKey, Value: "no-nul"}, false},
		{"binary item under another name", Item{Key: "Blob", Data: []byte("no-nul"), Flags: bin}, false},
	} {
		if got := UndecodableCover(c.it); got != c.want {
			t.Errorf("%s: UndecodableCover = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRebuildKeepsSameRoleCoversUnderTheirOwnNames: pictures of one role the file holds under
// different names both stay on a picture edit, and so do two items sharing a name.
func TestRebuildKeepsSameRoleCoversUnderTheirOwnNames(t *testing.T) {
	a := coverItem("Artist Picture", "a.png", tinyPNGBytes(), 0)
	b := coverItem("Cover Art (Artist)", "b.png", append(tinyPNGBytes(), 2), 0)
	dup := coverItem("Cover Art (Artist)", "c.png", append(tinyPNGBytes(), 3), 0)
	orig := []Item{a, b, dup}
	tg := Project(&Tag{Items: orig})
	if len(tg.Pictures) != 3 || tg.Pictures[0].Type != core.PicArtist || tg.Pictures[2].Type != core.PicArtist {
		t.Fatalf("pictures = %+v, want three artist pictures", tg.Pictures)
	}
	front := core.Picture{Type: core.PicFrontCover, Data: tinyPNGBytes()}
	pics := append(slices.Clone(tg.Pictures), front)
	if kept, dropped := PartitionCovers(pics, []bool{false, false, false, true}, orig); len(kept) != 4 || len(dropped) != 0 {
		t.Errorf("partition kept %v dropped %v, want every picture kept", kept, dropped)
	}
	got, info := Rebuild(orig, tg.Tags, tg.Tags, pics, true)
	if len(got) != 4 || !sameItem(got[0], a) || !sameItem(got[1], b) || !sameItem(got[2], dup) || got[3].Key != coverFrontKey {
		t.Fatalf("items = %+v, want the three artist items as the file holds them and the front", got)
	}
	if len(info.SlotDroppedCovers) != 0 {
		t.Errorf("SlotDroppedCovers = %v, want none", info.SlotDroppedCovers)
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

// TestRebuildCoverLeavesOtherMalformedCoversAlone: a cover written under one name leaves an
// undecodable item under another as it is.
func TestRebuildCoverLeavesOtherMalformedCoversAlone(t *testing.T) {
	badFront := Item{Key: coverFrontKey, Data: []byte("no-nul"), Flags: itemTypeBinary << itemTypeShift}
	ts := tag.NewTagSet()
	artist := []core.Picture{{Type: core.PicArtist, Data: tinyPNGBytes()}}

	got, info := Rebuild([]Item{badFront}, ts, ts, artist, true)
	if len(got) != 2 || got[0].Key != "Cover Art (Artist)" || !sameItem(got[1], badFront) {
		t.Fatalf("items = %+v, want the artist under its own name and the junk front preserved", got)
	}
	if len(info.MalformedCoversReplaced) != 0 || len(info.SlotDroppedCovers) != 0 {
		t.Errorf("info = %+v, want no replacement and no drop", info)
	}
}

// TestPartitionCovers: where two pictures get one item name the added one wins, else the earlier;
// a picture the file holds keeps the name of its item.
func TestPartitionCovers(t *testing.T) {
	pics := []core.Picture{
		{Type: core.PicFrontCover, Data: []byte("old")},
		{Type: core.PicFrontCover, Data: []byte("new")},
	}
	kept, dropped := PartitionCovers(pics, []bool{false, true}, nil)
	if !slices.Equal(kept, []int{1}) || !slices.Equal(dropped, []int{0}) {
		t.Errorf("added-aware partition kept %v dropped %v, want the added picture kept", kept, dropped)
	}
	kept, dropped = PartitionCovers(pics, nil, nil)
	if !slices.Equal(kept, []int{0}) || !slices.Equal(dropped, []int{1}) {
		t.Errorf("plain partition kept %v dropped %v, want the first picture kept", kept, dropped)
	}

	// The file holds "old" as its front: an added front takes the name from it.
	orig := []Item{coverItem(coverFrontKey, "f.png", []byte("old"), 0)}
	kept, dropped = PartitionCovers(pics, []bool{false, true}, orig)
	if !slices.Equal(kept, []int{1}) || !slices.Equal(dropped, []int{0}) {
		t.Errorf("over a held front: kept %v dropped %v, want the added picture kept", kept, dropped)
	}
	// Held under a name no role writes, it is in nobody's way.
	orig = []Item{coverItem("Cover Art (Sleeve)", "f.png", []byte("old"), 0)}
	other := []core.Picture{{Type: core.PicOther, Data: []byte("old")}, {Type: core.PicOther, Data: []byte("new")}}
	if kept, dropped = PartitionCovers(other, []bool{false, true}, orig); len(kept) != 2 || len(dropped) != 0 {
		t.Errorf("held under its own name: kept %v dropped %v, want both kept", kept, dropped)
	}
	// Every role has a name, so no role crowds out another.
	var all []core.Picture
	for typ := core.PicOther; typ <= core.PicPublisherLogo; typ++ {
		all = append(all, core.Picture{Type: typ, Data: []byte{byte(typ)}})
	}
	if kept, dropped = PartitionCovers(all, nil, nil); len(kept) != len(all) || len(dropped) != 0 {
		t.Errorf("one picture per role: kept %v dropped %v, want all kept", kept, dropped)
	}
}

// TestPartitionCoversKeepsWhatItKept: the pictures a partition keeps are all kept when they are
// partitioned again, as the editor does with a transfer's pictures and the writer with an
// editor's, and the writer stores each of them as it is, in order. Random arrangements of items
// and pictures over a few roles, names and images, so that equal pictures and shared names abound.
func TestPartitionCoversKeepsWhatItKept(t *testing.T) {
	rng := rand.New(rand.NewPCG(16, 2))
	names := []string{"Cover Art (Artist)", "Artist Picture", "COVER ART (ARTIST)", "Cover Art (Media)", "Cover Art (Disc)",
		"Cover Art (Front)", "Cover Art (Bogus)", "Cover Art (Other)", " Cover Art (Front) "}
	types := []core.PictureType{core.PicArtist, core.PicMedia, core.PicFrontCover, core.PicOther}
	images := [][]byte{{1}, {2}, {3}}
	for n := range 40000 {
		orig := make([]Item, rng.IntN(6))
		for i := range orig {
			orig[i] = coverItem(names[rng.IntN(len(names))], "f.png", images[rng.IntN(len(images))], 0)
		}
		pics := make([]core.Picture, rng.IntN(6))
		mask := make([]bool, len(pics))
		for i := range pics {
			pics[i] = core.Picture{Type: types[rng.IntN(len(types))], Data: images[rng.IntN(len(images))]}
			mask[i] = rng.IntN(2) == 0
		}
		if n%2 == 0 {
			mask = nil // a transfer
		}
		keptIdx, _ := PartitionCovers(pics, mask, orig)
		kept := make([]core.Picture, len(keptIdx))
		all := make([]bool, len(keptIdx))
		for i, at := range keptIdx {
			kept[i], all[i] = pics[at], true
		}
		describe := func() string {
			var b strings.Builder
			for _, it := range orig {
				fmt.Fprintf(&b, "item %q %v; ", it.Key, it.Data[len(it.Data)-1])
			}
			for i, p := range pics {
				fmt.Fprintf(&b, "pic %v %v added=%v; ", p.Type, p.Data, mask != nil && mask[i])
			}
			return b.String()
		}
		for _, again := range [][]bool{nil, all} {
			if _, dropped := PartitionCovers(kept, again, orig); len(dropped) != 0 {
				t.Fatalf("case %d: kept %v, then dropped %v of them (added=%v)\n%s", n, keptIdx, dropped, again != nil, describe())
			}
		}
		var info RebuildInfo
		items, _ := emitPictures(orig, kept, &info)
		if len(info.SlotDroppedCovers) != 0 || len(items) != len(kept) {
			t.Fatalf("case %d: the writer stored %d of the %d pictures kept\n%s", n, len(items), len(kept), describe())
		}
		for i, it := range items {
			if typ, img, ok := coverImage(it); !ok || typ != kept[i].Type || !bytes.Equal(img, kept[i].Data) {
				t.Fatalf("case %d: item %d %q does not hold picture %d\n%s", n, i, it.Key, i, describe())
			}
		}
	}
}

// TestAssignCoversInOnePass: a tag of one image in many items, each a picture, is assigned in one
// pass over the pictures, not by a search of the items for each.
func TestAssignCoversInOnePass(t *testing.T) {
	const n = 300000
	orig := make([]Item, n)
	pics := make([]core.Picture, n)
	for i := range orig {
		orig[i] = coverItem(coverFrontKey, "f.png", tinyPNGBytes(), 0)
		pics[i] = core.Picture{Type: core.PicFrontCover, Data: tinyPNGBytes()}
	}
	start := time.Now()
	slots := assignCovers(pics, nil, orig)
	if d := time.Since(start); d > onePass {
		t.Errorf("the assignment took %v", d)
	}
	if len(slots) != n || slots[n-1].from != n-1 {
		t.Fatalf("slots = %d, want every picture stored as the item at its place", len(slots))
	}
}

// TestRebuildAddedPictureBesideOneImageUnderTwoNames: a file holding one image under two names of
// a role, an added picture of the role: it takes the role's own name, and the item under the other
// name stays, whichever of the two equal pictures the editor kept.
func TestRebuildAddedPictureBesideOneImageUnderTwoNames(t *testing.T) {
	own := coverItem("Cover Art (Artist)", "a.png", tinyPNGBytes(), 0)
	other := coverItem("Artist Picture", "a.png", tinyPNGBytes(), 0)
	added := core.Picture{Type: core.PicArtist, Data: append(tinyPNGBytes(), 9)}
	for _, orig := range [][]Item{{own, other}, {other, own}} {
		held := Project(&Tag{Items: orig}).Pictures
		pics := append(slices.Clone(held), added)
		keptIdx, dropped := PartitionCovers(pics, []bool{false, false, true}, orig)
		if len(keptIdx) != 2 || len(dropped) != 1 || keptIdx[1] != 2 {
			t.Fatalf("partition kept %v dropped %v, want one of the equal pictures and the added one", keptIdx, dropped)
		}
		ts := tag.NewTagSet()
		got, info := Rebuild(orig, ts, ts, []core.Picture{pics[keptIdx[0]], added}, true)
		if len(info.SlotDroppedCovers) != 0 || len(got) != 2 || !sameItem(got[0], other) || got[1].Key != "Cover Art (Artist)" {
			t.Fatalf("items = %+v (dropped %v), want Artist Picture as the file holds it and the added picture under the role's name",
				got, info.SlotDroppedCovers)
		}
		if _, img, _ := coverImage(got[1]); !bytes.Equal(img, added.Data) {
			t.Error("the item under the role's name does not hold the added image")
		}
	}
}

// TestRenderRejectsOversizedTag: refuse tags past reader size cap.
func TestRenderRejectsOversizedTag(t *testing.T) {
	huge := Item{Key: coverFrontKey, Data: make([]byte, maxTagBytes+1), Flags: itemTypeBinary << itemTypeShift}
	if _, err := Render([]Item{huge}, WriteVersion, true, 0); !errors.Is(err, waxerr.ErrPictureTooLarge) {
		t.Errorf("err = %v, want ErrPictureTooLarge", err)
	}
}

// TestRenderPreservesVersionAndShape: keep APEv1 and footer-only shape, and a read-only tag's
// flags.
func TestRenderPreservesVersionAndShape(t *testing.T) {
	raw, err := Render([]Item{{Key: "Title", Value: "x"}}, 1000, false, 0)
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
	if tg.Version != 1000 || tg.HasHeader || tg.Flags != 0 {
		t.Errorf("read back version %d hasHeader=%v flags %#x, want 1000, false and 0", tg.Version, tg.HasHeader, tg.Flags)
	}

	const readOnlyHeaded = 0x80000001
	raw, err = Render([]Item{{Key: "Title", Value: "x"}}, WriteVersion, true, readOnlyHeaded)
	if err != nil {
		t.Fatal(err)
	}
	tg, ok, err = ParseAt(core.BytesSource(raw), int64(len(raw)), 1<<20, 1000)
	if err != nil || !ok {
		t.Fatalf("ParseAt: ok=%v err=%v", ok, err)
	}
	if tg.Flags != readOnlyHeaded || !tg.HasHeader {
		t.Errorf("read back flags %#x hasHeader=%v, want %#x and a header", tg.Flags, tg.HasHeader, uint32(readOnlyHeaded))
	}
	again, err := Render(tg.Items, tg.Version, tg.HasHeader, tg.Flags)
	if err != nil || !bytes.Equal(again, raw) {
		t.Errorf("a tag rendered from what was read differs from the tag read (err %v)", err)
	}
}

// TestHeaderOnlyFlagsSurvive: a flag one record sets and the other does not is the tag's, however
// the tag is found, and a rewrite carries it in both.
func TestHeaderOnlyFlagsSurvive(t *testing.T) {
	raw, err := Render([]Item{{Key: "Title", Value: "x"}}, WriteVersion, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	const extra = flagReadOnly | 1<<10
	binary.LittleEndian.PutUint32(raw[20:24], flagHasHeader|flagIsHeader|extra)
	src, end := core.BytesSource(raw), int64(len(raw))

	check := func(how string, tg *Tag, ok bool, err error) {
		t.Helper()
		if err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", how, ok, err)
		}
		if tg.Flags&extra != extra || !tg.ReadOnly() {
			t.Errorf("%s: flags = %#x, want the header's read-only and bit 10 among them", how, tg.Flags)
		}
	}
	tg, ok, err := ParseAt(src, end, 1<<20, 0)
	check("by the footer", tg, ok, err)
	headed, ok, err := ParseHeadAt(src, 0, end, 1<<20, 0)
	check("by the header", headed, ok, err)
	search, err := NewSearch(src, 0, end, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	found, ok, err := search.Before(end, 0)
	check("by a search", found, ok, err)

	again, err := Render(tg.Items, tg.Version, tg.HasHeader, tg.Flags)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(again[20:24]); got != flagHasHeader|flagIsHeader|extra {
		t.Errorf("rewritten header flags = %#x, want the two bits kept", got)
	}
	if got := binary.LittleEndian.Uint32(again[len(again)-12 : len(again)-8]); got != flagHasHeader|extra {
		t.Errorf("rewritten footer flags = %#x, want the two bits carried into it", got)
	}
}

// TestParseAtRejectsUnbackedHeaderFlag: has-header bit needs a backing APETAGEX record.
func TestParseAtRejectsUnbackedHeaderFlag(t *testing.T) {
	raw, err := Render([]Item{{Key: "Title", Value: "x"}}, WriteVersion, false, 0)
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
	raw, err := Render([]Item{{Key: "A", Value: "1"}, {Key: "B", Value: "2"}, {Key: "C", Value: "3"}}, WriteVersion, true, 0)
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

// A tag the allocation limit cannot hold, footer or items, is refused; a region too short for a
// footer holds no tag.
func TestParseAtRefusesOverLimit(t *testing.T) {
	raw, err := Render([]Item{{Key: "Title", Value: strings.Repeat("t", 100)}}, WriteVersion, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	src := core.BytesSource(raw)
	itemsLen := int64(len(raw)) - 2*footerLen
	for _, limit := range []int64{footerLen - 1, itemsLen - 1} {
		if _, ok, err := ParseAt(src, int64(len(raw)), limit, 0); ok || !errors.Is(err, waxerr.ErrSizeTooLarge) {
			t.Errorf("limit %d: ok=%v err=%v, want declared size too large", limit, ok, err)
		}
	}
	if _, ok, err := ParseAt(src, int64(len(raw)), itemsLen, 0); !ok || err != nil {
		t.Errorf("limit %d: ok=%v err=%v, want the tag read", itemsLen, ok, err)
	}
	if _, ok, err := ParseAt(src, footerLen-1, 1, 0); ok || err != nil {
		t.Errorf("short region: ok=%v err=%v, want no tag and no error", ok, err)
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

// TestRebuildEmptyValuesWithinMultiValue: an empty value ahead of or between others is stored and
// reads back; one at the end would read as a terminator, so it is dropped with a warning; values
// all empty leave one empty item.
func TestRebuildEmptyValuesWithinMultiValue(t *testing.T) {
	base := tag.NewTagSet()
	for _, c := range []struct {
		name    string
		vals    []string
		want    string // the item's value
		read    []string
		dropped bool
	}{
		{"all empty", []string{"", ""}, "", []string{""}, true},
		{"trailing empty", []string{"A", ""}, "A", []string{"A"}, true},
		{"several trailing", []string{"A", "", "", ""}, "A", []string{"A"}, true},
		{"leading empty", []string{"", "B"}, "\x00B", []string{"", "B"}, false},
		{"interior empty", []string{"A", "", "B"}, "A\x00\x00B", []string{"A", "", "B"}, false},
		{"leading and trailing", []string{"", "B", ""}, "\x00B", []string{"", "B"}, true},
		{"both present", []string{"A", "B"}, "A\x00B", []string{"A", "B"}, false},
		{"one empty", []string{""}, "", []string{""}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			edited := tag.NewTagSet()
			for _, v := range c.vals {
				edited.Add(tag.Artist, v)
			}
			got, info := Rebuild(nil, base, edited, nil, false)
			if len(got) != 1 || got[0].Value != c.want {
				t.Fatalf("items = %+v, want one item valued %q", got, c.want)
			}
			if DropsTrailingEmpty(c.vals) != c.dropped {
				t.Errorf("DropsTrailingEmpty(%q) = %v, want %v", c.vals, !c.dropped, c.dropped)
			}
			ws := RebuildWarnings(nil, info)
			if c.dropped {
				if len(ws) != 1 || ws[0].Code != core.WarnValueDropped || !slices.Equal(ws[0].Keys, []tag.Key{tag.Artist}) {
					t.Errorf("warnings = %+v, want one value-dropped keyed ARTIST", ws)
				}
			} else if len(ws) != 0 {
				t.Errorf("warnings = %+v, want none", ws)
			}
			// Round-trip value set.
			tg := parseRendered(t, got)
			if vals, _ := Project(tg).Tags.Get(tag.Artist); !slices.Equal(vals, c.read) {
				t.Errorf("read back %q, want %q", vals, c.read)
			}
			d, reason, override := NewTransferClassifier(nil)(tag.Artist, c.vals, nil)
			if c.dropped && (!override || d != core.Lossy || reason == "") {
				t.Errorf("classifier = %v,%q,%v, want Lossy with a reason", d, reason, override)
			}
			if !c.dropped && override {
				t.Errorf("classifier overrode the grade of values stored whole: %v, %q", d, reason)
			}
		})
	}
}

// TestRebuildWarnsOfEachKeyLosingATrailingEmpty: one warning per key, in rebuild order.
func TestRebuildWarnsOfEachKeyLosingATrailingEmpty(t *testing.T) {
	edited := tag.NewTagSet()
	edited.Set(tag.Artist, "A", "")
	edited.Set(tag.Title, "T")
	edited.Set(tag.Composer, "C", "", "")
	_, info := Rebuild(nil, tag.NewTagSet(), edited, nil, false)
	var keys []tag.Key
	for _, w := range RebuildWarnings(nil, info) {
		keys = append(keys, w.Keys...)
	}
	if want := []tag.Key{tag.Artist, tag.Composer}; !slices.Equal(keys, want) {
		t.Errorf("warned keys = %v, want %v", keys, want)
	}
}

// TestSplitItemValues: NULs at the end are terminators; the empties before them are values.
func TestSplitItemValues(t *testing.T) {
	for value, want := range map[string][]string{
		"":               {""},
		"\x00":           {""},
		"\x00\x00":       {""},
		"A":              {"A"},
		"A\x00":          {"A"},
		"A\x00B":         {"A", "B"},
		"A\x00B\x00\x00": {"A", "B"},
		"A\x00\x00B":     {"A", "", "B"},
		"\x00B":          {"", "B"},
		"\x00\x00B\x00":  {"", "", "B"},
	} {
		if got := splitItemValues(value); !slices.Equal(got, want) {
			t.Errorf("splitItemValues(%q) = %q, want %q", value, got, want)
		}
	}
}
