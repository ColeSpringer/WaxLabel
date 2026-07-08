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

// parseOpusStreamWith builds and parses a minimal three-page Opus stream (OpusHead, an
// OpusTags comment packet carrying "TITLE=Song" plus the given extra comments, then one audio
// page). The comment packet must stay under 255 bytes so it fits a single page-lacing segment.
func parseOpusStreamWith(t *testing.T, comments ...string) *core.Media {
	t.Helper()
	const serial = 0x4F4747
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0, 0, 0x80, 0xBB, 0, 0, 0, 0, 0}
	tags := opusTagsPacket("libopus 1.4", append([]string{"TITLE=Song"}, comments...)...)
	if len(tags) > 255 {
		t.Fatalf("comment packet is %d bytes; keep test covers small enough for single-segment lacing", len(tags))
	}
	audio := []byte("AUDIOPKT!!")
	page0 := buildPage(flagBOS, 0, serial, 0, []byte{byte(len(head))}, head)
	page1 := buildPage(0, 0, serial, 1, []byte{byte(len(tags))}, tags)
	page2 := buildPage(0, 960, serial, 2, []byte{byte(len(audio))}, audio)
	stream := append(append(append([]byte{}, page0...), page1...), page2...)
	base, err := parse(context.Background(), core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return base
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

// TestPlanRejectsOversizedNewCover checks the write-side cap: a newly added cover whose
// base64 footprint exceeds the write limit is refused with ErrPictureTooLarge (the same error
// the sibling MP4/FLAC/ID3 covers return), rather than emitting a file a default-limit reader
// would then choke on - a failure --verify's output-sized structural re-parse cannot catch.
func TestPlanRejectsOversizedNewCover(t *testing.T) {
	base := parseOpusStreamWith(t) // no pictures in the source
	cover := smallCover()

	// The lowered limit sits below the cover's footprint but above the source's (cover-less)
	// comment packet. bits.DefaultLimits gates the check (mutation is safe: the Ogg tests run
	// sequentially and each package's test binary is a separate process); the defer restores it.
	old := bits.DefaultLimits.MaxAllocBytes
	defer func() { bits.DefaultLimits.MaxAllocBytes = old }()

	edited := base.Clone()
	edited.Pictures = []core.Picture{cover}

	bits.DefaultLimits.MaxAllocBytes = vorbis.PictureCommentLen(cover) - 1
	if _, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions()); !errors.Is(err, waxerr.ErrPictureTooLarge) {
		t.Fatalf("Plan with an oversized new cover: err=%v, want ErrPictureTooLarge", err)
	}

	// Control: with the real ceiling restored, the same cover writes cleanly - the cap is a
	// genuine size limit, not a blanket refusal of covers.
	bits.DefaultLimits.MaxAllocBytes = old
	if _, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions()); err != nil {
		t.Fatalf("Plan with an in-bounds cover: err=%v, want success", err)
	}
}

// TestPlanWritesBackCoverParsedUnderRaisedLimit is the regression for the cap's floor: a cover
// that was read within the (possibly raised) parse limit must remain writable even when the write
// limit later sits below it - the file was readable going in, so an unrelated edit that does not
// grow the comment packet past what the file already held must not be refused going out. The floor
// is now the whole original comment packet (origCommentPacketLen), so a same-length edit stays
// within it. This is the Ogg analogue of MP4 checkBuiltItems' parsed-size floor; without it, a
// large-cover file parsed under WithLimits could never be edited.
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

// TestPlanRejectsCoversJointlyExceedingLimit pins the whole-packet cap: two covers that each fit
// under the limit individually (so the old per-cover check passed both) but whose comment packet
// jointly exceeds it are now rejected, rather than writing a file reassembleHeaders would refuse to
// re-parse at the same limit. A one-cover control at the same limit still succeeds.
func TestPlanRejectsCoversJointlyExceedingLimit(t *testing.T) {
	cover := smallCover()
	// Measure the cover-less and one-cover comment packet sizes so the limit can sit strictly
	// between the one-cover packet (must fit) and the two-cover packet (must be rejected).
	p0 := parseOpusStreamWith(t).Native.(*doc).origCommentPacketLen                        // base, no cover
	p1 := parseOpusStreamWith(t, pictureComment(cover)).Native.(*doc).origCommentPacketLen // base + 1 cover
	p2 := 2*p1 - p0                                                                        // base + 2 covers
	limit := (p1 + p2) / 2                                                                 // p1 < limit < p2

	// Each cover individually clears the old per-cover check (its base64 footprint is below the
	// limit), so this is exactly the case the old code let through into an unparseable file.
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

// TestPlanRejectsAdditiveEditPastFloor pins the deliberately locked-in edge: on a file whose
// comment packet already sits above the write limit (only reachable via a raised WithLimits parse
// then a lower-limit write), an additive edit that grows the packet past the whole-packet floor is
// now rejected with a limit hint - where the old per-cover check silently wrote a file a re-parse
// at the same limit would refuse.
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
