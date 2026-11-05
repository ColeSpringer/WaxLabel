package waxlabel_test

import (
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
)

// A picture edit re-emits the edited cover set and drops the original APIC frames. A malformed
// original (one decodeAPIC cannot read) is therefore lost, so the write must warn invalid-picture.
func TestID3MalformedCoverDroppedWarns(t *testing.T) {
	addCover := func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: testfile.PNG()})
	}

	// 0xFF is not a valid ID3 text-encoding byte, so decodeAPIC rejects this body.
	malformed := append(testfile.ID3v2(3, testfile.ID3Frame(3, "APIC", []byte{0xFF, 0x00, 0x00, 0x00}), testfile.TextFrame(3, "TIT2", "T")), mp3Audio(t)...)
	plan := prepareWith(t, malformed, addCover)
	if !planWarns(t, plan, wl.WarnInvalidPicture) {
		t.Errorf("dropping a malformed cover on a picture edit must warn invalid-picture; got %v", plan.Report().Warnings)
	}

	// A picture edit on a file whose only original frames decode cleanly must not warn.
	clean := append(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "T")), mp3Audio(t)...)
	planClean := prepareWith(t, clean, addCover)
	if planWarns(t, planClean, wl.WarnInvalidPicture) {
		t.Errorf("a picture edit with no malformed original must not warn invalid-picture; got %v", planClean.Report().Warnings)
	}
}
