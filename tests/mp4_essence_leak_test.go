package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
)

// mp4WithTrailingMdats builds a parseable MP4 (ftyp + audio-only moov + audio mdat) and appends
// nLeaked extra "dead" mdats after the audio, the shape left by a chapter writer that appended a
// fresh QuickTime-chapter mdat on every edit without reclaiming the prior one.
func mp4WithTrailingMdats(nLeaked int) []byte {
	audio := bytes.Repeat([]byte{0xA7}, 120)
	dead := bytes.Repeat([]byte{0xCC}, 48)
	build := func(stcoOff uint32) []byte {
		parts := [][]byte{testfile.MP4Ftyp(), mp4Moov(nil, stcoOff), testfile.MP4Atom("mdat", audio)}
		for range nLeaked {
			parts = append(parts, testfile.MP4Atom("mdat", dead))
		}
		return slices.Concat(parts...)
	}
	tmp := build(0)
	j := bytes.Index(tmp, []byte("mdat"))
	return build(uint32(j + 4)) // audio stco -> first (audio) mdat payload
}

// The audio essence is exactly the mdats that contain an audio-track chunk offset, so leaked
// dead chapter mdats (with no surviving chapter track) are excluded from the digest, which
// equals a clean single-mdat file's digest.
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
		moov := testfile.MP4Atom("moov", slices.Concat(testfile.MP4SoundTrak(1, stco1), testfile.MP4SoundTrak(2, stco2)))
		return slices.Concat(testfile.MP4Ftyp(), moov, testfile.MP4Atom("mdat", audio1), testfile.MP4Atom("mdat", audio2))
	}
	tmp := build(0, 0)
	o1 := bytes.Index(tmp, []byte("mdat")) + 4           // payload of the first mdat
	o2 := bytes.Index(tmp[o1:], []byte("mdat")) + o1 + 4 // payload of the second mdat
	return build(uint32(o1), uint32(o2))
}

// With two audio tracks in two mdats, the audio-essence digest hashes both. Filtering on only
// the first track's offset table would drop the second track's mdat, so a change confined to it
// would compare equal (a verify/dedup false negative).
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

// When no chunk offset lands in an mdat every mdat is hashed whole, but never one the chapter
// track's samples fill, so a chapter write leaves that digest alone: also where the mdat it
// replaces does not end the file and so stays, as free space.
func TestMP4EssenceFallbackExcludesChapterMdat(t *testing.T) {
	// The audio track's one chunk offset points into ftyp.
	base := slices.Concat(testfile.MP4Ftyp(), mp4Moov(nil, 4), testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 120)))
	chaptered, err := mustParseBytes(t, base).Edit().SetChapters(chaptersFromTitles("A", "B")...).Prepare()
	if err != nil {
		t.Fatal(err)
	}
	written := applyToBytes(t, base, chaptered)
	for name, data := range map[string][]byte{
		"no chapters":                base,
		"chapter mdat ahead of free": slices.Concat(written, testfile.MP4Atom("free", nil)),
		"chapter mdat ahead of mdat": slices.Concat(written, testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xC3}, 40))),
	} {
		t.Run(name, func(t *testing.T) {
			essence := essenceOf(t, data)
			out := data
			for _, titles := range [][]string{{"C", "D"}, {"E"}, nil} {
				plan, err := mustParseBytes(t, out).Edit().SetChapters(chaptersFromTitles(titles...)...).Prepare(wl.WithVerifyEssence())
				if err != nil {
					t.Fatal(err)
				}
				out = applyToBytes(t, out, plan)
				if re := mustParseBytes(t, out); len(re.Chapters()) != len(titles) || !essenceOf(t, out).Equal(essence) {
					t.Errorf("chapters %q: %d read back, digest equal %v", titles, len(re.Chapters()), essenceOf(t, out).Equal(essence))
				}
			}
		})
	}
}
