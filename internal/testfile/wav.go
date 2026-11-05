package testfile

import "slices"

func WAV(chunks ...[]byte) []byte {
	body := slices.Concat([]byte("WAVE"), slices.Concat(chunks...))
	return slices.Concat([]byte("RIFF"), LE32(len(body)), body)
}

func WAVChunk(id string, body []byte) []byte {
	out := slices.Concat([]byte(id), LE32(len(body)), body)
	if len(body)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

// WAVFmtPCM is a 16-byte PCM "fmt " chunk: 44100 Hz, stereo, 16-bit.
func WAVFmtPCM() []byte {
	return WAVChunk("fmt ", slices.Concat(LE16(1), LE16(2), LE32(44100), LE32(176400), LE16(4), LE16(16)))
}

func WAVData(n int) []byte { return WAVChunk("data", make([]byte, n)) }

// WAVInfo is a LIST/INFO chunk of 4CC/value pairs.
func WAVInfo(pairs ...[2]string) []byte {
	body := []byte("INFO")
	for _, p := range pairs {
		body = append(body, WAVChunk(p[0], append([]byte(p[1]), 0))...)
	}
	return WAVChunk("LIST", body)
}
