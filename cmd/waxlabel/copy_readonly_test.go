package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestCopyToReadOnlyDestinationExits3: read-only destination is exit 3, not silent 0. Per-field
// drops still print before the refusal.
func TestCopyToReadOnlyDestinationExits3(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, td("sample.wma"))
	before, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}

	out, errOut, code := runCLI(t, "copy", td("sample.flac"), dst)
	if code != 3 {
		t.Fatalf("copy onto a read-only destination exit = %d, want 3:\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "destination is read-only") {
		t.Errorf("the per-field drops should still be shown before the refusal:\n%s", out)
	}
	after, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the refused copy modified the destination")
	}

	// --dry-run hits the same refusal before write.
	if _, _, code := runCLI(t, "copy", td("sample.flac"), dst, "--dry-run"); code != 3 {
		t.Errorf("copy --dry-run exit = %d, want 3", code)
	}

	// JSON carries both exit code and per-item drops, matching the human report.
	jout, _, jcode := runCLI(t, "--json", "copy", td("sample.flac"), dst)
	if jcode != 3 {
		t.Errorf("--json copy exit = %d, want 3", jcode)
	}
	jc := decodeCopyJSON(t, jout)
	if jc.Error == nil || jc.Error.Code != "unsupported-format" {
		t.Errorf("--json copy error = %+v, want unsupported-format", jc.Error)
	}
	if !slices.ContainsFunc(jc.Transfer, func(it jsonTransferItem) bool {
		return it.Disposition == "dropped" && it.Reason == "destination is read-only"
	}) {
		t.Errorf("--json copy lost the transfer report: %+v", jc.Transfer)
	}
}

// TestCopyWithNothingToCarryStillSucceeds: read-only refusal needs a dropped item, not just RO dst.
func TestCopyWithNothingToCarryStillSucceeds(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, td("sample.wma"))
	out, _, code := runCLI(t, "copy", td("notags.flac"), dst)
	if code != 0 {
		t.Errorf("copy with nothing to carry exit = %d, want 0:\n%s", code, out)
	}
}

// TestCopyToWritableDestinationWithDropsSucceeds: RO gate is destination writability, not drops.
func TestCopyToWritableDestinationWithDropsSucceeds(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, td("sample.webm"))
	out, _, code := runCLI(t, "copy", td("sample.m4a"), dst)
	if code != 0 {
		t.Errorf("copy onto a writable destination with drops exit = %d, want 0:\n%s", code, out)
	}
}

// TestCopyStrictRefusesLossyTransfer: --strict fails before write when anything drops
// (Matroska chapters onto WavPack drops all three).
func TestCopyStrictRefusesLossyTransfer(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, td("notags.wv"))
	before, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	// Fixture must drop chapters under dry-run or --strict has nothing to catch.
	if out, _, code := runCLI(t, "copy", td("chapters.mka"), dst, "--dry-run"); code != 0 ||
		!strings.Contains(out, "dropped chapters") {
		t.Fatalf("setup: expected the chapters to drop; exit = %d:\n%s", code, out)
	}

	out, errOut, code := runCLI(t, "copy", td("chapters.mka"), dst, "--strict")
	if code != 2 {
		t.Fatalf("copy --strict on a lossy transfer exit = %d, want 2:\n%s\n%s", code, out, errOut)
	}
	after, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("copy --strict refused but wrote anyway")
	}
	// Strict JSON must list dropped items; counts alone are not enough.
	jout, _, jcode := runCLI(t, "--json", "copy", td("chapters.mka"), dst, "--strict")
	if jcode != 2 {
		t.Errorf("--json copy --strict exit = %d, want 2", jcode)
	}
	jc := decodeCopyJSON(t, jout)
	if jc.Error == nil {
		t.Error("--json copy --strict emitted no error body")
	}
	if !slices.ContainsFunc(jc.Transfer, func(it jsonTransferItem) bool {
		return it.Disposition == "dropped" && strings.Contains(it.Reason, "chapters")
	}) {
		t.Errorf("--json copy --strict lost the transfer report: %+v", jc.Transfer)
	}
	// Without --strict the same copy writes with a reported loss.
	if _, _, code := runCLI(t, "copy", td("chapters.mka"), dst); code != 0 {
		t.Errorf("copy without --strict exit = %d, want 0", code)
	}
}

// TestCopyStrictAllowsLosslessTransfer: lossless carry still writes under --strict.
func TestCopyStrictAllowsLosslessTransfer(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, td("notags.flac"))
	if out, _, code := runCLI(t, "copy", td("sample.flac"), dst, "--strict"); code != 0 {
		t.Errorf("copy --strict on a lossless transfer exit = %d, want 0:\n%s", code, out)
	}
}

// decodeCopyJSON decodes copy's single-object envelope (not a list command).
func decodeCopyJSON(t *testing.T, data string) jsonCopy {
	t.Helper()
	var jc jsonCopy
	if err := json.Unmarshal([]byte(data), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s", err, data)
	}
	return jc
}
