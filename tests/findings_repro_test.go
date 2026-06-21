package waxlabel_test

import (
	"encoding/binary"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// planWarns reports whether a prepared plan's report carries the given warning code.
func planWarns(t *testing.T, plan *wl.Plan, code wl.WarningCode) bool {
	t.Helper()
	for _, w := range plan.Report().Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// prepareWith parses bytes and prepares an edit applied by the given closure.
func prepareWith(t *testing.T, src []byte, edit func(*wl.Editor)) *wl.Plan {
	t.Helper()
	ed := mustParseBytes(t, src).Edit()
	edit(ed)
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return plan
}

// TestPictureWarningsScopedToAddedPictures pins C1: the edit-time picture warnings
// fire only for pictures this edit authored, never for a file's pre-existing art
// (which stays the linter's whole-set concern). Adding a second front cover warns;
// adding an unrelated picture to a file that already had two front covers does not,
// and a tags-only edit on it stays silent.
func TestPictureWarningsScopedToAddedPictures(t *testing.T) {
	t.Parallel()
	frontA := tinyPNG()
	frontB := append(slices.Clone(tinyPNG()), 0x01) // distinct bytes, still a valid PNG header
	back := append(slices.Clone(tinyPNG()), 0x02)

	// Adding a second front cover (the set then holds two) warns.
	oneFront := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: frontA})
	})
	addAnotherFront := prepareWith(t, oneFront, func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: frontB})
	})
	if !planWarns(t, addAnotherFront, wl.WarnMultipleFrontCovers) {
		t.Error("adding a 2nd front cover should warn multiple-front-covers")
	}

	// A file that already carries two front covers the user did not touch: adding a
	// back cover must NOT warn multiple-front-covers (the fronts are pre-existing).
	twoFronts := writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: frontA})
		e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: frontB})
	})
	addBack := prepareWith(t, twoFronts, func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicBackCover, Data: back})
	})
	if planWarns(t, addBack, wl.WarnMultipleFrontCovers) {
		t.Error("adding a back cover must not flag the pre-existing front covers")
	}

	// A tags-only edit on the two-front file touches no pictures, so no picture
	// warning is emitted at all.
	tagsOnly := prepareWith(t, twoFronts, func(e *wl.Editor) { e.Set(tag.Title, "X") })
	if planWarns(t, tagsOnly, wl.WarnMultipleFrontCovers) {
		t.Error("a tags-only edit must not flag pre-existing pictures")
	}

	// Adding a picture whose bytes duplicate a pre-existing one warns duplicate-picture.
	addDup := prepareWith(t, oneFront, func(e *wl.Editor) {
		e.AddPicture(wl.Picture{Type: wl.PicBackCover, Data: slices.Clone(frontA)})
	})
	if !planWarns(t, addDup, wl.WarnDuplicatePicture) {
		t.Error("adding a picture identical to a pre-existing one should warn duplicate-picture")
	}
}

// truncatedSSND builds an SSND chunk header that declares declaredBody bytes but
// supplies only presentBody (< declaredBody) and no word-align pad - the on-disk
// shape of a cut-off AIFF, where the sound chunk runs past the end of the file.
func truncatedSSND(declaredBody, presentBody int) []byte {
	out := append([]byte("SSND"), aiffBE32(declaredBody)...)
	return append(out, make([]byte, presentBody)...)
}

// truncatedSSNDOffset is truncatedSSND with a non-zero SSND `offset` field (the
// block-alignment bytes that precede the first sample frame), written into the first
// 4 bytes of the body.
func truncatedSSNDOffset(declaredBody int, offset uint32, presentBody int) []byte {
	out := append([]byte("SSND"), aiffBE32(declaredBody)...)
	body := make([]byte, presentBody)
	binary.BigEndian.PutUint32(body[0:4], offset) // offset field; blockSize stays 0
	return append(out, body...)
}

