package main

import "testing"

// An oversized cover is a refusal to write, not a verdict on the input file: the file reads
// fine and only this invocation's image does not fit. It used to exit 4 (invalid-data,
// documented as "the file is corrupt"), so a batch script that quarantines exit-4 files
// quarantined healthy ones. It now exits 3 with its own code, alongside the other
// write refusals.
//
// One case only, driving the real CLI path: the sentinel's own threshold is already pinned
// in-memory by tests/review_test.go, and a success control would write another ~15 MiB per
// run for no added coverage. 16 MiB is the FLAC picture block's 24-bit body limit.
func TestOversizedPictureIsAWriteRefusal(t *testing.T) {
	t.Parallel()
	big := make([]byte, 16<<20)
	copy(big, minimalPNG()) // a real header, so the image-recognition gate is not what rejects it
	cover := writeTempImage(t, "huge.png", big)

	stdout, stderr, code := runCLI(t, "set", "--json", copyFixture(t, sampleFLAC), "--add-cover", cover)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (a write refusal, not a corrupt file)\n%s\n%s", code, stdout, stderr)
	}
	if got := decodeJSONOne[jsonErrorEntry](t, stdout).Error.Code; got != "picture-too-large" {
		t.Errorf("machine code = %q, want %q", got, "picture-too-large")
	}
}
