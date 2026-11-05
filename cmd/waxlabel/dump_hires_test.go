package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// hiResFile: minimal one-track .m4a at 96 kHz media timescale.
func hiResFile(t *testing.T, entry []byte) string {
	t.Helper()
	build := func(stcoOff int) []byte {
		return slices.Concat(testfile.MP4Atom("ftyp", []byte("M4A \x00\x00\x00\x00M4A mp42")),
			testfile.MP4MoovStsd(nil, uint32(stcoOff), nil, nil, testfile.MP4Stsd(entry), 96000),
			testfile.MP4Atom("mdat", bytes.Repeat([]byte{0xA7}, 120)))
	}
	raw := build(bytes.Index(build(0), []byte("mdat")) + 4)

	path := filepath.Join(t.TempDir(), "hires.m4a")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// hiResALACFile: stsd 16.16 rate is 0; the ALAC magic cookie holds 96 kHz/24-bit stereo.
func hiResALACFile(t *testing.T) string {
	t.Helper()
	return hiResFile(t, testfile.MP4StsdEntry("alac", 2, 16, 0, testfile.MP4AlacCookie(96000, 2, 24)))
}

// hiResAACFile: stsd 16.16 rate is 0; the esds ASC holds 96 kHz stereo.
func hiResAACFile(t *testing.T) string {
	t.Helper()
	asc := []byte{0x10, 0x10} // AAC LC, samplingFrequencyIndex 0 (96000), stereo
	return hiResFile(t, testfile.MP4StsdEntry("mp4a", 2, 16, 0, testfile.MP4EsdsShort(asc)))
}

// TestDumpJSONHiResALACSampleRate: hi-res ALAC sample rate comes from magic cookie.
func TestDumpJSONHiResALACSampleRate(t *testing.T) {
	path := hiResALACFile(t)
	jd := dumpJSON(t, path)
	if jd.Properties == nil {
		t.Fatal("dump --json reported no properties")
	}
	if jd.Properties.SampleRate != 96000 {
		t.Errorf("sampleRate = %d, want 96000", jd.Properties.SampleRate)
	}
	if jd.Properties.Codec != "ALAC" {
		t.Errorf("codec = %q, want ALAC", jd.Properties.Codec)
	}
	if jd.Properties.BitsPerSample != 24 {
		t.Errorf("bitsPerSample = %d, want 24", jd.Properties.BitsPerSample)
	}
	stdout, _, code := runCLI(t, "dump", path)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if !strings.Contains(stdout, "96000 Hz") {
		t.Errorf("text dump should show 96000 Hz:\n%s", stdout)
	}
}

// TestDumpJSONHiResAACSampleRate: hi-res AAC rate from esds ASC; profile from object type.
func TestDumpJSONHiResAACSampleRate(t *testing.T) {
	t.Parallel()
	path := hiResAACFile(t)
	jd := dumpJSON(t, path)
	if jd.Properties == nil {
		t.Fatal("dump --json reported no properties")
	}
	if jd.Properties.SampleRate != 96000 {
		t.Errorf("sampleRate = %d, want 96000", jd.Properties.SampleRate)
	}
	if jd.Properties.Codec != "AAC" || jd.Properties.CodecProfile != "AAC LC" {
		t.Errorf("codec = %q/%q, want AAC with profile AAC LC", jd.Properties.Codec, jd.Properties.CodecProfile)
	}
	stdout, _, code := runCLI(t, "dump", path)
	if code != 0 {
		t.Fatalf("dump exit = %d", code)
	}
	if !strings.Contains(stdout, "96000 Hz") {
		t.Errorf("text dump should show 96000 Hz:\n%s", stdout)
	}
}
