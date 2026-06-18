package aac

import (
	"bytes"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// buildADTS assembles a 7-byte ADTS fixed header (no CRC) from decoded fields.
// profile is the 2-bit ADTS profile (object type - 1).
func buildADTS(profile, sfIndex, chanConfig, frameLen int) []byte {
	b := make([]byte, 7)
	b[0] = 0xFF
	b[1] = 0xF1 // sync low | MPEG-4 | layer 00 | no CRC
	b[2] = byte(profile<<6) | byte(sfIndex<<2) | byte((chanConfig>>2)&1)
	b[3] = byte((chanConfig&3)<<6) | byte((frameLen>>11)&3)
	b[4] = byte((frameLen >> 3) & 0xFF)
	b[5] = byte((frameLen & 7) << 5)
	return b
}

func TestDecodeADTSValid(t *testing.T) {
	h, ok := decodeADTS(buildADTS(1, 4, 2, 384)) // LC, 44100, stereo
	if !ok {
		t.Fatal("valid ADTS header rejected")
	}
	if h.objectType != 2 || h.sfIndex != 4 || h.sampleRate != 44100 || h.chanConfig != 2 || h.channels != 2 || h.frameLength != 384 {
		t.Errorf("decoded = %+v, want {objectType:2 sfIndex:4 sampleRate:44100 chanConfig:2 channels:2 frameLength:384}", h)
	}
}

func TestDecodeADTSRejects(t *testing.T) {
	valid := buildADTS(1, 4, 2, 384)
	cases := []struct {
		name string
		b    []byte
	}{
		{"short", valid[:6]},
		{"bad sync byte0", append([]byte{0xFE}, valid[1:]...)},
		{"bad sync nibble", func() []byte { b := append([]byte(nil), valid...); b[1] = 0xE1; return b }()},
		{"layer nonzero", func() []byte { b := append([]byte(nil), valid...); b[1] |= 0x02; return b }()},
		{"reserved profile", buildADTS(3, 4, 2, 384)},
		{"reserved sfIndex", buildADTS(1, 13, 2, 384)},
		{"frame length below header", buildADTS(1, 4, 2, 3)},
	}
	for _, c := range cases {
		if _, ok := decodeADTS(c.b); ok {
			t.Errorf("%s: accepted an invalid ADTS header", c.name)
		}
	}
}

// TestDecodeADTSConfigIndependentOfFrameLength is the crux of the essence-digest
// design: two frames with identical static configuration but different
// frame_length (bytes 3-5) decode to the same object type, rate index, and
// channel config - so hashing the decoded config, not the raw header, is exact.
func TestDecodeADTSConfigIndependentOfFrameLength(t *testing.T) {
	a, ok1 := decodeADTS(buildADTS(1, 4, 2, 384))
	b, ok2 := decodeADTS(buildADTS(1, 4, 2, 700))
	if !ok1 || !ok2 {
		t.Fatal("valid headers rejected")
	}
	if a.frameLength == b.frameLength {
		t.Fatal("test setup: frame lengths should differ")
	}
	if a.objectType != b.objectType || a.sfIndex != b.sfIndex || a.chanConfig != b.chanConfig {
		t.Errorf("static config differs with frame length: %+v vs %+v", a, b)
	}
}

func TestEssenceExtentConfig(t *testing.T) {
	d := &doc{header: adtsHeader{objectType: 2, sfIndex: 4, chanConfig: 2}}
	ver, cfg := Codec{}.EssenceExtent(&core.Media{Native: d})
	if ver != "aac-adts-v1" {
		t.Errorf("extent version = %q, want aac-adts-v1", ver)
	}
	// The config carries the static fields only (object type, rate index, channel
	// config) - never the per-frame frame_length.
	if !bytes.Equal(cfg, []byte{2, 4, 2}) {
		t.Errorf("config = %v, want [2 4 2]", cfg)
	}
}
