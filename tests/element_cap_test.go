package waxlabel_test

import (
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/waxerr"
)

// Files built from floods of minimum-size metadata elements must be rejected with ErrSizeTooLarge
// once the per-parse element count crosses Limits.MaxElements (default 100000).
const overCap = 100001

// repeatConcat returns elem repeated n times in a single pre-sized buffer.
func repeatConcat(prefix, elem []byte, n int) []byte {
	out := make([]byte, 0, len(prefix)+n*len(elem))
	out = append(out, prefix...)
	for range n {
		out = append(out, elem...)
	}
	return out
}

// A caller passing a partial Limits via WithLimits (e.g. only MaxDepth) must still get the default
// element cap: a zero field means "use the default", not "unlimited".
func TestPartialLimitsKeepElementCap(t *testing.T) {
	emptyChunk := append([]byte("JUNK"), testfile.LE32(0)...)
	body := repeatConcat([]byte("WAVE"), emptyChunk, overCap)
	data := append(append([]byte("RIFF"), testfile.LE32(len(body))...), body...)

	// Only MaxDepth is set; MaxElements is left zero and must be backfilled to the default.
	_, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxDepth: 8}))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("partial WithLimits should keep the element cap: err = %v, want ErrSizeTooLarge", err)
	}
}

func TestWAVChunkCountCapped(t *testing.T) {
	// RIFF/WAVE then overCap empty 8-byte "JUNK" chunks.
	emptyChunk := append([]byte("JUNK"), testfile.LE32(0)...)
	body := repeatConcat([]byte("WAVE"), emptyChunk, overCap)
	data := append(append([]byte("RIFF"), testfile.LE32(len(body))...), body...)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Parse of %d-chunk WAV: err = %v, want ErrSizeTooLarge", overCap, err)
	}
}

func TestAIFFChunkCountCapped(t *testing.T) {
	// FORM/AIFF then overCap empty 8-byte "JUNK" chunks.
	emptyChunk := append([]byte("JUNK"), testfile.BE32(0)...)
	body := repeatConcat([]byte("AIFF"), emptyChunk, overCap)
	data := append(append([]byte("FORM"), testfile.BE32(len(body))...), body...)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Parse of %d-chunk AIFF: err = %v, want ErrSizeTooLarge", overCap, err)
	}
}

func TestFLACBlockCountCapped(t *testing.T) {
	// "fLaC" then overCap minimum 4-byte PADDING blocks (code 1, not-last, len 0).
	block := []byte{0x01, 0x00, 0x00, 0x00}
	data := repeatConcat([]byte("fLaC"), block, overCap)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Parse of %d-block FLAC: err = %v, want ErrSizeTooLarge", overCap, err)
	}
}

func TestMP4AtomCountCapped(t *testing.T) {
	// A valid ftyp (so detection routes to MP4) then overCap empty 8-byte "free"
	// atoms; the per-parse atom counter trips before the missing-moov check.
	freeAtom := append(testfile.BE32(8), "free"...)
	data := repeatConcat(testfile.MP4Ftyp(), freeAtom, overCap)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Parse of %d-atom MP4: err = %v, want ErrSizeTooLarge", overCap, err)
	}
}

func TestID3FrameCountCapped(t *testing.T) {
	// An MP3 whose front ID3v2.3 tag carries overCap minimum text frames. The
	// front-tag path (ReadFront) propagates the cap error, unlike the WAV/AIFF
	// id3-chunk path which tolerates a tag that fails to parse.
	frames := make([][]byte, overCap)
	empty := testfile.TextFrame(3, "TIT2", "")
	for i := range frames {
		frames[i] = empty
	}
	data := append(testfile.ID3v2(3, frames...), mp3Audio(t)...)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Parse of %d-frame ID3: err = %v, want ErrSizeTooLarge", overCap, err)
	}
}

// Vorbis comment list declares an attacker-controlled uint32 count; for Ogg the comment packet is
// bounded only by the alloc limit, so a body packed with minimum entries would amplify into one
// Comment descriptor each to OOM. ParseCommentList (shared by FLAC and Ogg) must trip the element
// cap.
func TestVorbisCommentCountCapped(t *testing.T) {
	entries := make([]string, overCap)
	for i := range entries {
		entries[i] = "X=" // minimal stored entry (has '=')
	}
	data := flacWithComments(entries...)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Parse of %d-comment FLAC: err = %v, want ErrSizeTooLarge", overCap, err)
	}
}

// A Tags element packed with minimum-size empty SimpleTags is metadata-granularity (unlike
// clusters), so the EBML walk must trip the element cap.
func TestMatroskaMetadataElementCapped(t *testing.T) {
	simples := repeatConcat(nil, testfile.El(testfile.IDSimpleTag, nil), overCap)
	seg := testfile.El(testfile.IDSegment, testfile.El(testfile.IDTags, testfile.El(testfile.IDTag, simples)))
	data := concat(testfile.EBMLHeader("matroska"), seg)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Parse of %d-SimpleTag Matroska: err = %v, want ErrSizeTooLarge", overCap, err)
	}
}

// A WAV/AIFF id3 chunk whose frame count exceeds MaxElements surfaces ErrSizeTooLarge; the
// tolerant "is this chunk a tag?" check must not swallow it, or a structurally valid id3 chunk
// would be treated as absent and dropped on rewrite.
func TestWavAiffID3FrameCapErrors(t *testing.T) {
	frames := make([][]byte, overCap)
	empty := testfile.TextFrame(3, "TIT2", "")
	for i := range frames {
		frames[i] = empty
	}
	tag := testfile.ID3v2(3, frames...)
	cases := map[string][]byte{
		"wav":  testfile.WAV(testfile.WAVFmtPCM(), wavID3(tag), testfile.WAVData(400)),
		"aiff": testfile.AIFF("AIFF", stdCOMM(), stdSSND(), aiffID3(tag)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); !errors.Is(err, waxerr.ErrSizeTooLarge) {
				t.Fatalf("%s id3 chunk over the frame cap: err = %v, want ErrSizeTooLarge (not a silent drop)", name, err)
			}
		})
	}
}

// The element cap must not apply to an audio-granularity loop: a Matroska level-1 Cluster occurs
// once per audio packet group, so a long file has hundreds of thousands. Far more than MaxElements
// clusters must still parse.
func TestMatroskaManyClustersParseUncapped(t *testing.T) {
	const n = overCap + 20000 // comfortably past the cap an audio loop must not honor
	cluster := testfile.AudioCluster(0)
	seg := testfile.El(testfile.IDInfo, mkStr(testfile.IDSegTitle, "x"))
	for range n {
		seg = append(seg, cluster...)
	}
	data := concat(testfile.EBMLHeader("matroska"), testfile.El(testfile.IDSegment, seg))

	if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); err != nil {
		t.Fatalf("Parse of %d-cluster Matroska: %v (the element cap must not apply to clusters)", n, err)
	}
}
