package iff

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/colespringer/waxlabel/waxerr"
)

var (
	riff = Dialect{Order: binary.LittleEndian, AudioID: [4]byte{'d', 'a', 't', 'a'}, Noun: "RIFF chunks"}
	form = Dialect{Order: binary.BigEndian, AudioID: [4]byte{'S', 'S', 'N', 'D'}, Noun: "IFF chunks"}
)

// chunkBytes builds one chunk: 4-byte id + 4-byte size (in the dialect's order) + body,
// plus a word-alignment pad byte when the body is odd (the pad is not counted in the size).
func chunkBytes(d Dialect, id string, body []byte) []byte {
	hdr := make([]byte, 8)
	copy(hdr, id)
	d.Order.PutUint32(hdr[4:8], uint32(len(body)))
	out := append(hdr, body...)
	if len(body)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

func walk(t *testing.T, data []byte, end int64, d Dialect) Result {
	t.Helper()
	res, err := WalkChunks(context.Background(), bytes.NewReader(data), int64(len(data)), end, 1<<20, 100, d)
	if err != nil {
		t.Fatalf("WalkChunks: %v", err)
	}
	return res
}

// TestWalkChunksPaddingAudioOuter exercises both dialects: an odd-length chunk excludes
// its pad from the declared length yet the next chunk begins past it, the audio chunk is
// indexed, and bytes beyond the container boundary are captured as the outer region.
func TestWalkChunksPaddingAudioOuter(t *testing.T) {
	for _, d := range []Dialect{riff, form} {
		t.Run(d.Noun, func(t *testing.T) {
			audio := string(d.AudioID[:])
			chunks := bytes.Join([][]byte{
				chunkBytes(d, "xxxx", []byte{1, 2, 3}), // odd body -> one pad byte
				chunkBytes(d, audio, []byte{0x11, 0x22, 0x33, 0x44}),
			}, nil)
			body := append(make([]byte, 12), chunks...) // 12-byte container header + chunks
			outer := []byte{0xAB, 0xCD}
			data := append(body, outer...)

			res := walk(t, data, int64(len(body)), d)
			if len(res.Chunks) != 2 {
				t.Fatalf("got %d chunks, want 2", len(res.Chunks))
			}
			if res.Chunks[0].BodyLen != 3 {
				t.Errorf("odd chunk BodyLen = %d, want 3 (pad excluded)", res.Chunks[0].BodyLen)
			}
			// The audio chunk starts past the odd chunk's body + pad: 12 + 8 + 3 + 1 + 8.
			if want := int64(12 + 8 + 3 + 1 + 8); res.Chunks[1].BodyOff != want {
				t.Errorf("audio BodyOff = %d, want %d (next chunk past the pad)", res.Chunks[1].BodyOff, want)
			}
			if res.AudioIdx != 1 || res.AudioTruncated {
				t.Errorf("AudioIdx=%d AudioTruncated=%v, want 1/false", res.AudioIdx, res.AudioTruncated)
			}
			if res.OuterLen != int64(len(outer)) || res.TrailingLen != 0 {
				t.Errorf("Outer=%d Trailing=%d, want %d/0", res.OuterLen, res.TrailingLen, len(outer))
			}
		})
	}
}

// TestWalkChunksID3v1Tail: a 128-byte "TAG" region after the audio, counted inside the
// container with a small (non-overrunning) declared length, is stopped at by the tail
// detector and captured as the trailing region rather than parsed as phantom chunks.
func TestWalkChunksID3v1Tail(t *testing.T) {
	for _, d := range []Dialect{riff, form} {
		t.Run(d.Noun, func(t *testing.T) {
			chunks := chunkBytes(d, string(d.AudioID[:]), []byte{0x11, 0x22})
			body := append(make([]byte, 12), chunks...)
			tag := make([]byte, 128)
			copy(tag, "TAG") // small declared-length bytes (all zero): does not overrun
			data := append(body, tag...)
			end := int64(len(data)) // the TAG is counted inside the container

			res := walk(t, data, end, d)
			if len(res.Chunks) != 1 || res.AudioIdx != 0 {
				t.Fatalf("got %d chunks AudioIdx=%d, want 1/0 (TAG must not parse as a chunk)", len(res.Chunks), res.AudioIdx)
			}
			if res.TrailingOff != int64(len(body)) || res.TrailingLen != 128 {
				t.Errorf("Trailing = (%d, %d), want (%d, 128) - the TAG preserved verbatim", res.TrailingOff, res.TrailingLen, len(body))
			}
		})
	}
}

// TestWalkChunksID3v1TailNoAudio: the ID3v1 tail is recognized by shape even in a
// malformed container with no audio chunk - the break is gated on AudioIdx only for the
// overrun shape, not the ID3v1 shape, so the marker is still preserved verbatim.
func TestWalkChunksID3v1TailNoAudio(t *testing.T) {
	body := append(make([]byte, 12), chunkBytes(riff, "junk", []byte{1, 2})...) // no audio chunk
	tail := make([]byte, 128)
	copy(tail, "TAG")
	data := append(body, tail...)

	res := walk(t, data, int64(len(data)), riff)
	if len(res.Chunks) != 1 || res.AudioIdx != -1 {
		t.Fatalf("got %d chunks AudioIdx=%d, want 1/-1 (TAG must not parse as a chunk)", len(res.Chunks), res.AudioIdx)
	}
	if res.TrailingOff != int64(len(body)) || res.TrailingLen != 128 {
		t.Errorf("Trailing = (%d, %d), want (%d, 128)", res.TrailingOff, res.TrailingLen, len(body))
	}
}

// TestWalkChunksNoChunks: an empty container yields ErrInvalidData with the dialect noun.
func TestWalkChunksNoChunks(t *testing.T) {
	data := make([]byte, 12) // header only, no chunks
	_, err := WalkChunks(context.Background(), bytes.NewReader(data), 12, 12, 1<<20, 100, riff)
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, want ErrInvalidData", err)
	}
}

// TestWalkChunksOversizedNonAudioChunk checks that clamped non-audio chunks are recorded
// for caller warnings, while the 0xFFFFFFFF "size unknown" sentinel is left alone.
func TestWalkChunksOversizedNonAudioChunk(t *testing.T) {
	for _, d := range []Dialect{riff, form} {
		t.Run(d.Noun, func(t *testing.T) {
			overChunk := func(declared uint32) []byte {
				h := make([]byte, 8)
				copy(h, "JUNK")
				d.Order.PutUint32(h[4:8], declared)
				return append(h, 1, 2, 3, 4) // only 4 body bytes present
			}

			// Declares far more than the file holds: clamped and recorded.
			data := append(make([]byte, 12), overChunk(9999)...)
			res := walk(t, data, int64(len(data)), d)
			if len(res.OversizedChunks) != 1 || string(res.OversizedChunks[0][:]) != "JUNK" {
				t.Errorf("OversizedChunks = %v, want one JUNK", res.OversizedChunks)
			}
			if res.AudioTruncated {
				t.Error("a non-audio overrun set AudioTruncated")
			}

			// The 0xFFFFFFFF streaming sentinel is "size unknown", not an overrun.
			data2 := append(make([]byte, 12), overChunk(0xFFFFFFFF)...)
			if res2 := walk(t, data2, int64(len(data2)), d); len(res2.OversizedChunks) != 0 {
				t.Errorf("0xFFFFFFFF sentinel flagged oversized: %v", res2.OversizedChunks)
			}
		})
	}
}
