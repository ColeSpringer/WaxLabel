package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"
)

// mp4WithTrailingMdats builds a parseable MP4 (ftyp + audio-only moov + audio mdat) and
// appends nLeaked extra "dead" mdats after the audio - the shape an older build leaked by
// appending a fresh QuickTime-chapter mdat on every chapter edit and never reclaiming the
// prior one. There is no chapter track, so this is the cleared-but-leaked layout the
// parse-side fix must still treat as audio-only. The audio stco points at the first
// (audio) mdat's payload.
func mp4WithTrailingMdats(nLeaked int) []byte {
	audio := bytes.Repeat([]byte{0xA7}, 120)
	dead := bytes.Repeat([]byte{0xCC}, 48)
	build := func(stcoOff uint32) []byte {
		parts := [][]byte{mp4Ftyp(), mp4Moov(nil, stcoOff), mp4Atom("mdat", audio)}
		for i := 0; i < nLeaked; i++ {
			parts = append(parts, mp4Atom("mdat", dead))
		}
		return slices.Concat(parts...)
	}
	tmp := build(0)
	j := bytes.Index(tmp, []byte("mdat"))
	return build(uint32(j + 4)) // audio stco -> first (audio) mdat payload
}

// TestMP4EssenceIgnoresLeakedChapterMdats verifies that the audio essence is exactly
// the mdats that contain an audio-track chunk offset, so several dead chapter mdats an
// older build leaked (with no surviving chapter track) are excluded from the digest. It
// therefore equals a clean single-mdat file's digest and stays byte-stable. This is the
// legacy-leak path the going-forward chapter-edit tests do not reach.
func TestMP4EssenceIgnoresLeakedChapterMdats(t *testing.T) {
	clean := mp4WithTrailingMdats(0)
	leaked := mp4WithTrailingMdats(3)

	// Sanity: the leak really is present (4 mdat atoms vs the clean file's 1).
	if got := bytes.Count(leaked, []byte("mdat")); got != 4 {
		t.Fatalf("leaked fixture mdat count = %d, want 4", got)
	}
	if got := bytes.Count(clean, []byte("mdat")); got != 1 {
		t.Fatalf("clean fixture mdat count = %d, want 1", got)
	}

	cleanDigest := essenceOf(t, clean)
	leakedDigest := essenceOf(t, leaked)
	if !cleanDigest.Equal(leakedDigest) {
		t.Errorf("essence differs between clean and leaked files: the dead chapter mdats were hashed as audio\n clean=%x\nleaked=%x",
			cleanDigest.Sum, leakedDigest.Sum)
	}
	// Deterministic and stable across recomputation.
	if again := essenceOf(t, leaked); !again.Equal(leakedDigest) {
		t.Errorf("essence not stable across recomputation of the leaked file")
	}
}

// mp4TwoAudioTwoMdat builds a parseable MP4 with two independent audio (soun) tracks whose
// chunks live in two separate mdats: track 1 -> mdat1, track 2 -> mdat2. It models the
// multi-track shape whose second audio track the essence digest must still cover.
func mp4TwoAudioTwoMdat(audio1, audio2 []byte) []byte {
	build := func(stco1, stco2 uint32) []byte {
		moov := mp4Atom("moov", slices.Concat(mp4SounTrak(1, stco1), mp4SounTrak(2, stco2)))
		return slices.Concat(mp4Ftyp(), moov, mp4Atom("mdat", audio1), mp4Atom("mdat", audio2))
	}
	tmp := build(0, 0)
	o1 := bytes.Index(tmp, []byte("mdat")) + 4           // payload of the first mdat
	o2 := bytes.Index(tmp[o1:], []byte("mdat")) + o1 + 4 // payload of the second mdat
	return build(uint32(o1), uint32(o2))
}

// TestMP4EssenceCoversSecondAudioTrack verifies that with two audio tracks in two mdats, the
// audio-essence digest hashes both mdats. Filtering on only the first audio track's
// offset table dropped the second track's mdat, so a change confined to it compared equal
// (a verify/dedup false negative). The digest must differ when only the second track's
// samples change.
func TestMP4EssenceCoversSecondAudioTrack(t *testing.T) {
	base := mp4TwoAudioTwoMdat(bytes.Repeat([]byte{0x11}, 64), bytes.Repeat([]byte{0x22}, 64))
	alt := mp4TwoAudioTwoMdat(bytes.Repeat([]byte{0x11}, 64), bytes.Repeat([]byte{0x33}, 64))

	// Sanity: the two distinct mdats really are present and the files are the same size.
	if got := bytes.Count(base, []byte("mdat")); got != 2 {
		t.Fatalf("fixture mdat count = %d, want 2", got)
	}
	if len(base) != len(alt) {
		t.Fatalf("setup: fixtures differ in length (%d vs %d)", len(base), len(alt))
	}
	if d := essenceOf(t, base); d.Equal(essenceOf(t, alt)) {
		t.Error("essence digest ignored the second audio track's mdat: a change there compared equal")
	}
}
