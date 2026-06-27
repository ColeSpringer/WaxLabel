package mp4

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// mkStsdPayload builds a minimal stsd payload (the bytes after the stsd box header)
// with one audio sample entry at the given sound-sample-description version. It plants
// sentinel bytes at the v0/v1 geometry offsets so a v2-unaware reader would surface
// them as channels / sample-size / sample-rate.
func mkStsdPayload(soundVersion uint16) []byte {
	b := make([]byte, 44)
	binary.BigEndian.PutUint32(b[4:8], 1) // entry_count
	copy(b[12:16], "mp4a")                // format four-cc
	binary.BigEndian.PutUint16(b[24:26], soundVersion)
	binary.BigEndian.PutUint16(b[32:34], 0xFFFF) // channels offset (sentinel)
	binary.BigEndian.PutUint16(b[34:36], 0xFFFF) // sample-size offset
	binary.BigEndian.PutUint16(b[40:42], 0xFFFF) // sample-rate integer offset
	return b
}

// parseStsdPayload wraps a payload in an 8-byte stsd box header and runs parseStsd.
func parseStsdPayload(t *testing.T, payload []byte) *doc {
	t.Helper()
	raw := append([]byte{0, 0, 0, 0, 's', 't', 's', 'd'}, payload...)
	binary.BigEndian.PutUint32(raw[0:4], uint32(len(raw)))
	d := &doc{}
	n := node{name: [4]byte{'s', 't', 's', 'd'}, offset: 0, headerLen: 8, size: int64(len(raw))}
	parseStsd(core.BytesSource(raw), n, d, 1<<20)
	return d
}

// TestParseStsdV2SkipsGeometry checks that a v2+ AudioSampleEntry does not use the fixed
// v0/v1 geometry offsets. Reading those offsets would feed bogus channels and sample rate
// into the essence-digest salt. The codec 4CC is still read, and a v0 entry still reads
// the fixed-offset geometry.
func TestParseStsdV2SkipsGeometry(t *testing.T) {
	d := parseStsdPayload(t, mkStsdPayload(2))
	if got := string(d.cfg.codec[:]); got != "mp4a" {
		t.Errorf("codec = %q, want mp4a", got)
	}
	if d.cfg.channels != 0 || d.cfg.sampleSize != 0 || d.cfg.sampleRate != 0 {
		t.Errorf("v2 entry surfaced bogus geometry: channels=%d sampleSize=%d sampleRate=%d (want all 0)",
			d.cfg.channels, d.cfg.sampleSize, d.cfg.sampleRate)
	}
	// The salt is deterministic: codec four-cc followed by zero geometry.
	_, salt := Codec{}.EssenceExtent(&core.Media{Native: d})
	if want := append([]byte("mp4a"), make([]byte, 8)...); !bytes.Equal(salt, want) {
		t.Errorf("essence salt = % x, want % x (deterministic, no bogus geometry)", salt, want)
	}

	// A v0 entry still reads the geometry from the fixed offsets.
	d0 := parseStsdPayload(t, mkStsdPayload(0))
	if d0.cfg.channels != 0xFFFF || d0.cfg.sampleSize != 0xFFFF || d0.cfg.sampleRate != 0xFFFF {
		t.Errorf("v0 entry should read fixed-offset geometry: channels=%d sampleSize=%d sampleRate=%d",
			d0.cfg.channels, d0.cfg.sampleSize, d0.cfg.sampleRate)
	}
}

// TestCapabilitiesPictureMIMEsCloned guards against the public Capabilities aliasing the
// package coverMIMEs backing array: a caller mutating the returned slice must not corrupt
// the write-time cover guard, which reads the package var via coverMIMESupported.
func TestCapabilitiesPictureMIMEsCloned(t *testing.T) {
	mimes := Codec{}.Capabilities(nil, core.WriteOptions{}).Pictures.PictureMIMEs
	if len(mimes) == 0 {
		t.Fatal("expected MP4 PictureMIMEs to be populated")
	}
	mimes[0] = "image/evil" // a caller mutating the slice it was handed
	if !coverMIMESupported("image/jpeg") {
		t.Error("mutating the returned PictureMIMEs corrupted coverMIMESupported (slice aliased the package var)")
	}
	if coverMIMESupported("image/evil") {
		t.Error("coverMIMESupported accepted a value injected through the returned slice")
	}
}
