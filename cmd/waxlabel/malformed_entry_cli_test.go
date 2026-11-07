package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// TestStrictHintDoesNotPromiseAWrite: discard-family hint must not say "write anyway" when no bytes write.
func TestStrictHintDoesNotPromiseAWrite(t *testing.T) {
	t.Parallel()
	file := copyFixture(t, "../../testdata/sample.webm")
	png := writeTempImage(t, "cover.png", testfile.PNG())
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

// wavItem renders one LIST/INFO sub-chunk: a 4CC and a NUL-terminated value, padded.
func wavItem(id, value string) []byte { return testfile.WAVChunk(id, append([]byte(value), 0)) }

// wavWithListBody builds a WAV around a verbatim LIST chunk body (item walk may fail).
func wavWithListBody(list []byte) []byte {
	return testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVChunk("LIST", list), testfile.WAVData(4000))
}

// wavUnpaddedInfo: LIST/INFO items without word-align pad; unconditional pad step desyncs on rewrite.
func wavUnpaddedInfo(pairs ...[2]string) []byte {
	info := []byte("INFO")
	for _, p := range pairs {
		val := append([]byte(p[1]), 0)
		info = slices.Concat(info, []byte(p[0]), testfile.LE32(len(val)), val)
	}
	return wavWithListBody(info)
}

// TestLintReportsMalformedTagEntry: malformed LIST/INFO reaches lint as warning (CI gate).
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
	// Values past desync survive rewrite; rewritten file lints clean.
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

