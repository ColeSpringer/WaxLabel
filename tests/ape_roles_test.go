package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// roleCover is a Cover Art item as another tagger wrote it: a binary item holding a file name, a
// NUL and the image.
func roleCover(name, file string, img []byte) ape.Item {
	return ape.Item{Key: name, Data: slices.Concat([]byte(file), []byte{0}, img), Flags: 1 << 1}
}

// roleItems are five cover items under the names Mp3tag and foobar2000 write and one no tagger
// does, each holding an image of its own.
func roleItems() []ape.Item {
	img := func(n byte) []byte { return append(testfile.PNG(), n) }
	return []ape.Item{
		roleCover("Cover Art (Artist)", "artist.png", img(1)),
		roleCover("COVER ART (MEDIA)", "the disc.png", img(2)),
		roleCover("Cover Art (Front)", "folder.png", img(3)),
		roleCover("Artist Picture", "artist2.png", img(4)),
		roleCover("Cover Art (Bogus)", "what.png", img(5)),
	}
}

// roleFiles is a file of each format whose tag store is an APE tag, holding roleItems and a title.
func roleFiles(t *testing.T) map[string][]byte {
	t.Helper()
	tagBytes, err := ape.Render(append(roleItems(), ape.Item{Key: "Title", Value: "T"}), 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	mac := readFixture(t, "../testdata/notags.ape")
	return map[string][]byte{
		"wavpack":       slices.Concat(bareWV(t), tagBytes),
		"monkeys-audio": slices.Concat(mac, tagBytes),
		"musepack":      slices.Concat(testfile.MPCChaptered(), tagBytes),
	}
}

var roleTypes = []wl.PictureType{wl.PicArtist, wl.PicMedia, wl.PicFrontCover, wl.PicArtist, wl.PicOther}

// holdsItems reports whether b holds each of items byte for byte: flags, name and payload.
func holdsItems(t *testing.T, b []byte, items []ape.Item) {
	t.Helper()
	for _, it := range items {
		one, err := ape.Render([]ape.Item{it}, 2000, false, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(b, one[:len(one)-32]) {
			t.Errorf("the %q item is not in the output as the file held it", it.Key)
		}
	}
}

// Every Cover Art item is read as a picture of the role its name gives, the names Mp3tag and
// foobar2000 write among them, and an unknown role as Other.
func TestAPECoverRolesRead(t *testing.T) {
	for name, src := range roleFiles(t) {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, src)
			pics := doc.Pictures()
			if got := pictureTypes(pics); !slices.Equal(got, roleTypes) {
				t.Fatalf("picture roles = %v, want %v", got, roleTypes)
			}
			for i, p := range pics {
				if want := append(testfile.PNG(), byte(i+1)); !bytes.Equal(p.Data, want) || p.MIME != "image/png" {
					t.Errorf("picture %d = %s, %d bytes, want the PNG its item holds", i, p.MIME, len(p.Data))
				}
			}
			if ws := doc.Warnings(); len(ws) != 0 {
				t.Errorf("warnings = %+v, want none", ws)
			}
		})
	}
}

// A copy carries the five pictures to a format that stores any number of them, roles kept.
func TestAPECoverRolesCopyToFLAC(t *testing.T) {
	src := mustParseBytes(t, roleFiles(t)["wavpack"])
	dstBytes := readFixture(t, "../testdata/notags.flac")
	plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if carried, lossy, dropped := pictureCounts(report); carried != 5 || lossy != 0 || dropped != 0 {
		t.Errorf("picture grades = %d carried %d lossy %d dropped, want 5/0/0", carried, lossy, dropped)
	}
	pics := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Pictures()
	if got := pictureTypes(pics); !slices.Equal(got, roleTypes) {
		t.Fatalf("FLAC picture roles = %v, want %v", got, roleTypes)
	}
}

// An edit that leaves a picture alone keeps its item as the file holds it, name, file name and
// flags, whether it edits a tag, adds a picture or removes another.
func TestAPECoverItemsKeptByEdits(t *testing.T) {
	for name, src := range roleFiles(t) {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, src)

			plan, err := doc.Edit().Set(tag.Title, "New").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, src, plan)
			holdsItems(t, out, roleItems())
			if got := pictureTypes(mustParseBytes(t, out).Pictures()); !slices.Equal(got, roleTypes) {
				t.Errorf("after a tag edit: picture roles = %v, want %v", got, roleTypes)
			}

			conductor := append(testfile.PNG(), 9)
			plan, err = doc.Edit().AddPicture(wl.Picture{Type: wl.PicConductor, Data: conductor}).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			for _, code := range []wl.WarningCode{wl.WarnPictureUnsupported, wl.WarnPictureMetadataDropped} {
				if planWarns(t, plan, code) {
					t.Errorf("adding a conductor picture warned %s: %+v", code, plan.Report().Warnings)
				}
			}
			out = applyToBytes(t, src, plan)
			holdsItems(t, out, roleItems())
			holdsItems(t, out, []ape.Item{roleCover("Cover Art (Conductor)", "cover_art_(conductor).png", conductor)})
			if got, want := pictureTypes(mustParseBytes(t, out).Pictures()), append(slices.Clone(roleTypes), wl.PicConductor); !slices.Equal(got, want) {
				t.Errorf("after adding a picture: picture roles = %v, want %v", got, want)
			}

			plan, err = doc.Edit().RemovePictures(func(p wl.Picture) bool { return p.Type == wl.PicFrontCover }).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out = applyToBytes(t, src, plan)
			items := roleItems()
			holdsItems(t, out, slices.Concat(items[:2], items[3:]))
			if bytes.Contains(out, []byte("folder.png")) {
				t.Error("the removed front cover is still in the output")
			}
		})
	}
}

