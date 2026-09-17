package main

import (
	"testing"
)

// batchElem is the shared list-command JSON element shape: file plus optional error.
type batchElem struct {
	File  string `json:"file"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

// TestDirectoryInBatchIsPerElementError: a non-recursive directory between good files is
// a per-element usage error, not a fileless batch abort. Good results must survive.
func TestDirectoryInBatchIsPerElementError(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"dump", "verify", "lint", "plan", "set"} {
		cmd := cmd
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			good1 := copyFixture(t, sampleFLAC)
			good2 := copyFixture(t, sampleFLAC)
			dir := t.TempDir()

			args := []string{cmd, "--json", good1, dir, good2}
			if cmd == "set" || cmd == "plan" {
				args = append(args, "--set", "TITLE=X")
			}
			out, _, code := runCLI(t, args...)
			// Non-zero exit expected; per-element usage code is the contract here.
			if code == 0 {
				t.Fatalf("%s exit = 0, want non-zero (the directory is an error)", cmd)
			}
			elems := decodeJSONList[batchElem](t, out)
			if len(elems) != 3 {
				t.Fatalf("%s: %d elements, want 3 (good, dir, good)\n%s", cmd, len(elems), out)
			}
			if elems[0].Error != nil {
				t.Errorf("%s elem[0] (%s) should succeed, got error %+v", cmd, elems[0].File, elems[0].Error)
			}
			if elems[1].Error == nil || elems[1].Error.Code != "usage" {
				t.Errorf("%s elem[1] should be a usage error, got %+v", cmd, elems[1].Error)
			}
			if elems[1].File != dir {
				t.Errorf("%s elem[1].file = %q, want the directory %q", cmd, elems[1].File, dir)
			}
			if elems[2].Error != nil {
				t.Errorf("%s elem[2] (%s) should succeed, got error %+v", cmd, elems[2].File, elems[2].Error)
			}
		})
	}
}
