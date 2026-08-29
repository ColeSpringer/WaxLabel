package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestCopyToReadOnlyDestinationExits3 is the regression guard for a refusal that used to
// arrive as an exit 0: the transfer set nothing on the editor, so the codec's no-op fast
// path returned before its own refusal could run. The per-field drops are still printed -
// the user needs to know what would not carry - and then the command fails.
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

	// --dry-run reaches the same refusal, since it comes before the write.
	if _, _, code := runCLI(t, "copy", td("sample.flac"), dst, "--dry-run"); code != 3 {
		t.Errorf("copy --dry-run exit = %d, want 3", code)
	}

	// The JSON envelope carries the codec's own code AND the per-item detail, so a script
	// is not left with an exit status and no account of what could not be carried - which
	// is exactly what the human surface prints above it.
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

// TestCopyWithNothingToCarryStillSucceeds is the boundary: the refusal is gated on a
// dropped item, not on the destination being read-only. A transfer that asks to write
// nothing writes nothing and succeeds, which is what set does on the same file.
func TestCopyWithNothingToCarryStillSucceeds(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, td("sample.wma"))
	out, _, code := runCLI(t, "copy", td("notags.flac"), dst)
	if code != 0 {
		t.Errorf("copy with nothing to carry exit = %d, want 0:\n%s", code, out)
	}
}

// TestCopyToWritableDestinationWithDropsSucceeds is the other boundary: the gate is the
// destination being read-only, checked before any disposition. A writable destination that
// cannot hold one item (WebM has no cover-art attachment) still carries the rest and saves.
func TestCopyToWritableDestinationWithDropsSucceeds(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, td("sample.webm"))
	out, _, code := runCLI(t, "copy", td("sample.m4a"), dst)
	if code != 0 {
		t.Errorf("copy onto a writable destination with drops exit = %d, want 0:\n%s", code, out)
	}
}

// TestCopyStrictRefusesLossyTransfer: --strict on copy means "this must be a faithful
// carry". A dropped item is exactly the loss the report already counts, so it fails before
// any write rather than saving a file that lost something. APEv2 has no chapter
// convention, so a chaptered Matroska onto a WavPack drops all three.
func TestCopyStrictRefusesLossyTransfer(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, td("notags.wv"))
	before, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	// Guard the fixture assumption: without a drop there is nothing for --strict to catch.
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
	// A strict run writes nothing, so its envelope is the user's only account of what to
	// fix: counts alone would not name the items.
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
	// Without --strict the same copy is a normal, reported loss that still writes.
	if _, _, code := runCLI(t, "copy", td("chapters.mka"), dst); code != 0 {
		t.Errorf("copy without --strict exit = %d, want 0", code)
	}
}

// TestCopyStrictAllowsLosslessTransfer is the negative: a carry that loses nothing must
// still write, or --strict would be unusable on the ordinary case.
func TestCopyStrictAllowsLosslessTransfer(t *testing.T) {
	t.Parallel()
	dst := copyFixture(t, td("notags.flac"))
	if out, _, code := runCLI(t, "copy", td("sample.flac"), dst, "--strict"); code != 0 {
		t.Errorf("copy --strict on a lossless transfer exit = %d, want 0:\n%s", code, out)
	}
}

// decodeCopyJSON decodes copy's single-object envelope (it is not a list command).
func decodeCopyJSON(t *testing.T, data string) jsonCopy {
	t.Helper()
	var jc jsonCopy
	if err := json.Unmarshal([]byte(data), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s", err, data)
	}
	return jc
}
