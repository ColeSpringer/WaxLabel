package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// riffChunk wraps a chunk body in its 8-byte header, word-aligning an odd body.
func riffChunk(id string, body []byte) []byte {
	h := make([]byte, 4)
	binary.LittleEndian.PutUint32(h, uint32(len(body)))
	out := slices.Concat([]byte(id), h, body)
	if len(body)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

// riffInfoList builds a LIST/INFO chunk from ordered 4CC/value pairs.
func riffInfoList(pairs ...[2]string) []byte {
	body := []byte("INFO")
	for _, p := range pairs {
		body = append(body, riffChunk(p[0], append([]byte(p[1]), 0))...)
	}
	return riffChunk("LIST", body)
}

// writeInfoOnlyWAV writes a PCM WAV whose only metadata is a LIST/INFO chunk.
func writeInfoOnlyWAV(t *testing.T, name string, pairs ...[2]string) string {
	t.Helper()
	fmtBody := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtBody[0:], 1)      // PCM
	binary.LittleEndian.PutUint16(fmtBody[2:], 2)      // channels
	binary.LittleEndian.PutUint32(fmtBody[4:], 44100)  // sample rate
	binary.LittleEndian.PutUint32(fmtBody[8:], 176400) // byte rate
	binary.LittleEndian.PutUint16(fmtBody[12:], 4)     // block align
	binary.LittleEndian.PutUint16(fmtBody[14:], 16)    // bits per sample
	inner := slices.Concat([]byte("WAVE"), riffChunk("fmt ", fmtBody),
		riffInfoList(pairs...), riffChunk("data", make([]byte, 4000)))
	sz := make([]byte, 4)
	binary.LittleEndian.PutUint32(sz, uint32(len(inner)))
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, slices.Concat([]byte("RIFF"), sz, inner), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// wavChunkKinds lists a WAV's top-level chunk kinds via dump --native --json. Nested
// entries (the INFO items, which Describe indents) are left out: this answers "what chunks
// does the file hold", which is what a structural remediation must not change.
func wavChunkKinds(t *testing.T, path string) []string {
	t.Helper()
	out, _, code := runCLI(t, "--json", "dump", "--native", path)
	if code != 0 {
		t.Fatalf("dump --native exit = %d, want 0:\n%s", code, out)
	}
	var kinds []string
	for _, n := range decodeJSONOne[jsonDocument](t, out).Native {
		if !strings.HasPrefix(n.Kind, " ") {
			kinds = append(kinds, n.Kind)
		}
	}
	return kinds
}

// TestLintFixKeepsInfoOnlyWAVStructural is the regression guard for lint --fix on a
// LIST/INFO-only WAV: remediation is documented as safe and non-destructive, so it must not
// restructure the file. It used to add an id3 chunk holding the TRACKTOTAL its own read had
// split out of IPRT="4/9", and rewrite IPRT to a bare "4". The assertion is the chunk list,
// not the finding count: the failure mode is a chunk appearing, which a count-based check
// would not see.
func TestLintFixKeepsInfoOnlyWAVStructural(t *testing.T) {
	t.Parallel()
	path := writeInfoOnlyWAV(t, "infoonly.wav",
		[2]string{"INAM", "Song"}, [2]string{"IPRT", "4/9"}, [2]string{"ISFT", "Lavf61.7.100"})
	before := wavChunkKinds(t, path)

	if _, _, code := runCLI(t, "lint", path); code != 1 {
		t.Fatalf("setup: lint exit = %d, want 1 (the inherited encoder stamp)", code)
	}
	if _, _, code := runCLI(t, "lint", "--fix", path); code != 0 {
		t.Fatalf("lint --fix exit = %d, want 0", code)
	}
	if got := wavChunkKinds(t, path); !slices.Equal(got, before) {
		t.Errorf("lint --fix restructured the file: chunks %v -> %v", before, got)
	}
	out, _, code := runCLI(t, "--json", "dump", path)
	if code != 0 {
		t.Fatalf("dump exit = %d, want 0", code)
	}
	jd := decodeJSONOne[jsonDocument](t, out)
	if v := tagValues(jd, "TRACKTOTAL"); !slices.Equal(v, []string{"9"}) {
		t.Errorf("TRACKTOTAL = %v, want [9] (the slashed IPRT must survive)", v)
	}
	if v := tagValues(jd, "ENCODER"); v != nil {
		t.Errorf("ENCODER = %v, want absent after --fix", v)
	}
	// A second run has nothing left to do.
	fixOut, _, code := runCLI(t, "lint", "--fix", path)
	if code != 0 || !strings.Contains(fixOut, "nothing to fix") {
		t.Errorf("re-running --fix exit = %d, output:\n%s", code, fixOut)
	}
}

// TestSetEncoderOnInfoOnlyWAVKeepsStructure is the companion for a plain edit: ENCODER has an
// INFO home (ISFT), so writing it no longer promotes the file to an id3 chunk.
func TestSetEncoderOnInfoOnlyWAVKeepsStructure(t *testing.T) {
	t.Parallel()
	path := writeInfoOnlyWAV(t, "encoder.wav", [2]string{"INAM", "Song"})
	before := wavChunkKinds(t, path)
	if _, _, code := runCLI(t, "set", path, "--set", "ENCODER=MyTagger 1.0"); code != 0 {
		t.Fatalf("set --set ENCODER exit = %d, want 0", code)
	}
	if got := wavChunkKinds(t, path); !slices.Equal(got, before) {
		t.Errorf("an ENCODER edit restructured the file: chunks %v -> %v", before, got)
	}
	out, _, _ := runCLI(t, "--json", "dump", path)
	if v := tagValues(decodeJSONOne[jsonDocument](t, out), "ENCODER"); !slices.Equal(v, []string{"MyTagger 1.0"}) {
		t.Errorf("ENCODER = %v, want [MyTagger 1.0]", v)
	}
}
