package waxlabel

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestParseLimitsThreadedToVerify covers the --verify limits fix: the limits a document is parsed
// under are recorded on it, so a WithVerifyEssence write re-parses the output under the same ceilings
// the original parse cleared - not the write options' defaults (which would fail an elevated-limit
// document's own structural re-verification, e.g. a tree deeper than the default MaxDepth or more
// elements than the default cap). The result document inherits them so a re-edit re-verifies the same.
func TestParseLimitsThreadedToVerify(t *testing.T) {
	elevated := Limits{MaxAllocBytes: 512 << 20, MaxDepth: 128, MaxElements: 500_000}
	doc, err := ParseFile(context.Background(), "testdata/sample.flac", WithLimits(elevated))
	if err != nil {
		t.Fatal(err)
	}
	if doc.limits != elevated {
		t.Errorf("parsed document limits = %+v, want %+v (the parse-time limits must be recorded)", doc.limits, elevated)
	}
	plan, err := doc.Edit().Set(tag.Title, "Verified").Prepare(WithVerifyEssence())
	if err != nil {
		t.Fatal(err)
	}
	resDoc, _, err := plan.Execute(context.Background(), SaveAsFile(filepath.Join(t.TempDir(), "out.flac")))
	if err != nil {
		t.Fatalf("verified write under elevated limits: %v", err)
	}
	if resDoc.limits != elevated {
		t.Errorf("result document limits = %+v, want %+v (inherited from the base for a re-edit)", resDoc.limits, elevated)
	}
}

// TestVerifyToleratesElementGrowthPastTightLimit covers the other half of the --verify limits fix: a
// document parsed under a tight MaxElements (enough for the input) can be edited to ADD elements past
// that cap and still verify. An edit can add ID3 frames the input lacked, and the writer's own size
// check gates against the library default (not the doc's tight cap), so the re-parse must floor
// MaxElements at the default too - otherwise a valid, grown rewrite is discarded as "did not parse
// back cleanly."
func TestVerifyToleratesElementGrowthPastTightLimit(t *testing.T) {
	// A tight element cap the tag-less MP3 input clears but a dozen-frame edit exceeds.
	doc, err := ParseFile(context.Background(), "testdata/notags.mp3", WithLimits(Limits{MaxElements: 8}))
	if err != nil {
		t.Fatalf("parse under a tight element cap: %v", err)
	}
	ed := doc.Edit()
	for i := 0; i < 12; i++ {
		ed.Set(tag.Key("CUSTOMFIELD"+string(rune('A'+i))), "value") // each custom key renders one TXXX frame
	}
	plan, err := ed.Prepare(WithVerifyEssence())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, _, err := plan.Execute(context.Background(), SaveAsFile(filepath.Join(t.TempDir(), "out.mp3"))); err != nil {
		t.Fatalf("verified write after element growth past the tight parse cap: %v", err)
	}
}

// mp4Box renders a minimal 8-byte-header MP4 atom for the structural-verify test.
func mp4Box(name string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	copy(b[4:8], name)
	copy(b[8:], payload)
	return b
}

// TestStructuralVerifyCatchesBadOutput exercises the exact composition the --verify structural
// re-parse uses inside verifyOutput: a sizedReaderAt over a real, still-open *os.File handed to the
// format's codec.Parse. A structurally-invalid rewrite (a truncated moov whose last child leaves a
// misaligning gap) must fail that parse, so a would-be-corrupt write aborts at writeAtomic's verify
// hook before the atomic commit. The essence hash alone cannot catch this - it re-reads the same
// verbatim media bytes - which is why this second check exists (Finding 1's --verify strengthening).
func TestStructuralVerifyCatchesBadOutput(t *testing.T) {
	// ftyp + moov(free child + 4-byte zero gap): well-formed boxes, but the gap after the moov's
	// last complete child is exactly the misalignment the parse guard rejects.
	bad := slices.Concat(
		mp4Box("ftyp", []byte("M4A \x00\x00\x00\x00")),
		mp4Box("moov", slices.Concat(mp4Box("free", nil), make([]byte, 4))),
	)
	path := filepath.Join(t.TempDir(), "bad.m4a")
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	codec, ok := core.ForFormat(core.FormatMP4)
	if !ok {
		t.Fatal("no MP4 codec registered")
	}
	sized := sizedReaderAt{ReaderAt: f, size: int64(len(bad))}
	if _, err := codec.Parse(context.Background(), sized, core.ParseOptions{}); err == nil {
		t.Error("structural re-parse accepted a truncated-moov output; --verify would ship the corruption")
	}
}
