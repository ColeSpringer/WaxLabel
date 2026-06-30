package ogg

import (
	"context"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
	"github.com/colespringer/waxlabel/tag"
)

// opusTagsPacket builds an OpusTags comment header: the "OpusTags" magic, a vendor string,
// then each "NAME=VALUE" comment, every length prefixed little-endian (the Vorbis comment
// layout Opus reuses).
func opusTagsPacket(vendor string, comments ...string) []byte {
	le32 := func(n int) []byte { return []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)} }
	b := append([]byte("OpusTags"), le32(len(vendor))...)
	b = append(b, vendor...)
	b = append(b, le32(len(comments))...)
	for _, c := range comments {
		b = append(b, le32(len(c))...)
		b = append(b, c...)
	}
	return b
}

// TestParseHidesMalformedPictureComment checks that a malformed METADATA_BLOCK_PICTURE
// comment is retained for round-trip without surfacing as a custom tag.
func TestParseHidesMalformedPictureComment(t *testing.T) {
	const serial = 0x4F4747
	// OpusHead: version 1, 2 channels, pre-skip 0, input rate 48000, gain 0, family 0.
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0, 0, 0x80, 0xBB, 0, 0, 0, 0, 0}
	tags := opusTagsPacket("vendor", "TITLE=Keep", "METADATA_BLOCK_PICTURE=not~valid~base64")
	audio := []byte("AUDIOPKT!!")

	page0 := buildPage(flagBOS, 0, serial, 0, []byte{byte(len(head))}, head)
	page1 := buildPage(0, 0, serial, 1, []byte{byte(len(tags))}, tags) // comment header ends the page
	page2 := buildPage(0, 960, serial, 2, []byte{byte(len(audio))}, audio)
	stream := append(append(append([]byte{}, page0...), page1...), page2...)

	media, err := parse(context.Background(), core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	// The corrupt picture must not leak as a tag key (dump --json would show it otherwise).
	for _, k := range media.Tags.Keys() {
		if vorbis.IsPictureComment(string(k)) {
			t.Errorf("malformed METADATA_BLOCK_PICTURE leaked as a tag key %q", k)
		}
	}
	if v, _ := media.Tags.First(tag.Title); v != "Keep" {
		t.Errorf("TITLE = %q, want Keep (a real tag still projects)", v)
	}
	// The opaque comment is retained for round-trip (preserved verbatim on a later write).
	d := media.Native.(*doc)
	found := false
	for _, cm := range d.comments {
		if vorbis.IsPictureComment(cm.Name) {
			found = true
		}
	}
	if !found {
		t.Error("malformed picture comment was dropped; it must be preserved opaque for round-trip")
	}
}
