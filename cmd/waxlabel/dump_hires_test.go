package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// hiResALACFile writes a synthetic ALAC .m4a whose sample entry's 16.16 rate field is 0
// (it cannot hold 96000) and whose magic cookie declares the real 96 kHz / 24-bit stereo
// configuration.
func hiResALACFile(t *testing.T) string {
	t.Helper()
	be32 := func(n int) []byte {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(n))
		return b
	}
	be16 := func(n int) []byte {
		b := make([]byte, 2)
		binary.BigEndian.PutUint16(b, uint16(n))
		return b
	}
	cookieCfg := make([]byte, 24)
	binary.BigEndian.PutUint32(cookieCfg[0:4], 4096) // frameLength
	cookieCfg[5] = 24                                // bitDepth
	cookieCfg[6], cookieCfg[7], cookieCfg[8] = 40, 10, 14
	cookieCfg[9] = 2                                    // numChannels
	binary.BigEndian.PutUint16(cookieCfg[10:12], 255)   // maxRun
	binary.BigEndian.PutUint32(cookieCfg[20:24], 96000) // sampleRate
	cookie := mp4Atom("alac", slices.Concat([]byte{0, 0, 0, 0}, cookieCfg))

	entry := mp4Atom("alac", slices.Concat(
		make([]byte, 6), []byte{0, 1}, make([]byte, 8),
		be16(2), be16(16), []byte{0, 0, 0, 0},
		be32(0), // 16.16 sample rate: 96000 does not fit
		cookie,
	))
	stsd := mp4Atom("stsd", slices.Concat([]byte{0, 0, 0, 0}, be32(1), entry))
	mdhd := mp4Atom("mdhd", slices.Concat([]byte{0, 0, 0, 0}, make([]byte, 8), be32(96000), be32(96000)))
	hdlr := mp4Atom("hdlr", slices.Concat(make([]byte, 8), []byte("soun"), make([]byte, 12)))

	build := func(stcoOff int) []byte {
		stco := mp4Atom("stco", slices.Concat([]byte{0, 0, 0, 0}, be32(1), be32(stcoOff)))
		stbl := mp4Atom("stbl", slices.Concat(stsd, stco))
		mdia := mp4Atom("mdia", slices.Concat(hdlr, mdhd, mp4Atom("minf", stbl)))
		moov := mp4Atom("moov", mp4Atom("trak", mdia))
		ftyp := mp4Atom("ftyp", []byte("M4A \x00\x00\x00\x00M4A mp42"))
		return slices.Concat(ftyp, moov, mp4Atom("mdat", bytes.Repeat([]byte{0xA7}, 120)))
	}
	raw := build(0)
	raw = build(bytes.Index(raw, []byte("mdat")) + 4)

	path := filepath.Join(t.TempDir(), "hires.m4a")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDumpJSONHiResALACSampleRate: the rate the CLI reports for a hi-res ALAC comes from
// the magic cookie, the only place a 96 kHz rate fits.
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
