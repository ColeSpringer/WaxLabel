package waxlabel

import (
	"sync"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// docWithCover builds a Document with one front cover; Edit() shallow-clones Data.
func docWithCover(first byte) *Document {
	return &Document{media: &core.Media{
		Format:   core.FormatFLAC,
		Pictures: []core.Picture{{Type: core.PicFrontCover, MIME: "image/jpeg", Data: []byte{first, 0xBB, 0xCC}}},
	}}
}

// TestRemovePicturesMatchCannotMutateDocument: match gets a Data-detached copy.
func TestRemovePicturesMatchCannotMutateDocument(t *testing.T) {
	doc := docWithCover(0xAA)
	doc.Edit().RemovePictures(func(p Picture) bool {
		if len(p.Data) > 0 {
			p.Data[0] = 0xFF // must land on the detached probe, not the Document
		}
		return false // keep the picture; only probe the aliasing
	})
	if got := doc.Pictures()[0].Data[0]; got != 0xAA {
		t.Errorf("Document cover Data[0] = %#x after a mutating match, want 0xaa (match mutated shared bytes)", got)
	}
}

// TestRemovePicturesNoRaceWithPictures: concurrent mutating match vs Pictures()
// must not race (-race proves it).
func TestRemovePicturesNoRaceWithPictures(t *testing.T) {
	doc := docWithCover(0xAA)
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range iters {
			doc.Edit().RemovePictures(func(p Picture) bool {
				for i := range p.Data {
					p.Data[i] ^= 0xFF
				}
				return false
			})
		}
	}()
	go func() {
		defer wg.Done()
		for range iters {
			for _, p := range doc.Pictures() {
				_ = p.Data
			}
		}
	}()
	wg.Wait()
}

// TestReplacePictures: the picture takes the place of the first match and every other match goes;
// with no match it is appended. Only the new picture counts as added.
func TestReplacePictures(t *testing.T) {
	pic := func(typ core.PictureType, id byte) core.Picture {
		return core.Picture{Type: typ, MIME: "image/jpeg", Data: []byte{id}}
	}
	isFront := func(p Picture) bool { return p.Type == PicFrontCover }
	front, back := core.PicFrontCover, core.PicBackCover
	for name, tc := range map[string]struct {
		have    []core.Picture
		replace []byte // the ids of the covers put in, one call each
		want    []byte // ids, in order
		added   int    // index of the added picture
	}{
		"front then back":     {[]core.Picture{pic(front, 1), pic(back, 2)}, []byte{9}, []byte{9, 2}, 0},
		"back then two front": {[]core.Picture{pic(back, 2), pic(front, 1), pic(front, 3)}, []byte{9}, []byte{2, 9}, 1},
		"no front":            {[]core.Picture{pic(back, 2)}, []byte{9}, []byte{2, 9}, 1},
		"no pictures":         {nil, []byte{9}, []byte{9}, 0},
		"replaced twice":      {[]core.Picture{pic(front, 1), pic(back, 2)}, []byte{8, 9}, []byte{9, 2}, 0},
	} {
		doc := &Document{media: &core.Media{Format: core.FormatFLAC, Pictures: tc.have}}
		ed := doc.Edit()
		for _, id := range tc.replace {
			ed.ReplacePictures(isFront, pic(front, id))
		}
		var got []byte
		for i, p := range ed.pictures {
			got = append(got, p.Data[0])
			if added := i < len(ed.addedMask) && ed.addedMask[i]; added != (i == tc.added) {
				t.Errorf("%s: picture %d added = %v, want only picture %d added", name, i, added, tc.added)
			}
		}
		if string(got) != string(tc.want) || !ed.picsTouched {
			t.Errorf("%s: pictures %v (touched %v), want %v", name, got, ed.picsTouched, tc.want)
		}
	}
}