// An added picture takes the name of its role: one the file holds under that name is replaced, with
// a warning, and one it holds under another name stays.
func TestAPEAddedPictureTakesItsRolesName(t *testing.T) {
	src := roleFiles(t)["wavpack"]
	artist := append(testfile.PNG(), 7)
	plan, err := mustParseBytes(t, src).Edit().AddPicture(wl.Picture{Type: wl.PicArtist, Data: artist}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnPictureUnsupported) {
		t.Errorf("plan warnings = %+v, want one for the artist picture replaced", plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)
	items := roleItems()
	holdsItems(t, out, items[1:]) // "Artist Picture" among them
	if bytes.Contains(out, []byte("artist.png")) {
		t.Error("the artist picture held under the role's own name was not replaced")
	}
	pics := mustParseBytes(t, out).Pictures()
	want := []wl.PictureType{wl.PicMedia, wl.PicFrontCover, wl.PicArtist, wl.PicOther, wl.PicArtist}
	if got := pictureTypes(pics); !slices.Equal(got, want) || !bytes.Equal(pics[4].Data, artist) {
		t.Errorf("picture roles = %v, want %v with the added artist last", got, want)
	}
}

// A copy of a file onto itself changes nothing, two pictures of one role under two names included.
func TestAPECoverRolesSelfCopyIsANoOp(t *testing.T) {
	for name, src := range roleFiles(t) {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, src)
			plan, report, err := doc.PrepareTransfer(mustParseBytes(t, src))
			if err != nil {
				t.Fatalf("PrepareTransfer: %v", err)
			}
			if carried, lossy, dropped := pictureCounts(report); carried != 5 || lossy != 0 || dropped != 0 {
				t.Errorf("picture grades = %d carried %d lossy %d dropped, want 5/0/0", carried, lossy, dropped)
			}
			if !plan.IsNoOp() {
				t.Errorf("a self-copy is not a no-op: %+v", plan.Report())
			}
		})
	}
}

// Every role is written under a name of its own and read back as that role.
func TestAPEEveryRoleRoundTrips(t *testing.T) {
	src := readFixture(t, notagsWV)
	ed := mustParseBytes(t, src).Edit()
	var want []wl.PictureType
	for typ := wl.PicOther; typ <= wl.PicPublisherLogo; typ++ {
		if typ == wl.PicFileIcon {
			continue // the file icon is a 32x32 PNG, checked apart from its name
		}
		ed.AddPicture(wl.Picture{Type: typ, Data: append(testfile.PNG(), byte(typ))})
		want = append(want, typ)
	}
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if planWarns(t, plan, wl.WarnPictureUnsupported) || planWarns(t, plan, wl.WarnPictureMetadataDropped) {
		t.Errorf("plan warnings = %+v, want none: every role has a name", plan.Report().Warnings)
	}
	doc := mustParseBytes(t, applyToBytes(t, src, plan))
	if got := pictureTypes(doc.Pictures()); !slices.Equal(got, want) {
		t.Errorf("picture roles = %v, want %v", got, want)
	}
}

