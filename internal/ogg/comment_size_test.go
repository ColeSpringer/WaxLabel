package ogg

import (
	"context"
	"encoding/base64"
	"errors"
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

// TestPlanRejectsOversizedNewCover checks the O3 write-side cap: a NEWLY added cover whose
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
// that was read within the (possibly raised) parse limit must remain writable even when the
// write limit later sits below it - the file was readable going in, so an unrelated tag edit
// must not be refused going out. This is the Ogg analogue of MP4 checkBuiltItems' parsed-size
// floor; without it, a large-cover file parsed under WithLimits could never be edited.
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
	edited.Tags.Set(tag.Title, "Retitled") // an unrelated edit; the cover is untouched

	if _, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions()); err != nil {
		t.Fatalf("writing back a cover parsed under a raised limit must succeed (floored at the parsed cover): err=%v", err)
	}
}
