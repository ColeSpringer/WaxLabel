package aiff

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// be32 encodes a big-endian uint32 (IFF is big-endian).
func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// aiffChunk renders one IFF chunk: a 4-byte id, the big-endian body length, the body,
// and a word-alignment pad byte when the body is odd (the pad is NOT counted in the
// declared length).
func aiffChunk(id string, body []byte) []byte {
	out := append([]byte(id), be32(uint32(len(body)))...)
	out = append(out, body...)
	if len(body)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

// aiffComm renders a minimal 18-byte COMM chunk: 2 channels, 16-bit, 44100 Hz (whose
// 80-bit SANE-extended encoding is the well-known 40 0E AC 44 00...).
func aiffComm() []byte {
	body := make([]byte, 18)
	binary.BigEndian.PutUint16(body[0:2], 2)  // channels
	binary.BigEndian.PutUint32(body[2:6], 8)  // sample frames
	binary.BigEndian.PutUint16(body[6:8], 16) // sample size
	copy(body[8:18], []byte{0x40, 0x0E, 0xAC, 0x44, 0, 0, 0, 0, 0, 0})
	return aiffChunk("COMM", body)
}

// aiffSsnd renders an SSND chunk: an 8-byte offset+blockSize header then audio bytes.
func aiffSsnd() []byte {
	body := append(make([]byte, 8), bytes.Repeat([]byte{0x11, 0x22}, 8)...)
	return aiffChunk("SSND", body)
}

// formWrap wraps inner chunks (without the "AIFF" tag) in a FORM/AIFF header whose
// declared size covers innerExtra, then appends any outer bytes after it.
func formWrap(chunks, innerExtra, outer []byte) []byte {
	inner := append([]byte("AIFF"), chunks...)
	inner = append(inner, innerExtra...)
	out := append([]byte("FORM"), be32(uint32(len(inner)))...)
	out = append(out, inner...)
	return append(out, outer...)
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
		aiffChunk("ANNO", odd),
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
// size or appended it after the FORM. If parsed as a phantom chunk, the edit would rewrite
// its header and split the marker.
func TestAIFFTrailerSurvivesEditByteForByte(t *testing.T) {
	chunks := bytes.Join([][]byte{aiffComm(), aiffSsnd()}, nil)
	// inbounds is a 128-byte "TAG" region whose declared-length bytes encode a small odd
	// value (1): it does not overrun the container, so the overrun proxy misses it. The odd
	// phantom body would force a word-align pad on re-emit that zeroes the non-zero byte at
	// [9], unless the 128-byte-TAG-at-tail detector preserves it. An all-NUL trailer would
	// round-trip by coincidence because declaredLen 0 is even.
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
