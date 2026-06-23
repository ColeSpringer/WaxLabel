package waxlabel_test

import (
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestWAVMultiValueNativeReduced verifies that a WAV retaining both LIST/INFO
// and ID3 stores only the first native INFO value while keeping the full set in
// ID3. The plan surfaces that as native-value-reduced; a single-value edit does
// not warn.
func TestWAVMultiValueNativeReduced(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"IART", "A"}), wavData(400))

	multi := prepareWith(t, data, func(e *wl.Editor) { e.Set(tag.Artist, "A", "B", "C") })
	if !planWarns(t, multi, wl.WarnNativeValueReduced) {
		t.Errorf("multi-value ARTIST should warn native-value-reduced; got %v", multi.Report().Warnings)
	}

	single := prepareWith(t, data, func(e *wl.Editor) { e.Set(tag.Artist, "Solo") })
	if planWarns(t, single, wl.WarnNativeValueReduced) {
		t.Errorf("single-value ARTIST must not warn native-value-reduced; got %v", single.Report().Warnings)
	}
}

// TestAIFFMultiValueNativeReduced verifies that a multi-value ARTIST reduces the
// single-valued AUTH text chunk to its first value while keeping the full set in
// ID3. Comment maps to repeatable ANNO chunks, so several Comment values write
// without reduction even when a picture forces ID3 to be written too.
func TestAIFFMultiValueNativeReduced(t *testing.T) {
	data := aiffFile("AIFF", stdCOMM(), aiffText("AUTH", "A"), aiffSSND(400))

	multi := prepareWith(t, data, func(e *wl.Editor) { e.Set(tag.Artist, "A", "B") })
	if !planWarns(t, multi, wl.WarnNativeValueReduced) {
		t.Errorf("multi-value ARTIST should warn native-value-reduced; got %v", multi.Report().Warnings)
	}

	// Comment maps to repeatable ANNO chunks, so multi-value Comment is not reduced.
	// Add a picture so the ID3 chunk is emitted alongside the text chunks.
	comments := prepareWith(t, data, func(e *wl.Editor) {
		e.Set(tag.Comment, "one", "two")
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()})
	})
	if planWarns(t, comments, wl.WarnNativeValueReduced) {
		t.Errorf("multi-value Comment (ANNO) must not warn native-value-reduced; got %v", comments.Report().Warnings)
	}
}
