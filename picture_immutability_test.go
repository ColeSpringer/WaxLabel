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
		return false // keep the picture; we are only probing the aliasing
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
		for n := 0; n < iters; n++ {
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
		for n := 0; n < iters; n++ {
			for _, p := range doc.Pictures() {
				_ = p.Data
			}
		}
	}()
	wg.Wait()
}
