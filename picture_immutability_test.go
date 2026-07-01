package waxlabel

import (
	"sync"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// docWithCover builds a Document carrying one embedded front cover whose Data begins with the
// given byte, for the RemovePictures immutability tests. Edit() seeds the editor via the shallow
// core.ClonePictures, so the editor's picture Data aliases this Document's backing array - which is
// exactly the sharing RemovePictures must not leak to a user predicate.
func docWithCover(first byte) *Document {
	return &Document{media: &core.Media{
		Format:   core.FormatFLAC,
		Pictures: []core.Picture{{Type: core.PicFrontCover, MIME: "image/jpeg", Data: []byte{first, 0xBB, 0xCC}}},
	}}
}

// TestRemovePicturesMatchCannotMutateDocument covers the F3 fix: RemovePictures is the only editor
// method that hands a Picture to user code, and Edit() seeds the editor with Data aliasing the
// immutable Document. A match predicate that writes p.Data must therefore not reach the Document's
// bytes - RemovePictures hands match a Data-detached copy.
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

// TestRemovePicturesNoRaceWithPictures is the -race regression for F3: a mutating RemovePictures
// predicate running concurrently with doc.Pictures() reads must not race on shared picture bytes.
// Before the fix, match received Data aliasing the Document, so its writes raced the reader's copy;
// after it, match writes only its own detached probe, so there is no shared write. It passes with
// or without -race, but only -race proves the absence of the data race.
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