// TestAIFFTruncatedDurationRecomputed pins C3: a truncated PCM AIFF reports the
// duration its surviving SSND bytes decode to (like WAV), while a truncated
// compressed AIFF-C keeps COMM's declared frame count (its bytes do not map linearly
// to frames). Both still flag truncated-audio.
func TestAIFFTruncatedDurationRecomputed(t *testing.T) {
	t.Parallel()
	const (
		channels, sampleSize = 2, 16
		rate                 = uint32(44100)
		declaredFrames       = 1000
		presentFrames        = 100
		frameSize            = channels * sampleSize / 8 // 4 bytes/frame
	)
	declaredBody := 8 + declaredFrames*frameSize
	presentBody := 8 + presentFrames*frameSize
	dur := func(frames int) time.Duration {
		return time.Duration(float64(frames) / float64(rate) * float64(time.Second))
	}
	const tol = 2 * time.Millisecond
	near := func(got, want time.Duration) bool { d := got - want; return d < tol && d > -tol }

	// Full reference: every declared frame is present, no truncation.
	full := mustParseBytes(t, aiffFile("AIFF",
		aiffCOMM(channels, declaredFrames, sampleSize, rate),
		aiffSSND(declaredFrames*frameSize)))
	if hasWarning(full, wl.WarnTruncatedAudio) {
		t.Error("a full AIFF should not flag truncated-audio")
	}
	if got := full.Properties().Duration(); !near(got, dur(declaredFrames)) {
		t.Errorf("full duration = %v, want ~%v", got, dur(declaredFrames))
	}

	// Truncated PCM: the duration follows the present bytes, not COMM's numFrames.
	pcm := mustParseBytes(t, aiffFile("AIFF",
		aiffCOMM(channels, declaredFrames, sampleSize, rate),
		truncatedSSND(declaredBody, presentBody)))
	if !hasWarning(pcm, wl.WarnTruncatedAudio) {
		t.Error("a truncated PCM AIFF should flag truncated-audio")
	}
	if got := pcm.Properties().Duration(); !near(got, dur(presentFrames)) {
		t.Errorf("truncated PCM duration = %v, want ~%v (recomputed from present bytes)", got, dur(presentFrames))
	}

	// Truncated compressed AIFF-C (ima4): COMM's declared count is kept, since the
	// bytes are not a constant size per frame.
	aifc := mustParseBytes(t, aiffFile("AIFC",
		aiffCOMMC(channels, declaredFrames, sampleSize, rate, "ima4"),
		truncatedSSND(declaredBody, presentBody)))
	if !hasWarning(aifc, wl.WarnTruncatedAudio) {
		t.Error("a truncated AIFF-C should still flag truncated-audio")
	}
	if got := aifc.Properties().Duration(); !near(got, dur(declaredFrames)) {
		t.Errorf("truncated AIFF-C duration = %v, want ~%v (declared frames kept)", got, dur(declaredFrames))
	}

	// SSND body truncated to fewer than the 8-byte sub-header: no sample bytes survive,
	// so 0 frames (a zero-length duration) - not a phantom frame from counting the
	// partial sub-header as audio.
	stub := mustParseBytes(t, aiffFile("AIFF",
		aiffCOMM(channels, declaredFrames, sampleSize, rate),
		truncatedSSND(declaredBody, 4))) // 4 bytes of body, below ssndHeaderLen (8)
	if got := stub.Properties().Duration(); got != 0 {
		t.Errorf("sub-sub-header SSND duration = %v, want 0 (no decodable frames)", got)
	}

	// A non-zero SSND offset declares alignment bytes that precede the first sample
	// frame; they must not be counted as audio. Body = 8 sub-header + 16 offset + 10
	// frames (40 bytes): the recompute must yield 10 frames, not (64-8)/4 = 14.
	const align, presentSampleFrames = 16, 10
	offBody := ssndHeaderLenBytes + align + presentSampleFrames*frameSize
	withOffset := mustParseBytes(t, aiffFile("AIFF",
		aiffCOMM(channels, declaredFrames, sampleSize, rate),
		truncatedSSNDOffset(declaredBody, align, offBody)))
	if got := withOffset.Properties().Duration(); !near(got, dur(presentSampleFrames)) {
		t.Errorf("truncated PCM with SSND offset: duration = %v, want ~%v (offset bytes excluded)", got, dur(presentSampleFrames))
	}
}

// ssndHeaderLenBytes mirrors the codec's ssndHeaderLen (the SSND offset+blockSize
// sub-header size) for the synthetic builders above.
const ssndHeaderLenBytes = 8
