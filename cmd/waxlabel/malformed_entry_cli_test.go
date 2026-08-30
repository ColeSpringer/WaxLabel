package main

import (
	"encoding/binary"
	"slices"
	"strings"
	"testing"
)

// TestStrictHintDoesNotPromiseAWrite: the gate appended "(omit --strict to write anyway)"
// unconditionally. That is true for a coercion or a reduction and false for the discard
// family, where omitting --strict drops the item either way - and here writes no bytes at
// all. One hint that is unconditionally true replaces it.
func TestStrictHintDoesNotPromiseAWrite(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, "../../testdata/sample.webm")
	png := writeTempImage(t, "cover.png", minimalPNG())
	_, errb, code := runCLI(t, "set", file, "--add-picture", "front-cover="+png, "--strict")
	if code != 2 {
		t.Fatalf("exit = %d, want 2: %s", code, errb)
	}
	if strings.Contains(errb, "write anyway") {
		t.Errorf("the hint promises a write that never happens:\n%s", errb)
	}
	if !strings.Contains(errb, "omit --strict to continue with a warning") {
		t.Errorf("the corrected hint is missing:\n%s", errb)
	}
}

// wavLE32 renders a little-endian RIFF size field.
func wavLE32(n int) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(n))
	return b
}

// wavChunk wraps a chunk body in its 8-byte header, word-aligning with a pad byte when the
// body length is odd.
func wavChunk(id string, body []byte) []byte {
	out := slices.Concat([]byte(id), wavLE32(len(body)), body)
	if len(body)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

// wavItem renders one LIST/INFO sub-chunk: a 4CC and a NUL-terminated value, padded.
func wavItem(id, value string) []byte { return wavChunk(id, append([]byte(value), 0)) }

// wavFmtChunk is a 16-byte PCM "fmt " chunk: 44100 Hz, stereo, 16-bit.
func wavFmtChunk() []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint16(b[0:], 1)      // PCM
	binary.LittleEndian.PutUint16(b[2:], 2)      // channels
	binary.LittleEndian.PutUint32(b[4:], 44100)  // sample rate
	binary.LittleEndian.PutUint32(b[8:], 176400) // byte rate
	binary.LittleEndian.PutUint16(b[12:], 4)     // block align
	binary.LittleEndian.PutUint16(b[14:], 16)    // bits per sample
	return wavChunk("fmt ", b)
}

// wavWrap puts the RIFF/WAVE header around assembled chunk bytes.
func wavWrap(chunks []byte) []byte {
	inner := append([]byte("WAVE"), chunks...)
	return slices.Concat([]byte("RIFF"), wavLE32(len(inner)), inner)
}

// wavWithListBody assembles a WAV around a verbatim LIST chunk body, so a test can hand in
// a list the item walk cannot fully read.
func wavWithListBody(list []byte) []byte {
	return wavWrap(slices.Concat(wavFmtChunk(), wavChunk("LIST", list), wavChunk("data", make([]byte, 4000))))
}

// wavUnpaddedInfo builds a WAV whose LIST/INFO items carry no word-alignment pad byte after
// an odd-size value, so a reader that steps over the byte unconditionally desynchronizes on
// the second item and loses every item after it on the next rewrite.
func wavUnpaddedInfo(pairs ...[2]string) []byte {
	info := []byte("INFO")
	for _, p := range pairs {
		val := append([]byte(p[1]), 0)
		info = slices.Concat(info, []byte(p[0]), wavLE32(len(val)), val)
	}
	return wavWithListBody(info)
}

// TestLintReportsMalformedTagEntry: the read code has to reach lint as a warning-severity
// finding, so a file that cannot be read in full fails a CI gate rather than passing
// silently.
func TestLintReportsMalformedTagEntry(t *testing.T) {
	t.Parallel()
	file := writeTempFile(t, "unpadded.wav",
		wavUnpaddedInfo([2]string{"INAM", "Song"}, [2]string{"IART", "Band"}, [2]string{"IPRD", "Album"}))
	out, _, code := runCLI(t, "--json", "lint", file)
	if code != 1 {
		t.Fatalf("lint exit = %d, want 1 (findings present)\n%s", code, out)
	}
	jl := decodeJSONList[jsonLint](t, out)
	if len(jl) != 1 {
		t.Fatalf("want one lint result, got %d: %s", len(jl), out)
	}
	found := false
	for _, f := range jl[0].Findings {
		if f.Code != "malformed-tag-entry" {
			continue
		}
		found = true
		if f.Severity != "warning" {
			t.Errorf("malformed-tag-entry severity = %q, want warning", f.Severity)
		}
	}
	if !found {
		t.Errorf("lint did not report malformed-tag-entry:\n%s", out)
	}
	// The values past the desync survive the rewrite, and the rewritten file lints clean.
	if _, errb, code := runCLI(t, "set", file, "--set", "GENRE=Rock"); code != 0 {
		t.Fatalf("set exit = %d: %s", code, errb)
	}
	dumped := mustDumpJSON(t, file)
	for _, want := range []string{"Album", "Band"} {
		if !strings.Contains(dumped, want) {
			t.Errorf("the rewrite dropped %q:\n%s", want, dumped)
		}
	}
	if _, _, code := runCLI(t, "lint", file); code != 0 {
		t.Errorf("the rewritten file still lints non-clean (exit %d)", code)
	}
}

