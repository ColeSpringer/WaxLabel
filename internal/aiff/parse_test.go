package aiff

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// aiffComm is a minimal COMM chunk: 2 channels, 8 frames, 16-bit, 44100 Hz.
func aiffComm() []byte { return testfile.COMM(2, 8, 16, 44100) }

// aiffSsnd is an SSND chunk holding 16 audio bytes.
func aiffSsnd() []byte { return testfile.SSNDOffset(0, nil, bytes.Repeat([]byte{0x11, 0x22}, 8)) }

// formWrap wraps inner chunks (without the "AIFF" tag) in a FORM/AIFF header whose
// declared size covers innerExtra, then appends any outer bytes after it.
func formWrap(chunks, innerExtra, outer []byte) []byte {
	return slices.Concat(testfile.AIFF("AIFF", chunks, innerExtra), outer)
}

func parseAIFFDoc(t *testing.T, src []byte) *doc {
	t.Helper()
	m, err := parse(context.Background(), core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m.Native.(*doc)
}

func findAIFFChunk(d *doc, id string) *chunk {
	for i := range d.chunks {
		if string(d.chunks[i].id[:]) == id {
			return &d.chunks[i]
		}
	}
	return nil
}

// TestAIFFChunkPaddingAndOuterBoundary checks the parse-time structural rules: an
// odd-length chunk excludes its word-alignment pad from the declared length yet the next
// chunk begins after the pad, and bytes past formSize are captured as the outer region.
func TestAIFFChunkPaddingAndOuterBoundary(t *testing.T) {
	odd := []byte{1, 2, 3, 4, 5} // 5-byte body -> one pad byte
	chunks := bytes.Join([][]byte{
		aiffComm(),
		testfile.AIFFChunk("ANNO", odd),
		aiffSsnd(),
	}, nil)
	outer := bytes.Repeat([]byte{0xAB}, 10) // appended outside the FORM size
	src := formWrap(chunks, nil, outer)

	d := parseAIFFDoc(t, src)
	anno := findAIFFChunk(d, "ANNO")
	ssnd := findAIFFChunk(d, "SSND")
	if anno == nil || ssnd == nil {
		t.Fatalf("expected COMM/ANNO/SSND chunks, got %d chunks", len(d.chunks))
	}
	if anno.bodyLen != 5 {
		t.Errorf("ANNO bodyLen = %d, want 5 (pad excluded from the declared length)", anno.bodyLen)
	}
	if want := anno.bodyOff + 5 + 1 + 8; ssnd.bodyOff != want {
		t.Errorf("SSND bodyOff = %d, want %d (next chunk starts past the pad byte)", ssnd.bodyOff, want)
	}
	if d.outerLen != int64(len(outer)) {
		t.Errorf("outerLen = %d, want %d (bytes past formSize captured as outer)", d.outerLen, len(outer))
	}
	if d.trailingLen != 0 {
		t.Errorf("trailingLen = %d, want 0 (no leftover inside the FORM size)", d.trailingLen)
	}
}

// TestAIFFTrailerSurvivesEditByteForByte checks that a contiguous ID3v1 "TAG" trailer
// survives an unrelated edit byte-for-byte, whether a writer counted it inside the FORM
// size or appended it after the FORM.
func TestAIFFTrailerSurvivesEditByteForByte(t *testing.T) {
	chunks := bytes.Join([][]byte{aiffComm(), aiffSsnd()}, nil)
	// inbounds is a 128-byte "TAG" region whose declared-length bytes encode a small odd
	// value (1): it does not overrun the container, so the overrun proxy misses it.
	inbounds := make([]byte, 128)
	copy(inbounds, "TAG")
	inbounds[7] = 0x01 // declaredLen = 1 (big-endian), small and odd
	inbounds[9] = 0xAB // the byte the phantom parse treats as the injected pad
	trailers := []struct {
		name string
		data []byte
	}{
		{"overrun-title", append([]byte("TAG"), bytes.Repeat([]byte{0xFF}, 125)...)},
		{"inbounds-odd", inbounds},
	}

	for _, tr := range trailers {
		for _, countedInside := range []bool{true, false} {
			boundary := "outsideFormSize"
			if countedInside {
				boundary = "insideFormSize"
			}
			trailer := tr.data
			t.Run(tr.name+"/"+boundary, func(t *testing.T) {
				var innerExtra, outer []byte
				if countedInside {
					innerExtra = trailer
				} else {
					outer = trailer
				}
				src := formWrap(chunks, innerExtra, outer)

				ctx := context.Background()
				base, err := parse(ctx, core.BytesSource(src), core.DefaultParseOptions())
				if err != nil {
					t.Fatalf("parse: %v", err)
				}
				edited := base.Clone()
				edited.Tags.Set(tag.Artist, "NewArtist")
				plan, err := Codec{}.Plan(ctx, base, edited, core.DefaultWriteOptions())
				if err != nil {
					t.Fatalf("Plan: %v", err)
				}
				var buf bytes.Buffer
				if _, err := bits.Write(ctx, &buf, core.BytesSource(src), plan.Segments, nil); err != nil {
					t.Fatalf("bits.Write: %v", err)
				}
				out := buf.Bytes()

				if !bytes.HasSuffix(out, trailer) {
					t.Error("trailer not preserved byte-for-byte at the end of the output")
				}
				if n := bytes.Count(out, trailer); n != 1 {
					t.Errorf("trailer appears %d times, want exactly 1", n)
				}
				re, err := parse(ctx, core.BytesSource(out), core.DefaultParseOptions())
				if err != nil {
					t.Fatalf("reparse: %v", err)
				}
				if v, _ := re.Tags.First(tag.Artist); v != "NewArtist" {
					t.Errorf("reparsed ARTIST = %q, want NewArtist (edit landed)", v)
				}
			})
		}
	}
}
