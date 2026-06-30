package ogg

import (
	"context"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/vorbis"
)

// hasInheritedWarn reports whether any warning is an inherited-encoder stamp.
func hasInheritedWarn(ws []core.Warning) bool {
	for _, w := range ws {
		if w.Code == core.WarnInheritedEncoder {
			return true
		}
	}
	return false
}

// TestStripEncoderNeutralizesOpusVendor checks that --strip-encoder on a transcoder-stamped
// Opus vendor string is a real write and that the returned document's warnings match the
// neutralized vendor.
func TestStripEncoderNeutralizesOpusVendor(t *testing.T) {
	const serial = 0x4F4747
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0, 0, 0x80, 0xBB, 0, 0, 0, 0, 0}
	tags := opusTagsPacket("Lavf58.76.100", "TITLE=Song")
	audio := []byte("AUDIOPKT!!")
	page0 := buildPage(flagBOS, 0, serial, 0, []byte{byte(len(head))}, head)
	page1 := buildPage(0, 0, serial, 1, []byte{byte(len(tags))}, tags)
	page2 := buildPage(0, 960, serial, 2, []byte{byte(len(audio))}, audio)
	stream := append(append(append([]byte{}, page0...), page1...), page2...)

	base, err := parse(context.Background(), core.BytesSource(stream), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !hasInheritedWarn(base.Warnings) {
		t.Fatal("setup: a Lavf-vendor Opus should warn inherited-encoder")
	}

	opts := core.DefaultWriteOptions()
	opts.StripEncoderStamp = true
	plan, err := NewOpus().Plan(context.Background(), base, base.Clone(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NoOp {
		t.Fatal("--strip-encoder on a transcoded-but-otherwise-clean Opus must not be a no-op")
	}
	rd := plan.Result.Native.(*doc)
	if rd.vendor != vorbis.WaxLabelVendor {
		t.Errorf("result vendor = %q, want %q", rd.vendor, vorbis.WaxLabelVendor)
	}
	if hasInheritedWarn(plan.Result.Warnings) {
		t.Error("the result still carries an inherited-encoder warning")
	}
}
