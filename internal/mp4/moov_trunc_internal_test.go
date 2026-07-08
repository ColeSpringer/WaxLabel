package mp4

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// mp4Ftyp / mp4Mdat build the minimal non-moov atoms a parseable MP4 needs around a
// crafted moov: a brand-carrying ftyp and a tiny media-data box.
func mp4Ftyp() []byte { return renderAtom(atomName("ftyp"), []byte("M4A \x00\x00\x00\x00M4A mp42")) }
func mp4Mdat() []byte { return renderAtom(atomName("mdat"), []byte("audiodata")) }

// inflateBoxSize rewrites a rendered atom's 32-bit size field to `declared`, so a test can build a
// top-level atom whose declared size overruns the bytes actually present - the clamp-to-EOF shape a
// truncated download produces. The atom must be the file's last, or the clamp would swallow what
// follows rather than stop at EOF.
func inflateBoxSize(box []byte, declared uint32) []byte {
	out := slices.Clone(box)
	binary.BigEndian.PutUint32(out[0:4], declared)
	return out
}

func hasWarn(ws []core.Warning, code core.WarningCode) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

// TestParseRejectsMoovTrailingGap covers a moov with no udta and a gap between where its
// last complete child ends and moov.end() would misalign a create-ilst edit - buildCreated appends
// the new udta at moov.end() (the no-udta/no-meta default branch), past the stray zeros walkAtoms
// tolerated (the udta-terminator rule), so the output re-parses misaligned. parse must reject it, the
// exact analogue of the meta-gap guard, whether the gap comes from a structural zero pad or a
// truncated-download clamp, and whether the moov is childless or has a tiling child before the gap.
func TestParseRejectsMoovTrailingGap(t *testing.T) {
	ctx := context.Background()
	freeChild := renderAtom(atomName("free"), nil) // 8 bytes, a complete child

	reject := map[string][]byte{
		// A complete child then a 4-byte all-zero gap (walkAtoms tolerates the zero tail).
		"structural gap after child": slices.Concat(mp4Ftyp(),
			renderAtom(atomName("moov"), slices.Concat(freeChild, make([]byte, 4))), mp4Mdat()),
		// No child at all: an 8-byte all-zero payload, so childEnd is the child-start position.
		"childless zero payload": slices.Concat(mp4Ftyp(),
			renderAtom(atomName("moov"), make([]byte, 8)), mp4Mdat()),
		// Truncated download: moov is last and declares far more than remains; the clamp to EOF
		// leaves a gap after its one complete child. This is the internal analogue of the
		// `head -c 9144 sample.m4a` repro that silently wrote a 2x-size, self-unreadable file.
		"truncated clamp leaves gap": slices.Concat(mp4Ftyp(),
			inflateBoxSize(renderAtom(atomName("moov"), slices.Concat(freeChild, make([]byte, 4))), 1<<20)),
	}
	for name, data := range reject {
		if _, err := parse(ctx, core.BytesSource(data), core.ParseOptions{}); !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("%s: parse err = %v, want ErrInvalidData", name, err)
		}
	}
}

// TestParseAcceptsMoovCleanTail is the must-not-reject half: the moov guard is scoped
// exactly to a udta-less moov that leaves a real gap, so it must not reject a moov whose child tiles
// exactly to its end, a moov padded with a legal trailing free atom, or a moov that zero-pads *around
// a present udta* (a muxer's alternative to a free atom) - all of which write correctly today. The
// last case is the load-bearing scoping proof: with a udta present the insert targets udta.end(), not
// moov.end(), so the moov-level gap is harmless and must be tolerated.
func TestParseAcceptsMoovCleanTail(t *testing.T) {
	ctx := context.Background()
	accept := map[string][]byte{
		"child tiles exactly": slices.Concat(mp4Ftyp(),
			renderAtom(atomName("moov"), renderAtom(atomName("free"), nil)), mp4Mdat()),
		"legal trailing free pad": slices.Concat(mp4Ftyp(),
			renderAtom(atomName("moov"), renderAtom(atomName("free"), make([]byte, 32))), mp4Mdat()),
		"present udta with moov-level zero gap": slices.Concat(mp4Ftyp(),
			renderAtom(atomName("moov"), slices.Concat(
				renderAtom(atomName("udta"), renderAtom(atomName("meta"), nil)), make([]byte, 4))), mp4Mdat()),
	}
	for name, data := range accept {
		if _, err := parse(ctx, core.BytesSource(data), core.ParseOptions{}); err != nil {
			t.Errorf("%s: parse err = %v, want nil", name, err)
		}
	}
}

// TestParseWarnsCleanTruncatedMoov covers the warn half: a moov clamped to EOF whose
// surviving children still tile exactly to the clamped end has no misaligning gap and is accepted,
// but the degraded structure must be surfaced with a truncation warning rather than reported clean.
func TestParseWarnsCleanTruncatedMoov(t *testing.T) {
	ctx := context.Background()
	// moov is last and declares far more than remains; its one child (a free atom) tiles exactly to
	// the clamped end, so there is no gap - only a truncation to report.
	moov := renderAtom(atomName("moov"), renderAtom(atomName("free"), nil)) // child tiles to end
	data := slices.Concat(mp4Ftyp(), inflateBoxSize(moov, 1<<20))
	media, err := parse(ctx, core.BytesSource(data), core.ParseOptions{})
	if err != nil {
		t.Fatalf("parse err = %v, want nil (a clean-tiling truncated moov is accepted)", err)
	}
	if !hasWarn(media.Warnings, core.WarnTruncatedAudio) {
		t.Errorf("warnings = %v, want a truncated warning for the moov atom", media.Warnings)
	}
}

// TestMoovLevelGapRoundTripsTagEdit is the round-trip counterpart to the scoping proof above: a moov
// that zero-pads around a present udta.meta still accepts a create-ilst tag edit (inserted at
// meta.end(), before the moov-level gap) and the written file re-parses with the tag present and no
// error - proving the tolerated moov gap does not become corruption on write.
func TestMoovLevelGapRoundTripsTagEdit(t *testing.T) {
	ctx := context.Background()
	udtaMeta := renderAtom(atomName("udta"), renderAtom(atomName("meta"), nil))
	moov := renderAtom(atomName("moov"), slices.Concat(udtaMeta, make([]byte, 4)))
	raw := slices.Concat(mp4Ftyp(), moov, mp4Mdat())

	base, err := parse(ctx, core.BytesSource(raw), core.ParseOptions{})
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	edited, err := parse(ctx, core.BytesSource(raw), core.ParseOptions{})
	if err != nil {
		t.Fatalf("parse edited: %v", err)
	}
	edited.Tags.Set(tag.Title, "Round Trip")

	plan, err := (Codec{}).Plan(ctx, base, edited, core.WriteOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var buf bytes.Buffer
	if _, err := bits.Write(ctx, &buf, core.BytesSource(raw), plan.Segments, nil); err != nil {
		t.Fatalf("render plan: %v", err)
	}
	reparsed, err := parse(ctx, core.BytesSource(buf.Bytes()), core.ParseOptions{})
	if err != nil {
		t.Fatalf("re-parse output: %v", err)
	}
	if v, ok := reparsed.Tags.Get(tag.Title); !ok || len(v) != 1 || v[0] != "Round Trip" {
		t.Errorf("re-parsed TITLE = %v (ok=%v), want [\"Round Trip\"]", v, ok)
	}
}
