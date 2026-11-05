package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/testfile"
)

// writeInfoOnlyWAV writes a PCM WAV whose only metadata is a LIST/INFO chunk.
func writeInfoOnlyWAV(t *testing.T, name string, pairs ...[2]string) string {
	t.Helper()
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo(pairs...), testfile.WAVData(4000))
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// wavChunkKinds: top-level WAV chunk kinds from dump --native --json (structural check).
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

// TestLintFixKeepsInfoOnlyWAVStructural: lint --fix must not add id3 or restructure INFO-only WAV.
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
	// Second lint --fix is a no-op.
	fixOut, _, code := runCLI(t, "lint", "--fix", path)
	if code != 0 || !strings.Contains(fixOut, "nothing to fix") {
		t.Errorf("re-running --fix exit = %d, output:\n%s", code, fixOut)
	}
}

// TestSetEncoderOnInfoOnlyWAVKeepsStructure: ENCODER via ISFT must not add id3 chunk.
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
