package wav

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// le32 encodes a little-endian uint32 (RIFF is little-endian).
func le32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// wavChunk renders one RIFF chunk: a 4-byte id, the little-endian body length, the
// body, and a word-alignment pad byte when the body is odd (the pad is NOT counted in
// the declared length).
func wavChunk(id string, body []byte) []byte {
	out := append([]byte(id), le32(uint32(len(body)))...)
	out = append(out, body...)
	if len(body)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

// wavFmtChunk renders a minimal 16-byte PCM "fmt " chunk.
func wavFmtChunk() []byte {
	body := make([]byte, 16)
	binary.LittleEndian.PutUint16(body[0:2], 1)     // PCM
	binary.LittleEndian.PutUint16(body[2:4], 2)     // channels
	binary.LittleEndian.PutUint32(body[4:8], 44100) // sample rate
	binary.LittleEndian.PutUint16(body[14:16], 16)  // bits per sample
	return wavChunk("fmt ", body)
}

// riffWrap wraps inner chunks (without the "WAVE" tag) in a RIFF/WAVE header whose
// declared size covers innerCounted bytes, then appends any outer bytes after it.
func riffWrap(chunks, innerExtra, outer []byte) []byte {
	inner := append([]byte("WAVE"), chunks...)
	inner = append(inner, innerExtra...)
	out := append([]byte("RIFF"), le32(uint32(len(inner)))...)
	out = append(out, inner...)
	return append(out, outer...)
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

// TestWAVChunkPaddingAndOuterBoundary checks the two parse-time structural rules: an
// odd-length chunk excludes its word-alignment pad byte from the declared length yet the
// next chunk begins after the pad, and bytes after riffSize are captured as the outer
// region (not parsed as chunks).
func TestWAVChunkPaddingAndOuterBoundary(t *testing.T) {
	odd := []byte{1, 2, 3, 4, 5} // 5-byte body -> one pad byte
	chunks := bytes.Join([][]byte{
		wavFmtChunk(),
		wavChunk("junk", odd),
		wavChunk("data", bytes.Repeat([]byte{0x11, 0x22}, 8)),
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
// size or appended it after the RIFF. If parsed as a phantom chunk, the edit would rewrite
// its header and split the marker.
func TestWAVTrailerSurvivesEditByteForByte(t *testing.T) {
	chunks := bytes.Join([][]byte{wavFmtChunk(), wavChunk("data", bytes.Repeat([]byte{0x11, 0x22}, 16))}, nil)
	// inbounds is a 128-byte "TAG" region whose declared-length bytes encode a small odd
	// value (1): it does not overrun the container, so the overrun proxy misses it. The odd
	// phantom body would force a word-align pad on re-emit that zeroes the non-zero byte at
	// [9], unless the 128-byte-TAG-at-tail detector preserves it. An all-NUL trailer would
	// round-trip by coincidence because declaredLen 0 is even, so it does not exercise the
	// detector.
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
