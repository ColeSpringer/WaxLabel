package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mp4Atom renders one MP4 box: a 32-bit big-endian total size, the 4-byte name, then body.
func mp4Atom(name string, body []byte) []byte {
	sz := 8 + len(body)
	b := []byte{byte(sz >> 24), byte(sz >> 16), byte(sz >> 8), byte(sz)}
	b = append(b, name...)
	return append(b, body...)
}

// TestMP4MetaZeroTailExit4 covers the CLI path: an MP4 whose moov.udta.meta has no ilst and
// ends in >=8 zero bytes must fail with exit 4 (invalid-data) on dump and set - never parse as
// a silent, tag-losing "Saved".
func TestMP4MetaZeroTailExit4(t *testing.T) {
	free := mp4Atom("free", nil)                     // an 8-byte leaf child that tiles cleanly
	metaBody := append([]byte{0, 0, 0, 0}, free...)  // FullBox version/flags + the child
	metaBody = append(metaBody, make([]byte, 16)...) // then >=8 trailing zeros, no ilst
	moov := mp4Atom("moov", mp4Atom("udta", mp4Atom("meta", metaBody)))
	ftyp := mp4Atom("ftyp", []byte("M4A \x00\x00\x00\x00M4A mp42"))
	mdat := mp4Atom("mdat", []byte("audiodata"))
	raw := slices.Concat(ftyp, moov, mdat)

	path := filepath.Join(t.TempDir(), "zerotail.m4a")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCLI(t, "dump", path); code != 4 {
		t.Errorf("dump exit = %d, want 4 (invalid-data)", code)
	}
	stdout, _, code := runCLI(t, "set", path, "--set", "TITLE=Ghost")
	if code != 4 {
		t.Errorf("set exit = %d, want 4 (invalid-data)", code)
	}
	if strings.Contains(stdout, "Saved") {
		t.Errorf("set must not report Saved on an unparseable file:\n%s", stdout)
	}
}

// TestID3CorruptVersionExit4: an ID3v2 header with an out-of-range major version (5)
// on valid MPEG audio is a recognized container whose contents are corrupt - invalid-data
// (exit 4), not an unsupported format (exit 3).
func TestID3CorruptVersionExit4(t *testing.T) {
	frames, err := os.ReadFile(filepath.Join("..", "..", "testdata", "notags.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	// A structurally valid 10-byte ID3v2 header (sync-safe size 0) but major version 5.
	corrupt := append([]byte("ID3\x05\x00\x00\x00\x00\x00\x00"), frames...)
	path := filepath.Join(t.TempDir(), "badver.mp3")
	if err := os.WriteFile(path, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, "dump", path); code != 4 {
		t.Errorf("dump exit = %d, want 4 (invalid-data); stderr=%q", code, stderr)
	}
}

// TestMP3MalformedAPICSurfaced covers the CLI path: an MP3 whose ID3v2 APIC frame is
// malformed (its MIME field has no NUL terminator, so decodeAPIC fails) must surface
// invalid-picture on dump and lint - not silently drop the cover and report "no issues".
func TestMP3MalformedAPICSurfaced(t *testing.T) {
	frames, err := os.ReadFile(filepath.Join("..", "..", "testdata", "notags.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	ss := func(n int) []byte { // 28-bit sync-safe size
		return []byte{byte(n>>21) & 0x7f, byte(n>>14) & 0x7f, byte(n>>7) & 0x7f, byte(n) & 0x7f}
	}
	apicBody := []byte("\x00image/png") // text-encoding byte + a MIME with no NUL terminator
	frame := append([]byte("APIC"), ss(len(apicBody))...)
	frame = append(frame, 0x00, 0x00) // frame flags
	frame = append(frame, apicBody...)
	tagBytes := append([]byte("ID3\x04\x00\x00"), ss(len(frame))...)
	tagBytes = append(tagBytes, frame...)
	path := filepath.Join(t.TempDir(), "badapic.mp3")
	if err := os.WriteFile(path, append(tagBytes, frames...), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := runCLI(t, "dump", path); !strings.Contains(out, "invalid-picture") {
		t.Errorf("dump should surface invalid-picture for a malformed APIC:\n%s", out)
	}
	out, _, code := runCLI(t, "lint", path)
	if !strings.Contains(out, "invalid-picture") {
		t.Errorf("lint should report invalid-picture:\n%s", out)
	}
	if code == 0 {
		t.Errorf("lint exit = 0, want non-zero once a malformed cover is a lint warning")
	}
}

// TestEmptyWAVJSONNoBitrate: a zero-duration file (header-only PCM WAV) must not
// emit bitrateBps in JSON, matching the human view's Duration()>0 gate so the two agree.
func TestEmptyWAVJSONNoBitrate(t *testing.T) {
	emptyWAV := filepath.Join("..", "..", "testdata", "empty.wav")
	stdout, _, code := runCLI(t, "dump", "--json", emptyWAV)
	if code != 0 {
		t.Fatalf("dump --json exit = %d, want 0", code)
	}
	if strings.Contains(stdout, "bitrateBps") {
		t.Errorf("zero-duration WAV must not emit bitrateBps in JSON:\n%s", stdout)
	}
	// Guard the fixture assumption: empty.wav must actually be zero-duration (durationMs is
	// omitempty, so absent), or the parity gate would not apply.
	if strings.Contains(stdout, "durationMs") {
		t.Errorf("empty.wav is unexpectedly non-zero-duration; fixture assumption broken:\n%s", stdout)
	}
}

// TestSubMillisecondWAVJSONConsistent covers the sub-millisecond edge of the bitrate gate: a
// WAV with a few PCM samples (< 1ms) has Duration()>0 but Milliseconds()==0, so durationMs
// rounds to 0 and drops (omitempty). bitrateBps must drop with it - gating on Milliseconds()
// keeps the JSON from showing a bitrate with no duration.
func TestSubMillisecondWAVJSONConsistent(t *testing.T) {
	fmtBody := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtBody[0:], 1)      // PCM
	binary.LittleEndian.PutUint16(fmtBody[2:], 2)      // channels
	binary.LittleEndian.PutUint32(fmtBody[4:], 44100)  // sample rate
	binary.LittleEndian.PutUint32(fmtBody[8:], 176400) // byte rate
	binary.LittleEndian.PutUint16(fmtBody[12:], 4)     // block align
	binary.LittleEndian.PutUint16(fmtBody[14:], 16)    // bits per sample
	chunk := func(id string, body []byte) []byte {
		h := make([]byte, 4)
		binary.LittleEndian.PutUint32(h, uint32(len(body)))
		return slices.Concat([]byte(id), h, body)
	}
	inner := slices.Concat([]byte("WAVE"), chunk("fmt ", fmtBody), chunk("data", make([]byte, 40))) // 10 frames ~ 0.23 ms
	sz := make([]byte, 4)
	binary.LittleEndian.PutUint32(sz, uint32(len(inner)))
	wav := slices.Concat([]byte("RIFF"), sz, inner)

	path := filepath.Join(t.TempDir(), "subms.wav")
	if err := os.WriteFile(path, wav, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := runCLI(t, "dump", "--json", path)
	if code != 0 {
		t.Fatalf("dump --json exit = %d, want 0", code)
	}
	if strings.Contains(stdout, "durationMs") {
		t.Fatalf("fixture assumption broken: sub-ms WAV should round to no durationMs:\n%s", stdout)
	}
	if strings.Contains(stdout, "bitrateBps") {
		t.Errorf("sub-ms WAV emits bitrateBps while durationMs is absent (inconsistent):\n%s", stdout)
	}
}
