package main

import "testing"

// TestOversizedPictureIsAWriteRefusal: oversized cover is exit 3 picture-too-large, not exit 4.
// Threshold pinned in review_test.go; 16 MiB is FLAC 24-bit picture body limit.
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
