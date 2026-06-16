package ogg

import (
	"context"
	"errors"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// nonAlignedOpus builds a deliberately non-standard Ogg Opus stream in which the
// first audio packet shares the OpusTags page (so the comment header does not end
// on a page boundary). No real encoder produces this, but the reader must still
// account for that page's audio. Returns the stream and the audio payload bytes.
func nonAlignedOpus(t *testing.T) ([]byte, []byte) {
	t.Helper()
	const serial = 0x1234
	// OpusHead: version 1, 2 channels, pre-skip 0, input rate 48000, gain 0, family 0.
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0, 0, 0x80, 0xBB, 0x00, 0x00, 0, 0, 0}
	// OpusTags: empty vendor, zero comments.
	tags := []byte{'O', 'p', 'u', 's', 'T', 'a', 'g', 's', 0, 0, 0, 0, 0, 0, 0, 0}
	audio := []byte("AUDIOPKT!!") // 10 opaque audio bytes

	page0 := buildPage(flagBOS, 0, serial, 0, []byte{byte(len(head))}, head)
	// One page carrying both OpusTags and the audio packet (two lacing values),
	// granule 960, so the comment header ends mid-page.
	body1 := append(append([]byte{}, tags...), audio...)
	page1 := buildPage(0, 960, serial, 1, []byte{byte(len(tags)), byte(len(audio))}, body1)
	return append(page0, page1...), audio
}

func TestParseNonAlignedAudioEssence(t *testing.T) {
	stream, audio := nonAlignedOpus(t)
	media, err := parse(context.Background(), core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if media.Format != core.FormatOggOpus {
		t.Fatalf("format = %v", media.Format)
	}
	d := media.Native.(*doc)
	if d.clean {
		t.Error("stream sharing the comment page with audio must not be marked clean")
	}

	// The shared page's audio body must appear in the essence ranges (the bug was
	// that it was dropped, yielding zero ranges and a wrong digest).
	ranges := media.EssenceRanges()
	if len(ranges) != 1 {
		t.Fatalf("essence ranges = %d, want 1", len(ranges))
	}
	lo, hi := ranges[0][0], ranges[0][1]
	if hi-lo != int64(len(audio)) || string(stream[lo:hi]) != string(audio) {
		t.Errorf("essence range = stream[%d:%d] = %q, want %q", lo, hi, stream[lo:hi], audio)
	}
	// And the final granule (960 samples at 48 kHz, minus zero pre-skip) gives a
	// non-zero duration rather than being lost with the page.
	if media.Properties.First().Duration <= 0 {
		t.Error("duration should be derived from the shared page's granule")
	}
}

func TestPlanRefusesNonAlignedWrite(t *testing.T) {
	stream, _ := nonAlignedOpus(t)
	media, err := parse(context.Background(), core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	edited := media.Clone()
	edited.Tags.Set(tag.Title, "nope") // a real change, so it is not a no-op

	if _, err := NewOpus().Plan(context.Background(), media, edited, core.DefaultWriteOptions()); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("non-page-aligned write should be refused with ErrInvalidData, got %v", err)
	}
}