// TestLintUnknownChunkSizeStaysClean: size-unknown sentinel (piped WAV) is info only; exit 0.
func TestLintUnknownChunkSizeStaysClean(t *testing.T) {
	t.Parallel()
	data := wavUnpaddedInfo() // empty LIST dropped by walk; fmt+data matter
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

// TestStrictRefusesDroppedMalformedRegion: unreadable LIST region destroyed on rewrite; --strict refuses.
func TestStrictRefusesDroppedMalformedRegion(t *testing.T) {
	t.Parallel()
	// Well-formed item, then 3 bytes no item walk can read.
	list := slices.Concat([]byte("INFO"), []byte("INAM"), testfile.LE32(5), []byte("Song\x00"), []byte{0},
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

// TestLintFixReportsWhatItDestroyed: --fix must report bytes lost on rewrite (re-lint cannot).
func TestLintFixReportsWhatItDestroyed(t *testing.T) {
	t.Parallel()
	// ISFT gives --fix work; trailing 3 bytes unreadable to item walk.
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

// TestLintFixOnACleanFileReportsNoLoss: ordinary fix loses nothing; no "lost in the rewrite" line.
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

// TestLintReportsNonStandardFrameID: an ID3v2 frame with a lowercase ID reaches lint as
// malformed-tag-entry, naming it, and the frames after it still read.
func TestLintReportsNonStandardFrameID(t *testing.T) {
	t.Parallel()
	audio, err := os.ReadFile("../../testdata/notags.mp3")
	if err != nil {
		t.Fatal(err)
	}
	file := writeTempFile(t, "lower.mp3", slices.Concat(testfile.ID3v2(3,
		testfile.TextFrame(3, "Tlow", "odd"), testfile.TextFrame(3, "TPE1", "Band")), audio))
	out, _, code := runCLI(t, "--json", "lint", file)
	if code != 1 {
		t.Fatalf("lint exit = %d, want 1 (findings present)\n%s", code, out)
	}
	jl := decodeJSONList[jsonLint](t, out)
	if len(jl) != 1 || !slices.ContainsFunc(jl[0].Findings, func(f jsonFinding) bool {
		return f.Code == "malformed-tag-entry" && f.Severity == "warning" && strings.Contains(f.Message, "Tlow")
	}) {
		t.Errorf("lint did not report the Tlow frame as malformed-tag-entry:\n%s", out)
	}
	if dumped := mustDumpJSON(t, file); !strings.Contains(dumped, "Band") {
		t.Errorf("the frame after Tlow did not read:\n%s", dumped)
	}
}

// TestDumpNativeNotesPlainV24FrameSizes: an iTunes-style ID3v2.4 tag with a plain-size frame over
// 127 bytes reads whole, and dump --native says its sizes are not sync-safe.
func TestDumpNativeNotesPlainV24FrameSizes(t *testing.T) {
	t.Parallel()
	audio, err := os.ReadFile("../../testdata/notags.mp3")
	if err != nil {
		t.Fatal(err)
	}
	comment := slices.Concat([]byte{0}, []byte("eng\x00"), []byte(strings.Repeat("c", 200)))
	plain := slices.Concat([]byte("COMM"), testfile.BE32(len(comment)), []byte{0, 0}, comment)
	file := writeTempFile(t, "plain.mp3", slices.Concat(testfile.ID3v2(4, plain, testfile.TextFrame(4, "TPE1", "Band")), audio))
	out, errb, code := runCLI(t, "dump", "--native", file)
	if code != 0 {
		t.Fatalf("dump exit = %d: %s", code, errb)
	}
	if !strings.Contains(out, "non-sync-safe frame sizes") || !strings.Contains(out, "Band") {
		t.Errorf("dump --native does not note the plain sizes or lost the frame after the comment:\n%s", out)
	}
}

// TestStrictRefusesDroppingADuplicateID3v2Tag: a second ID3v2 tag chained behind a populated one is a
// duplicate lint reports, and a write that drops it names what it held, a value and a frame the
// canonical view does not carry: --strict refuses, a plain set warns. An empty tag joined ahead of a
// tagged MP3 yields to it, so a strict edit there loses nothing.
func TestStrictRefusesDroppingADuplicateID3v2Tag(t *testing.T) {
	t.Parallel()
	audio, err := os.ReadFile("../../testdata/notags.mp3")
	if err != nil {
		t.Fatal(err)
	}
	second := testfile.ID3v2(3, testfile.TextFrame(3, "TALB", "Second Album"), testfile.ID3Frame(3, "POPM", []byte("me@example.com\x00\xC8")))
	file := writeTempFile(t, "dup.mp3", slices.Concat(testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Front")), second, audio))
	if out, _, code := runCLI(t, "lint", file); code != 4 || !strings.Contains(out, "[error] duplicate-tag-block") {
		t.Errorf("lint exit = %d, want 4 reporting the duplicate as an error:\n%s", code, out)
	}
	_, errb, code := runCLI(t, "set", file, "--strict", "--set", "TITLE=New")
	if code != 2 || !strings.Contains(errb, "ALBUM") || !strings.Contains(errb, "POPM") {
		t.Fatalf("strict exit = %d, want 2 naming ALBUM and the POPM frame: %s", code, errb)
	}
	out, errb, code := runCLI(t, "set", file, "--set", "TITLE=New")
	if code != 0 || !strings.Contains(out, "duplicate-tag-block-dropped") {
		t.Fatalf("set exit = %d, want 0 warning the drop:\n%s%s", code, out, errb)
	}
	if dumped := mustDumpJSON(t, file); strings.Contains(dumped, "duplicate-tag-block") {
		t.Errorf("the rewritten file still holds a duplicate:\n%s", dumped)
	}

	var joined []byte
	for _, name := range []string{"empty.mp3", "sample24.mp3"} {
		b, err := os.ReadFile("../../testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		joined = append(joined, b...)
	}
	file = writeTempFile(t, "joined.mp3", joined)
	if got := tagValues(dumpJSON(t, file), "ALBUM"); len(got) != 1 {
		t.Errorf("ALBUM = %q, want the tagged file's", got)
	}
	if _, errb, code := runCLI(t, "set", file, "--strict", "--set", "COMMENT=Note"); code != 0 {
		t.Errorf("strict set on the joined file exit = %d, want 0 (the empty tag holds nothing): %s", code, errb)
	}
}

// TestStrictRefusesDroppingUnreadableID3Bytes: a rewrite of an ID3v2 tag the frame walk could not
// read to its end, or of a front tag declaring more bytes than the file holds, drops bytes it never
// read: --strict refuses, a plain set warns malformed-tag-entry-dropped.
func TestStrictRefusesDroppingUnreadableID3Bytes(t *testing.T) {
	t.Parallel()
	audio, err := os.ReadFile("../../testdata/notags.mp3")
	if err != nil {
		t.Fatal(err)
	}
	pastEnd := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Cut"))
	copy(pastEnd[6:10], testfile.Syncsafe(len(audio)+1000))
	for name, front := range map[string][]byte{
		"control byte in an ID": testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Song"), testfile.TextFrame(3, "T\x01LW", "junk")),
		"cut header":            testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Song"), []byte("TPE")),
		"tag past the end":      pastEnd,
	} {
		file := writeTempFile(t, "unreadable.mp3", slices.Concat(front, audio))
		if _, errb, code := runCLI(t, "set", file, "--strict", "--set", "ARTIST=Band"); code != 2 {
			t.Errorf("%s: strict exit = %d, want 2: %s", name, code, errb)
		}
		if out, errb, code := runCLI(t, "set", file, "--set", "ARTIST=Band"); code != 0 || !strings.Contains(out, "malformed-tag-entry-dropped") {
			t.Errorf("%s: set exit = %d, want 0 warning the drop:\n%s%s", name, code, out, errb)
		}
	}
}
