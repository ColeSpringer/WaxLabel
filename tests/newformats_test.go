package waxlabel_test

import (
	"encoding/binary"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestNewFormatNativeDescribe checks each new codec's native view names the regions it
// parsed. Describe feeds `dump --native`, so an empty or panicking one is a user-facing
// hole that no other test would catch.
func TestNewFormatNativeDescribe(t *testing.T) {
	for _, c := range []struct {
		path string
		want []string
	}{
		{sampleWV, []string{"WavPack blocks", "APEv2"}},
		{sampleAPE, []string{"Monkey's Audio frames", "APEv2"}},
		{sampleWMA, []string{"File Properties", "Stream Properties", "data"}},
		{sampleOggFLAC, []string{"FLAC identification header", "VORBIS_COMMENT", "audio pages"}},
		{sampleRF64, []string{"ds64", "fmt", "data"}},
	} {
		t.Run(c.path, func(t *testing.T) {
			var kinds []string
			for _, e := range mustParseFile(t, c.path).Native().Describe() {
				kinds = append(kinds, strings.TrimSpace(e.Kind))
			}
			for _, want := range c.want {
				if !slices.Contains(kinds, want) {
					t.Errorf("native view = %v, want it to name %q", kinds, want)
				}
			}
		})
	}
}

// TestMusepackNativeDescribe covers the synthesized formats, which have no fixture.
func TestMusepackNativeDescribe(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"SV7", mpcSV7(100, 0), "Musepack SV7 stream"},
		{"SV8", mpcSV8(44100, 0, 0, 2), "Musepack SV8 packets"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var kinds []string
			for _, e := range mustParseBytes(t, c.data).Native().Describe() {
				kinds = append(kinds, strings.TrimSpace(e.Kind))
			}
			if !slices.Contains(kinds, c.want) {
				t.Errorf("native view = %v, want it to name %q", kinds, c.want)
			}
		})
	}
}

// wvBlock builds a WavPack block header with the given flags, so the flag-derived
// geometry can be exercised without an encoder that emits it.
func wvBlock(flags uint32, totalSamples uint32, body []byte) []byte {
	b := make([]byte, 32)
	copy(b[0:4], "wvpk")
	binary.LittleEndian.PutUint32(b[4:8], uint32(24+len(body)))
	binary.LittleEndian.PutUint16(b[8:10], 0x0410)
	binary.LittleEndian.PutUint32(b[12:16], totalSamples)
	binary.LittleEndian.PutUint32(b[20:24], totalSamples)
	binary.LittleEndian.PutUint32(b[24:28], flags)
	return append(b, body...)
}

// wvSubBlock frames one WavPack metadata sub-block: the id byte, a word count, and the
// payload, with the odd-size bit set when the payload is not word-aligned.
func wvSubBlock(id byte, data []byte) []byte {
	words := (len(data) + 1) / 2
	if len(data)%2 == 1 {
		id |= 0x40 // ID_ODD_SIZE
	}
	out := append([]byte{id, byte(words)}, data...)
	if len(data)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

// TestWavPackNonStandardSampleRate covers the ID_SAMPLE_RATE sub-block walk: with the
// rate index set to "unknown", the rate can only come from the block body, and a break
// there would leave every such file reporting 0 Hz and no duration.
func TestWavPackNonStandardSampleRate(t *testing.T) {
	const rateIndexUnknown, magShift = 15, 18
	flags := uint32(1) | // 2 bytes per sample
		uint32(15)<<magShift | // magnitude, deliberately not the bit depth
		uint32(rateIndexUnknown)<<23
	sub := wvSubBlock(0x27, []byte{0x40, 0x1F, 0x00}) // ID_SAMPLE_RATE: 8000 Hz, 24-bit LE
	data := wvBlock(flags, 8000, sub)

	tr := mustParseBytes(t, data).Properties().First()
	if tr.SampleRate != 8000 {
		t.Errorf("SampleRate = %d, want 8000 from the ID_SAMPLE_RATE sub-block", tr.SampleRate)
	}
	if tr.Duration <= 0 {
		t.Errorf("Duration = %v, want a second of audio", tr.Duration)
	}
	if tr.BitsPerSample != 16 {
		t.Errorf("BitsPerSample = %d, want 16 from the storage width", tr.BitsPerSample)
	}
}

// TestWavPackDSDBlock covers the other sub-block the walk looks for: a DSD stream is one
// bit per sample at a rate only the sub-block carries.
func TestWavPackDSDBlock(t *testing.T) {
	const rateIndexUnknown = 15
	flags := uint32(1) | uint32(rateIndexUnknown)<<23 | 1<<31 // DSD
	body := append(wvSubBlock(0x0E, []byte{0x01}), wvSubBlock(0x27, []byte{0x00, 0x11, 0x2B})...)
	data := wvBlock(flags, 2822400, body)

	tr := mustParseBytes(t, data).Properties().First()
	if tr.SampleRate != 2822400 {
		t.Errorf("SampleRate = %d, want the DSD rate from ID_SAMPLE_RATE", tr.SampleRate)
	}
	if tr.BitsPerSample != 1 {
		t.Errorf("BitsPerSample = %d, want 1 for DSD", tr.BitsPerSample)
	}
}

// TestMP3LegacyAPEDateSurfacesAsRecordingDate pins the read-alias behavior the APEv2
// mapping gained when its table moved to internal/mapping: an APE "Year" item resolves
// to RECORDINGDATE like every other format's date spelling, so MP3's legacy family view
// shows it under the same key the canonical set uses.
func TestMP3LegacyAPEDateSurfacesAsRecordingDate(t *testing.T) {
	ape := apeTagText(map[string]string{"Year": "1999"})
	data := append(append(id3v2(4, textFrame(4, "TDRC", "2020")), mp3Audio(t)...), ape...)

	doc := mustParseBytes(t, data)
	if got := doc.Fields().RecordingDate; got != "2020" {
		t.Fatalf("canonical RecordingDate = %q, want the ID3 value to stay authoritative", got)
	}
	var legacy []string
	for _, f := range doc.Families() {
		if f.Key == tag.RecordingDate && f.Legacy {
			legacy = append(legacy, f.Values...)
		}
	}
	if !slices.Equal(legacy, []string{"1999"}) {
		t.Errorf("legacy RECORDINGDATE entries = %v, want the APE Year item surfaced under it", legacy)
	}
	if !hasWarning(doc, wl.WarnLegacyAPE) {
		t.Error("expected the legacy-APE warning")
	}
}

// apeTagText builds a footer-only APEv2 tag from text items.
func apeTagText(items map[string]string) []byte {
	var body []byte
	for k, v := range items {
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(v)))
		body = append(body, hdr[:]...)
		body = append(body, k...)
		body = append(body, 0)
		body = append(body, v...)
	}
	foot := make([]byte, 32)
	copy(foot[0:8], "APETAGEX")
	binary.LittleEndian.PutUint32(foot[8:12], 2000)
	binary.LittleEndian.PutUint32(foot[12:16], uint32(len(body)+32))
	binary.LittleEndian.PutUint32(foot[16:20], uint32(len(items)))
	return append(body, foot...)
}
