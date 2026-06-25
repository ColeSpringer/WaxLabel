package waxlabel_test

import (
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// Files built from floods of minimum-size metadata elements must be rejected with
// ErrSizeTooLarge once the per-parse element count crosses Limits.MaxElements
// (default 100000), instead of growing one descriptor per element. The inputs are
// generated in-test and stay under about 1 MB because the cap fires on element count,
// not byte size.
const overCap = 100001

// repeatConcat returns elem repeated n times in a single pre-sized buffer.
func repeatConcat(prefix, elem []byte, n int) []byte {
	out := make([]byte, 0, len(prefix)+n*len(elem))
	out = append(out, prefix...)
	for i := 0; i < n; i++ {
		out = append(out, elem...)
	}
	return out
}

// TestPartialLimitsKeepElementCap verifies that a caller passing a partial Limits via WithLimits
// (e.g. only MaxDepth) must still get the default element cap - a zero field means "use the
// default", not "unlimited" - so the DoS protection is not silently disabled.
func TestPartialLimitsKeepElementCap(t *testing.T) {
	emptyChunk := append([]byte("JUNK"), wavLE32(0)...)
	body := repeatConcat([]byte("WAVE"), emptyChunk, overCap)
	data := append(append([]byte("RIFF"), wavLE32(len(body))...), body...)

	// Only MaxDepth is set; MaxElements is left zero and must be backfilled to the default.
	_, err := wl.Parse(context.Background(), wl.BytesSource(data), wl.WithLimits(wl.Limits{MaxDepth: 8}))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("partial WithLimits should keep the element cap: err = %v, want ErrSizeTooLarge", err)
	}
}

func TestWAVChunkCountCapped(t *testing.T) {
	// RIFF/WAVE then overCap empty 8-byte "JUNK" chunks.
	emptyChunk := append([]byte("JUNK"), wavLE32(0)...)
	body := repeatConcat([]byte("WAVE"), emptyChunk, overCap)
	data := append(append([]byte("RIFF"), wavLE32(len(body))...), body...)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Parse of %d-chunk WAV: err = %v, want ErrSizeTooLarge", overCap, err)
	}
}

func TestAIFFChunkCountCapped(t *testing.T) {
	// FORM/AIFF then overCap empty 8-byte "JUNK" chunks.
	emptyChunk := append([]byte("JUNK"), aiffBE32(0)...)
	body := repeatConcat([]byte("AIFF"), emptyChunk, overCap)
	data := append(append([]byte("FORM"), aiffBE32(len(body))...), body...)

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
	freeAtom := append(mp4be32(8), "free"...)
	data := repeatConcat(mp4Ftyp(), freeAtom, overCap)

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
	empty := textFrame(3, "TIT2", "")
	for i := range frames {
		frames[i] = empty
	}
	data := append(id3v2(3, frames...), mp3Audio(t)...)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Parse of %d-frame ID3: err = %v, want ErrSizeTooLarge", overCap, err)
	}
}

// TestVorbisCommentCountCapped verifies that a Vorbis comment list declares an
// attacker-controlled uint32 count; for Ogg the comment packet is bounded only by the alloc
// limit, so a body packed with minimum entries would amplify into one Comment descriptor
// each to OOM. ParseCommentList (shared by FLAC and Ogg) must trip the element cap. The FLAC
// path wires it; the entries carry '=' so each is actually stored (and counted).
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

// TestMatroskaMetadataElementCapped verifies that a Tags element packed with minimum-size
// empty SimpleTags is metadata-granularity (unlike clusters), so the EBML walk must trip the
// element cap rather than accumulate one descriptor each to OOM. The cap rides the depth
// guard's breadth budget, which eachChild (the metadata walk) counts and walkSegment (the
// cluster walk) does not - see TestMatroskaManyClustersParseUncapped for the exempt side.
func TestMatroskaMetadataElementCapped(t *testing.T) {
	simples := repeatConcat(nil, mkEl(idSimpleTag, nil), overCap)
	seg := mkEl(idSegment, mkEl(idTags, mkEl(idTag, simples)))
	data := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), seg)

	_, err := wl.Parse(context.Background(), wl.BytesSource(data))
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Parse of %d-SimpleTag Matroska: err = %v, want ErrSizeTooLarge", overCap, err)
	}
}

// TestWavAiffID3FrameCapErrors verifies that a WAV/AIFF id3 chunk whose frame count exceeds
// MaxElements surfaces ErrSizeTooLarge instead of being swallowed by the tolerant
// "is this chunk a tag?" guard (which would treat a structurally-valid id3 chunk as absent
// and rewrite the file without it). Symmetric with the MP3/AAC front-tag path, which errors.
// A genuinely malformed chunk still falls through to the native LIST/INFO fallback (covered
// elsewhere); this is specifically the bounded-allocation cap breach.
func TestWavAiffID3FrameCapErrors(t *testing.T) {
	frames := make([][]byte, overCap)
	empty := textFrame(3, "TIT2", "")
	for i := range frames {
		frames[i] = empty
	}
	tag := id3v2(3, frames...)
	cases := map[string][]byte{
		"wav":  wavFile(wavFmtPCM(), wavID3(tag), wavData(400)),
		"aiff": aiffFile("AIFF", stdCOMM(), aiffSSND(400), aiffID3(tag)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); !errors.Is(err, waxerr.ErrSizeTooLarge) {
				t.Fatalf("%s id3 chunk over the frame cap: err = %v, want ErrSizeTooLarge (not a silent drop)", name, err)
			}
		})
	}
}

// TestMatroskaManyClustersParseUncapped guards against someone later
// over-applying the element cap to an audio-granularity loop: a Matroska level-1
// Cluster occurs once per audio packet group, so a long file legitimately has
// hundreds of thousands. Far more than MaxElements clusters must still parse
// cleanly. (The Ogg-page analogue is internal/ogg's TestScanPagesManyPagesUncapped.)
func TestMatroskaManyClustersParseUncapped(t *testing.T) {
	const n = overCap + 20000 // comfortably past the cap an audio loop must not honor
	cluster := mkAudioCluster()
	seg := mkEl(idInfo, mkStr(idSegTitle, "x"))
	for i := 0; i < n; i++ {
		seg = append(seg, cluster...)
	}
	data := concat(mkEl(idEBML, mkStr(idDocType, "matroska")), mkEl(idSegment, seg))

	if _, err := wl.Parse(context.Background(), wl.BytesSource(data)); err != nil {
		t.Fatalf("Parse of %d-cluster Matroska: %v (the element cap must not apply to clusters)", n, err)
	}
}