// TestLintUnknownChunkSizeStaysClean: the size-unknown sentinel is what a non-seekable
// writer emits, so reporting it must not fail the very common piped-WAV case. It is info
// severity: visible in lint, exit code untouched.
func TestLintUnknownChunkSizeStaysClean(t *testing.T) {
	t.Parallel()
	data := wavUnpaddedInfo() // a LIST with no items is dropped by the walk; only fmt+data matter
	i := strings.Index(string(data), "data")
	copy(data[i+4:i+8], []byte{0xFF, 0xFF, 0xFF, 0xFF})
	file := writeTempFile(t, "sentinel.wav", data)

	out, _, code := runCLI(t, "--json", "lint", file)
	if code != 0 {
		t.Fatalf("lint exit = %d, want 0 (an info finding must not fail the gate)\n%s", code, out)
	}
	jl := decodeJSONList[jsonLint](t, out)
	if len(jl) != 1 {
		t.Fatalf("want one lint result, got %d: %s", len(jl), out)
	}
	found := false
	for _, f := range jl[0].Findings {
		if f.Code != "unknown-chunk-size" {
			continue
		}
		found = true
		if f.Severity != "info" {
			t.Errorf("unknown-chunk-size severity = %q, want info", f.Severity)
		}
	}
	if !found {
		t.Errorf("lint did not report unknown-chunk-size:\n%s", out)
	}
}

// TestStrictRefusesDroppedMalformedRegion: a region the item walk cannot read has nowhere
// to go in a rebuilt LIST/INFO chunk, so the rewrite destroys it. That is destruction by
// this write, which is exactly what --strict is for; without the flag the same write
// proceeds and reports the loss.
func TestStrictRefusesDroppedMalformedRegion(t *testing.T) {
	t.Parallel()
	// One well-formed item, then three bytes no item walk can read: too short to hold
	// another item header, and not the clean end of the list.
	list := slices.Concat([]byte("INFO"), []byte("INAM"), wavLE32(5), []byte("Song\x00"), []byte{0},
		[]byte{0x01, 0x02, 0x03})
	file := writeTempFile(t, "tail.wav", wavWithListBody(list))

	_, errb, code := runCLI(t, "set", file, "--strict", "--set", "ARTIST=Band")
	if code != 2 {
		t.Fatalf("exit = %d, want 2: %s", code, errb)
	}
	if !strings.Contains(errb, "could not be read") {
		t.Errorf("the refusal does not say what the write destroys:\n%s", errb)
	}
	out, errb, code := runCLI(t, "set", file, "--set", "ARTIST=Band")
	if code != 0 {
		t.Fatalf("non-strict exit = %d: %s", code, errb)
	}
	if !strings.Contains(out, "malformed-tag-entry-dropped") {
		t.Errorf("the plan did not report the dropped region:\n%s", out)
	}
}

// TestLintFixReportsWhatItDestroyed: --fix promises only provably-safe repairs, and
// re-linting cannot show what a rewrite destroyed on the way, because the condition is gone
// from the output. A region no parser could read is re-rendered away by any write, so the
// fix has to say so rather than report the file as clean.
func TestLintFixReportsWhatItDestroyed(t *testing.T) {
	t.Parallel()
	// An ISFT transcoder stamp gives --fix something to do; the three trailing bytes are a
	// region no item walk can read.
	list := slices.Concat([]byte("INFO"),
		wavItem("INAM", "Song"), wavItem("ISFT", "Lavf61.7.100"), []byte{0x01, 0x02, 0x03})
	file := writeTempFile(t, "fix.wav", wavWithListBody(list))

	out, _, code := runCLI(t, "lint", "--fix", file)
	if code != 0 {
		t.Fatalf("lint --fix exit = %d: %s", code, out)
	}
	if !strings.Contains(out, "lost in the rewrite") || !strings.Contains(out, "malformed-tag-entry-dropped") {
		t.Errorf("the fix destroyed a region without saying so:\n%s", out)
	}
	if !strings.Contains(out, "3 byte(s)") {
		t.Errorf("the report does not count the destroyed bytes exactly:\n%s", out)
	}
}

// TestLintFixOnACleanFileReportsNoLoss is the control: the ordinary fix loses nothing, so
// the new line must not appear on every run.
func TestLintFixOnACleanFileReportsNoLoss(t *testing.T) {
	t.Parallel()
	list := slices.Concat([]byte("INFO"), wavItem("INAM", "Song"), wavItem("ISFT", "Lavf61.7.100"))
	file := writeTempFile(t, "clean.wav", wavWithListBody(list))

	out, _, code := runCLI(t, "lint", "--fix", file)
	if code != 0 {
		t.Fatalf("lint --fix exit = %d: %s", code, out)
	}
	if strings.Contains(out, "lost in the rewrite") {
		t.Errorf("a fix that loses nothing must not report a loss:\n%s", out)
	}
}
