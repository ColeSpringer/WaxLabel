package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// TestMP4MetaZeroTailExit4: meta with zero tail is exit 4 on dump/set, not silent success.
func TestMP4MetaZeroTailExit4(t *testing.T) {
	free := testfile.MP4Atom("free", nil)            // an 8-byte leaf child that tiles cleanly
	metaBody := append([]byte{0, 0, 0, 0}, free...)  // FullBox version/flags + the child
	metaBody = append(metaBody, make([]byte, 16)...) // then >=8 trailing zeros, no ilst
	moov := testfile.MP4Atom("moov", testfile.MP4Atom("udta", testfile.MP4Atom("meta", metaBody)))
	ftyp := testfile.MP4Atom("ftyp", []byte("M4A \x00\x00\x00\x00M4A mp42"))
	mdat := testfile.MP4Atom("mdat", []byte("audiodata"))
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

// TestID3UnknownVersionIgnored: a front tag of ID3v2 major version 5 is ignored and reported, not a
// refused file; a write that replaces it says so, and --strict refuses that.
func TestID3UnknownVersionIgnored(t *testing.T) {
	frames, err := os.ReadFile(filepath.Join("..", "..", "testdata", "notags.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	// Valid 10-byte ID3v2 header, major version 5.
	corrupt := append([]byte("ID3\x05\x00\x00\x00\x00\x00\x00"), frames...)
	path := filepath.Join(t.TempDir(), "badver.mp3")
	if err := os.WriteFile(path, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, stderr, code := runCLI(t, "dump", path); code != 0 || !strings.Contains(out, "major version 5") {
		t.Errorf("dump exit = %d, want 0 reporting the ignored tag; stdout=%q stderr=%q", code, out, stderr)
	}
	if _, _, code := runCLI(t, "set", path, "--strict", "--set", "TITLE=New"); code != 2 {
		t.Errorf("strict set exit = %d, want 2 (the rewrite drops the ignored tag)", code)
	}
	if out, stderr, code := runCLI(t, "set", path, "--set", "TITLE=New"); code != 0 || !strings.Contains(out, "malformed-tag-entry-dropped") {
		t.Errorf("set exit = %d, want 0 warning the drop; stdout=%q stderr=%q", code, out, stderr)
	}
}

// TestMP3MalformedAPICSurfaced: malformed APIC surfaces invalid-picture on dump/lint.
func TestMP3MalformedAPICSurfaced(t *testing.T) {
	frames, err := os.ReadFile(filepath.Join("..", "..", "testdata", "notags.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	apicBody := []byte("\x00image/png") // text-encoding byte + a MIME with no NUL terminator
	tagBytes := testfile.ID3v2(4, testfile.ID3Frame(4, "APIC", apicBody))
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

// TestEmptyWAVJSONNoBitrate: zero-duration WAV omits bitrateBps in JSON (Duration()>0 gate).
func TestEmptyWAVJSONNoBitrate(t *testing.T) {
	emptyWAV := filepath.Join("..", "..", "testdata", "empty.wav")
	stdout, _, code := runCLI(t, "dump", "--json", emptyWAV)
	if code != 0 {
		t.Fatalf("dump --json exit = %d, want 0", code)
	}
	if strings.Contains(stdout, "bitrateBps") {
		t.Errorf("zero-duration WAV must not emit bitrateBps in JSON:\n%s", stdout)
	}
	// Fixture must be zero-duration or gate does not apply.
	if strings.Contains(stdout, "durationMs") {
		t.Errorf("empty.wav is unexpectedly non-zero-duration; fixture assumption broken:\n%s", stdout)
	}
}

// TestSubMillisecondWAVJSONConsistent: sub-ms WAV drops durationMs and bitrateBps together.
func TestSubMillisecondWAVJSONConsistent(t *testing.T) {
	wav := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVData(40)) // 10 frames ~ 0.23 ms

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
