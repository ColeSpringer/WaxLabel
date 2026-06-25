package waxlabel_test

import (
	"testing"

	wl "github.com/colespringer/waxlabel"
)

// apicFrameRaw wraps a (possibly malformed) APIC body in a v2.3 frame header so a
// hand-built tag can carry an undecodable cover.
func apicFrameRaw(body []byte) []byte {
	out := append([]byte("APIC"), byte(len(body)>>24), byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	out = append(out, 0, 0) // frame flags
	return append(out, body...)
}

// TestID3MalformedCoverDroppedWarns verifies that a picture edit re-emits the edited cover set and
// drops the original APIC frames. A malformed original (one decodeAPIC cannot read) is
// therefore lost, so the write must surface an invalid-picture warning rather than dropping
// it silently. A picture edit with no malformed original must not warn.
func TestID3MalformedCoverDroppedWarns(t *testing.T) {
	addCover := func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()})
	}

	// 0xFF is not a valid ID3 text-encoding byte, so decodeAPIC rejects this body.
	malformed := append(id3v2(3, apicFrameRaw([]byte{0xFF, 0x00, 0x00, 0x00}), textFrame(3, "TIT2", "T")), mp3Audio(t)...)
	plan := prepareWith(t, malformed, addCover)
	if !planWarns(t, plan, wl.WarnInvalidPicture) {
		t.Errorf("dropping a malformed cover on a picture edit must warn invalid-picture; got %v", plan.Report().Warnings)
	}

	// A picture edit on a file whose only original frames decode cleanly must not warn.
	clean := append(id3v2(3, textFrame(3, "TIT2", "T")), mp3Audio(t)...)
	planClean := prepareWith(t, clean, addCover)
	if planWarns(t, planClean, wl.WarnInvalidPicture) {
		t.Errorf("a picture edit with no malformed original must not warn invalid-picture; got %v", planClean.Report().Warnings)
	}
}
