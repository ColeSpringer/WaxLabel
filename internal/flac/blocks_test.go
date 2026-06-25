package flac

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

func TestParseStreamInfo(t *testing.T) {
	body := make([]byte, streamInfoLen)
	// min/max block size = 4096.
	body[0], body[1] = 0x10, 0x00
	body[2], body[3] = 0x10, 0x00
	// min/max frame size left zero (bytes 4..9).
	// Sample rate 44100 (0x0AC44) in 20 bits, channels=2, bps=16, samples=1000.
	body[10] = 0x0A
	body[11] = 0xC4
	body[12] = 0x40 | (1 << 1)                                      // rate low nibble | (channels-1)<<1 | (bps-1)>>4 (high bit of bps-1 = 0)
	body[13] = (15 << 4)                                            // (bps-1)&0xf << 4, then high 4 bits of sample count = 0
	body[14], body[15], body[16], body[17] = 0x00, 0x00, 0x03, 0xE8 // 1000

	tr, err := parseStreamInfo(body)
	if err != nil {
		t.Fatal(err)
	}
	if tr.SampleRate != 44100 {
		t.Errorf("SampleRate = %d, want 44100", tr.SampleRate)
	}
	if tr.Channels != 2 {
		t.Errorf("Channels = %d, want 2", tr.Channels)
	}
	if tr.BitsPerSample != 16 {
		t.Errorf("BitsPerSample = %d, want 16", tr.BitsPerSample)
	}
	if tr.TotalSamples != 1000 {
		t.Errorf("TotalSamples = %d, want 1000", tr.TotalSamples)
	}
	if tr.MinBlockSize != 4096 || tr.MaxBlockSize != 4096 {
		t.Errorf("block sizes = %d/%d, want 4096/4096", tr.MinBlockSize, tr.MaxBlockSize)
	}
}

func TestParseStreamInfoDurationOverflowGuarded(t *testing.T) {
	// SampleRate 1 with a near-2^36 total-sample count would overflow the int64
	// nanoseconds of time.Duration; the guard leaves Duration at 0 instead of
	// producing garbage.
	body := make([]byte, streamInfoLen)
	body[0], body[1], body[2], body[3] = 0x10, 0x00, 0x10, 0x00
	// Sample rate = 1: bytes 10,11 zero; byte 12 high nibble carries rate&0xF.
	body[12] = 0x10 // rate low nibble = 1, channels-1 = 0
	// Total samples: set the top of the 36-bit field high.
	body[13] = 0x0F // low nibble = high 4 bits of sample count
	body[14], body[15], body[16], body[17] = 0xFF, 0xFF, 0xFF, 0xFF

	tr, err := parseStreamInfo(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.SampleRate != 1 {
		t.Fatalf("SampleRate = %d, want 1", tr.SampleRate)
	}
	if tr.Duration < 0 {
		t.Errorf("Duration = %v, want a non-negative (clamped) value", tr.Duration)
	}
}

func TestParseStreamInfoRejectsZeroRate(t *testing.T) {
	body := make([]byte, streamInfoLen)
	if _, err := parseStreamInfo(body); err == nil {
		t.Error("expected error for zero sample rate")
	}
	if _, err := parseStreamInfo(body[:10]); err == nil {
		t.Error("expected error for short STREAMINFO")
	}
}

func TestVorbisCommentRoundTrip(t *testing.T) {
	comments := []comment{
		{"TITLE", "Hello"},
		{"ARTIST", "World"},
		{"ARTIST", "Second"}, // multi-value preserved in order
		{"DESCRIPTION", "has = equals = signs"},
	}
	body := renderVorbisComment("WaxLabel/0.1", comments)
	vendor, got, err := parseVorbisComment(body, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if vendor != "WaxLabel/0.1" {
		t.Errorf("vendor = %q", vendor)
	}
	if !slices.Equal(got, comments) {
		t.Errorf("comments = %v, want %v", got, comments)
	}
}

func TestPictureRoundTrip(t *testing.T) {
	in := core.Picture{
		Type: core.PicFrontCover, MIME: "image/png", Description: "cover",
		Width: 640, Height: 480, Depth: 24, Colors: 0, Data: []byte{1, 2, 3, 4, 5},
	}
	body := renderPicture(in)
	got, err := parsePictureBlock(body, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != in.Type || got.MIME != in.MIME || got.Description != in.Description ||
		got.Width != in.Width || got.Height != in.Height || got.Depth != in.Depth ||
		got.Colors != in.Colors || !slices.Equal(got.Data, in.Data) {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, in)
	}
}

func TestRenderBlockHeader(t *testing.T) {
	b := renderBlock(blkVorbisComment, true, []byte{0xAA, 0xBB})
	if b[0] != 0x80|blkVorbisComment {
		t.Errorf("header[0] = 0x%02x, want last-flag|type", b[0])
	}
	if b[1] != 0 || b[2] != 0 || b[3] != 2 {
		t.Errorf("length bytes = %v, want [0 0 2]", b[1:4])
	}
	if !slices.Equal(b[4:], []byte{0xAA, 0xBB}) {
		t.Errorf("body = %v", b[4:])
	}

	// Not last: high bit clear.
	nb := renderBlock(blkPadding, false, nil)
	if nb[0] != blkPadding {
		t.Errorf("non-last header[0] = 0x%02x, want 0x%02x", nb[0], blkPadding)
	}
}

func TestParseVorbisCommentSkipsEntriesWithoutEquals(t *testing.T) {
	// An entry lacking '=' is dropped from the projection.
	comments := []comment{{"TITLE", "ok"}}
	body := renderVorbisComment("v", comments)
	// Append a malformed entry manually.
	bad := []byte("noequalshere")
	body = append(body, byte(len(bad)), 0, 0, 0)
	body = append(body, bad...)
	// Bump the comment count from 1 to 2.
	// vendor len (4) + vendor (1) => count at offset 5.
	body[5] = 2
	_, got, err := parseVorbisComment(body, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].name != "TITLE" {
		t.Errorf("got %v, want only the valid TITLE entry", got)
	}
}