// A description has nowhere to go in a Cover Art item: the picture is stored and the loss warned.
func TestAPEPictureDescriptionIsDropped(t *testing.T) {
	src := readFixture(t, notagsWV)
	plan, err := mustParseBytes(t, src).Edit().
		AddPicture(wl.Picture{Type: wl.PicArtist, Description: "on stage", Data: testfile.PNG()}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !planWarns(t, plan, wl.WarnPictureMetadataDropped) {
		t.Errorf("plan warnings = %+v, want picture-metadata-dropped for the description", plan.Report().Warnings)
	}
	pics := mustParseBytes(t, applyToBytes(t, src, plan)).Pictures()
	if len(pics) != 1 || pics[0].Type != wl.PicArtist || pics[0].Description != "" {
		t.Errorf("pictures = %+v, want the artist picture without its description", pics)
	}
}

// twoNamesFile holds one image under two names of a role, as a file tagged for two players does,
// and returns the items too.
func twoNamesFile(t *testing.T) ([]byte, []ape.Item) {
	t.Helper()
	img := append(testfile.PNG(), 1)
	items := []ape.Item{roleCover("Cover Art (Artist)", "a.png", img), roleCover("Artist Picture", "a.png", img)}
	tagBytes, err := ape.Render(append(slices.Clone(items), ape.Item{Key: "Title", Value: "T"}), 2000, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	return slices.Concat(bareWV(t), tagBytes), items
}

// An added picture of a role the file holds one image of under two names takes the role's own
// name: the picture there is replaced, with one warning, the item under the other name stays, and
// no drop option is needed, since the added picture is stored.
func TestAPEAddedPictureBesideOneImageUnderTwoNames(t *testing.T) {
	src, items := twoNamesFile(t)
	added := append(testfile.PNG(), 7)
	plan, err := mustParseBytes(t, src).Edit().AddPicture(wl.Picture{Type: wl.PicArtist, Data: added}).Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var warned int
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnPictureUnsupported {
			warned++
		}
	}
	if warned != 1 {
		t.Errorf("plan warnings = %+v, want one, for the picture replaced", plan.Report().Warnings)
	}
	out := applyToBytes(t, src, plan)
	holdsItems(t, out, items[1:])
	holdsItems(t, out, []ape.Item{roleCover("Cover Art (Artist)", "cover_art_(artist).png", added)})
	pics := mustParseBytes(t, out).Pictures()
	if len(pics) != 2 || !bytes.Equal(pics[0].Data, append(testfile.PNG(), 1)) || !bytes.Equal(pics[1].Data, added) {
		t.Fatalf("pictures = %d, want the image the file held and the added one", len(pics))
	}
}

// A copy onto such a file of an artist picture ahead of the two it holds stores the new one and one
// of the two: what the report says is what the write does.
func TestAPECopyOntoOneImageUnderTwoNames(t *testing.T) {
	dstBytes, items := twoNamesFile(t)
	img, other := append(testfile.PNG(), 1), append(testfile.PNG(), 7)
	flacSrc := readFixture(t, "../testdata/notags.flac")
	seed, err := mustParseBytes(t, flacSrc).Edit().
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: other}).
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: img}).
		AddPicture(wl.Picture{Type: wl.PicArtist, Data: img}).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	src := mustParseBytes(t, applyToBytes(t, flacSrc, seed))
	plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	if carried, lossy, dropped := pictureCounts(report); carried != 2 || lossy != 0 || dropped != 1 {
		t.Errorf("picture grades = %d carried %d lossy %d dropped, want 2/0/1", carried, lossy, dropped)
	}
	out := applyToBytes(t, dstBytes, plan)
	holdsItems(t, out, items[1:])
	pics := mustParseBytes(t, out).Pictures()
	if len(pics) != 2 || !bytes.Equal(pics[0].Data, other) || !bytes.Equal(pics[1].Data, img) {
		t.Fatalf("pictures = %d, want the new artist picture and the one the file held", len(pics))
	}
}

// A clear of the pictures also removes the cover items that did not decode, as in FLAC and Ogg; a
// removal of the pictures that are read leaves them, and so does any other edit.
func TestAPEClearPicturesRemovesUndecodableCovers(t *testing.T) {
	bad := ape.Item{Key: "Cover Art (Booklet)", Data: []byte("no file name ends here"), Flags: 1 << 1}
	good := roleCover("Cover Art (Front)", "folder.png", testfile.PNG())
	for name, items := range map[string][]ape.Item{
		"beside a picture": {{Key: "Title", Value: "T"}, good, bad},
		"alone":            {{Key: "Title", Value: "T"}, bad},
	} {
		t.Run(name, func(t *testing.T) {
			tagBytes, err := ape.Render(items, 2000, true, 0)
			if err != nil {
				t.Fatal(err)
			}
			src := slices.Concat(bareWV(t), tagBytes)
			doc := mustParseBytes(t, src)
			if !hasWarning(doc, wl.WarnInvalidPicture) {
				t.Fatalf("warnings = %+v, want invalid-picture for the item that does not decode", doc.Warnings())
			}

			plan, err := doc.Edit().RemovePictures(func(wl.Picture) bool { return true }).Set(tag.Title, "New").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			holdsItems(t, applyToBytes(t, src, plan), []ape.Item{bad})

			plan, err = doc.Edit().ClearPictures().Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if plan.IsNoOp() || !slices.Contains(plan.Report().Operations, "undecodable Cover Art items removed: 1") {
				t.Fatalf("noOp=%v operations=%q, want the item's removal reported", plan.IsNoOp(), plan.Report().Operations)
			}
			out := applyToBytes(t, src, plan)
			if bytes.Contains(out, []byte("Cover Art")) {
				t.Error("a cover item is still in the output")
			}
			re := mustParseBytes(t, out)
			if len(re.Warnings()) != 0 || len(re.Pictures()) != 0 || re.Fields().Title != "T" {
				t.Errorf("after the clear: %d pictures, title %q, warnings %+v", len(re.Pictures()), re.Fields().Title, re.Warnings())
			}
			// Cleared again, there is nothing left to remove.
			if again, err := re.Edit().ClearPictures().Prepare(); err != nil || !again.IsNoOp() {
				t.Errorf("a second clear: noOp=%v err=%v, want a no-op", again != nil && again.IsNoOp(), err)
			}
		})
	}
}
