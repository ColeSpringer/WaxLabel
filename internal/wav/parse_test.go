package wav

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// wavFmtChunk renders a minimal 16-byte PCM "fmt " chunk.
func wavFmtChunk() []byte {
	body := make([]byte, 16)
	binary.LittleEndian.PutUint16(body[0:2], 1)     // PCM
	binary.LittleEndian.PutUint16(body[2:4], 2)     // channels
	binary.LittleEndian.PutUint32(body[4:8], 44100) // sample rate
	binary.LittleEndian.PutUint16(body[14:16], 16)  // bits per sample
	return testfile.WAVChunk("fmt ", body)
}

// riffWrap wraps inner chunks (without the "WAVE" tag) in a RIFF/WAVE header whose
// declared size covers innerCounted bytes, then appends any outer bytes after it.
func riffWrap(chunks, innerExtra, outer []byte) []byte {
	return slices.Concat(testfile.WAV(chunks, innerExtra), outer)
}

func parseWAVDoc(t *testing.T, src []byte) *doc {
	t.Helper()
	m, err := parse(context.Background(), core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m.Native.(*doc)
}

func findWAVChunk(d *doc, id string) *chunk {
	for i := range d.chunks {
		if string(d.chunks[i].id[:]) == id {
			return &d.chunks[i]
		}
	}
	return nil
}

// TestWAVChunkPaddingAndOuterBoundary checks two structural rules: an odd-length chunk
// excludes its word-alignment pad byte from the declared length yet the next chunk
// begins after the pad, and bytes after riffSize are captured as the outer region.
func TestWAVChunkPaddingAndOuterBoundary(t *testing.T) {
	odd := []byte{1, 2, 3, 4, 5} // 5-byte body -> one pad byte
	chunks := bytes.Join([][]byte{
		wavFmtChunk(),
		testfile.WAVChunk("junk", odd),
		testfile.WAVChunk("data", bytes.Repeat([]byte{0x11, 0x22}, 8)),
	}, nil)
	outer := bytes.Repeat([]byte{0xAB}, 10) // appended outside the RIFF size
	src := riffWrap(chunks, nil, outer)

	d := parseWAVDoc(t, src)
	junk := findWAVChunk(d, "junk")
	data := findWAVChunk(d, "data")
	if junk == nil || data == nil {
		t.Fatalf("expected fmt/junk/data chunks, got %d chunks", len(d.chunks))
	}
	if junk.bodyLen != 5 {
		t.Errorf("junk bodyLen = %d, want 5 (pad excluded from the declared length)", junk.bodyLen)
	}
	// data's header begins after junk's body + its single pad byte: junk.bodyOff + 5 + 1
	// (pad) + 8 (data header) == data.bodyOff.
	if want := junk.bodyOff + 5 + 1 + 8; data.bodyOff != want {
		t.Errorf("data bodyOff = %d, want %d (next chunk starts past the pad byte)", data.bodyOff, want)
	}
	if d.outerLen != int64(len(outer)) {
		t.Errorf("outerLen = %d, want %d (bytes past riffSize captured as outer)", d.outerLen, len(outer))
	}
	if d.trailingLen != 0 {
		t.Errorf("trailingLen = %d, want 0 (no leftover inside the RIFF size)", d.trailingLen)
	}
}

// TestWAVTrailerSurvivesEditByteForByte checks that a contiguous ID3v1 "TAG" trailer
// survives an unrelated edit byte-for-byte, whether a writer counted it inside the RIFF
// size or appended it after the RIFF.
func TestWAVTrailerSurvivesEditByteForByte(t *testing.T) {
	chunks := bytes.Join([][]byte{wavFmtChunk(), testfile.WAVChunk("data", bytes.Repeat([]byte{0x11, 0x22}, 16))}, nil)
	// inbounds is a 128-byte "TAG" region whose declared-length bytes encode a small odd
	// value (1): it does not overrun the container, so the overrun proxy misses it.
	inbounds := make([]byte, 128)
	copy(inbounds, "TAG")
	inbounds[4] = 0x01 // declaredLen = 1 (little-endian), small and odd
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
			boundary := "outsideRiffSize"
			if countedInside {
				boundary = "insideRiffSize"
			}
			trailer := tr.data
			t.Run(tr.name+"/"+boundary, func(t *testing.T) {
				var innerExtra, outer []byte
				if countedInside {
					innerExtra = trailer
				} else {
					outer = trailer
				}
				src := riffWrap(chunks, innerExtra, outer)

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
