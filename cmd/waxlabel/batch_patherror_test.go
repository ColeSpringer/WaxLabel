package main

import (
	"testing"
)

// batchElem is the minimal shape every list command's --json element shares: the file
// it describes, and an error object when that element failed. It is enough to assert
// the per-element batch contract without each command's full result struct (json
// decoding ignores the other fields).
type batchElem struct {
	File  string `json:"file"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

// TestDirectoryInBatchIsPerElementError: a directory argument without
// --recursive sitting between two good files must not collapse the whole batch into a
// single fileless error. Each list command emits one element per input - the good
// files succeed (no error), and the directory is its own "usage"-coded error carrying
// its file - so a --json consumer keeps the one-element-per-input contract and the
// good results survive. Previously expandPaths aborted the run up front, dropping
// both good results into a single error with no file field.
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
			// A failure occurred (the directory), but it did not abort the batch. The exact
			// class is worseError's job; the per-element "usage" code below is the contract.
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
