package ogg

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// parseOpusStreamWith builds and parses a minimal three-page Opus stream with no output
// gain. See [parseOpusStreamWithGain] for the layout.
func parseOpusStreamWith(t *testing.T, comments ...string) *core.Media {
	t.Helper()
	return parseOpusStreamWithGain(t, 0, comments...)
}

// smallCover is a tiny front cover whose base64 METADATA_BLOCK_PICTURE footprint
// (PictureCommentLen) is a known, small number of bytes, so a lowered limit can sit below it.
func smallCover() core.Picture {
	return core.Picture{Type: core.PicFrontCover, MIME: "image/png", Data: make([]byte, 60)}
}

// pictureComment renders p as the METADATA_BLOCK_PICTURE comment string parse decodes.
func pictureComment(p core.Picture) string {
	return "METADATA_BLOCK_PICTURE=" + base64.StdEncoding.EncodeToString(vorbis.RenderPicture(p))
}

// TestPlanRejectsOversizedNewCover checks that a new cover whose base64 footprint exceeds
// the write limit is refused with ErrPictureTooLarge (as MP4/FLAC/ID3 covers are), since
// --verify's output-sized re-parse cannot catch a file a default-limit reader rejects.
func TestPlanRejectsOversizedNewCover(t *testing.T) {
	base := parseOpusStreamWith(t) // no pictures in the source
	cover := smallCover()

	// The lowered limit sits below the cover's footprint but above the cover-less
	// source packet. Mutating bits.DefaultLimits is safe: the Ogg tests run sequentially
	// in their own process; the defer restores it.
	old := bits.DefaultLimits.MaxAllocBytes
	defer func() { bits.DefaultLimits.MaxAllocBytes = old }()

	edited := base.Clone()
	edited.Pictures = []core.Picture{cover}

	bits.DefaultLimits.MaxAllocBytes = vorbis.PictureCommentLen(cover) - 1
	if _, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions()); !errors.Is(err, waxerr.ErrPictureTooLarge) {
		t.Fatalf("Plan with an oversized new cover: err=%v, want ErrPictureTooLarge", err)
	}

	// Control: with the ceiling restored, the same cover writes cleanly.
	bits.DefaultLimits.MaxAllocBytes = old
	if _, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions()); err != nil {
		t.Fatalf("Plan with an in-bounds cover: err=%v, want success", err)
	}
}

// TestPlanWritesBackCoverParsedUnderRaisedLimit checks the cap's floor: a cover read
// within a raised parse limit stays writable under a lower write limit, since the floor
// is the whole original comment packet (origCommentPacketLen) and a same-length edit
// stays within it. This is the Ogg analogue of MP4 checkBuiltItems' parsed-size floor.
func TestPlanWritesBackCoverParsedUnderRaisedLimit(t *testing.T) {
	cover := smallCover()
	base := parseOpusStreamWith(t, pictureComment(cover))
	if len(base.Pictures) != 1 {
		t.Fatalf("setup: parsed %d pictures, want 1 (the embedded cover)", len(base.Pictures))
	}

	old := bits.DefaultLimits.MaxAllocBytes
	defer func() { bits.DefaultLimits.MaxAllocBytes = old }()
	bits.DefaultLimits.MaxAllocBytes = vorbis.PictureCommentLen(cover) - 1 // below the parsed cover

	edited := base.Clone()
	edited.Tags.Set(tag.Title, "Tune") // an unrelated, same-length edit ("Song" -> "Tune"); packet size unchanged

	if _, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions()); err != nil {
		t.Fatalf("a same-length edit staying within the whole-packet floor must succeed: err=%v", err)
	}
}

// TestPlanRejectsCoversJointlyExceedingLimit pins the whole-packet cap: two covers that
// each fit under the limit but whose comment packet jointly exceeds it are rejected,
// since reassembleHeaders would refuse to re-parse the file at the same limit. A
// one-cover control at the same limit succeeds.
func TestPlanRejectsCoversJointlyExceedingLimit(t *testing.T) {
	cover := smallCover()
	// Measure the cover-less and one-cover comment packet sizes so the limit can sit strictly
	// between the one-cover packet (must fit) and the two-cover packet (must be rejected).
	p0 := parseOpusStreamWith(t).Native.(*doc).origCommentPacketLen                        // base, no cover
	p1 := parseOpusStreamWith(t, pictureComment(cover)).Native.(*doc).origCommentPacketLen // base + 1 cover
	p2 := 2*p1 - p0                                                                        // base + 2 covers
	limit := (p1 + p2) / 2                                                                 // p1 < limit < p2

	// Each cover's base64 footprint is below the limit, isolating the whole-packet check.
	if vorbis.PictureCommentLen(cover) >= limit {
		t.Fatalf("setup: single-cover footprint %d must be below the limit %d to isolate the whole-packet check",
			vorbis.PictureCommentLen(cover), limit)
	}

	old := bits.DefaultLimits.MaxAllocBytes
	defer func() { bits.DefaultLimits.MaxAllocBytes = old }()
	bits.DefaultLimits.MaxAllocBytes = limit

	base := parseOpusStreamWith(t) // cover-less source; the covers are added by the edit
	twoCovers := base.Clone()
	twoCovers.Pictures = []core.Picture{cover, cover}
	if _, err := NewOpus().Plan(context.Background(), base, twoCovers, core.DefaultWriteOptions()); !errors.Is(err, waxerr.ErrPictureTooLarge) {
		t.Fatalf("two covers jointly exceeding the limit: err=%v, want ErrPictureTooLarge", err)
	}

	// Control: one cover at the same limit still writes (its packet fits below the limit).
	oneCover := base.Clone()
	oneCover.Pictures = []core.Picture{cover}
	if _, err := NewOpus().Plan(context.Background(), base, oneCover, core.DefaultWriteOptions()); err != nil {
		t.Fatalf("one cover within the limit must still succeed: err=%v", err)
	}
}

// TestPlanRejectsAdditiveEditPastFloor pins the edge: on a file whose comment packet
// already sits above the write limit (a raised WithLimits parse, then a lower-limit
// write), an edit that grows the packet past the whole-packet floor is rejected with a
// limit hint.
func TestPlanRejectsAdditiveEditPastFloor(t *testing.T) {
	cover := smallCover()
	base := parseOpusStreamWith(t, pictureComment(cover)) // parsed at the default (high) limit
	p1 := base.Native.(*doc).origCommentPacketLen

	old := bits.DefaultLimits.MaxAllocBytes
	defer func() { bits.DefaultLimits.MaxAllocBytes = old }()
	bits.DefaultLimits.MaxAllocBytes = p1 - 10 // write limit below the parsed packet; the floor raises it back to p1

	edited := base.Clone()
	edited.Tags.Set(tag.Title, "Retitled") // "Song" -> "Retitled" grows the packet past the p1 floor

	_, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if !errors.Is(err, waxerr.ErrPictureTooLarge) {
		t.Fatalf("an additive edit past the floor must be rejected: err=%v, want ErrPictureTooLarge", err)
	}
	if !strings.Contains(err.Error(), "raise the write allocation limit") {
		t.Errorf("rejection should hint at raising the limit; got %q", err.Error())
	}
}
