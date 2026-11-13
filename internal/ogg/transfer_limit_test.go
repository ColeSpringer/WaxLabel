package ogg

import (
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// A transfer grades by the limits the writer holds a header packet to: a cover whose comment or
// a value past the allocation limit is dropped from the copy, and an Ogg FLAC picture past what
// a metadata block holds.
func TestCapabilitiesStateThePacketLimit(t *testing.T) {
	cover := core.Picture{Type: core.PicFrontCover, MIME: "image/png", Data: append(testfile.PNG(), make([]byte, 900)...)}
	src := &core.Media{Tags: tag.NewTagSet(), Pictures: []core.Picture{cover}}
	src.Tags.Set(tag.Title, "T")
	src.Tags.Set(tag.Comment, strings.Repeat("x", 2000))
	reasons := func(caps core.Capabilities) map[core.TransferKind]string {
		got := map[core.TransferKind]string{}
		for _, it := range core.ProjectTransferOnto(src, nil, caps).Items {
			if it.Disposition == core.Dropped {
				got[it.Kind] = it.Reason
			}
		}
		return got
	}

	// The cover's bytes fit the limit; its base64 comment does not.
	small := core.WriteOptions{Limits: bits.Limits{MaxAllocBytes: 1024}}
	for _, c := range []Codec{NewVorbis(), NewOpus()} {
		got := reasons(c.Capabilities(nil, small))
		if want := "a value is 2.0 KiB, over the 1.0 KiB an Ogg header packet holds under the allocation limit"; got[core.TransferField] != want {
			t.Errorf("%s value: %q, want %q", c.format, got[core.TransferField], want)
		}
		if want := "the picture is 1.3 KiB, over the 1.0 KiB an Ogg header packet holds under the allocation limit"; got[core.TransferPicture] != want {
			t.Errorf("%s picture: %q, want %q", c.format, got[core.TransferPicture], want)
		}
		if got := reasons(c.Capabilities(nil, core.WriteOptions{})); len(got) != 0 {
			t.Errorf("%s under the default limit drops %v", c.format, got)
		}
		// The limit itself fits; one byte more does not.
		atLimit := &core.Media{Tags: tag.NewTagSet()}
		atLimit.Tags.Set(tag.Comment, strings.Repeat("x", 1024))
		atLimit.Tags.Set(tag.Lyrics, strings.Repeat("x", 1025))
		items := core.ProjectTransferOnto(atLimit, nil, c.Capabilities(nil, small)).Items
		if len(items) != 2 || items[0].Disposition != core.Carried || items[1].Disposition != core.Dropped {
			t.Errorf("%s at and one past the limit: %+v", c.format, items)
		}
	}

	// An Ogg FLAC picture is a block of its own, so the block limit stands under any allocation limit.
	src.Pictures[0].Data = append(testfile.PNG(), make([]byte, 17<<20)...)
	got := reasons(NewFLAC().Capabilities(nil, core.WriteOptions{}))
	if want := "the picture is 17.0 MiB, over the 16.0 MiB a FLAC metadata block holds"; got[core.TransferPicture] != want {
		t.Errorf("Ogg FLAC picture: %q, want %q", got[core.TransferPicture], want)
	}
	if _, dropped := got[core.TransferField]; dropped {
		t.Errorf("Ogg FLAC drops a 2 KiB value: %v", got)
	}
	// Under an allocation limit below the block's, the packet limit is the one that binds.
	src.Pictures[0].Data = append(testfile.PNG(), make([]byte, 2000)...)
	got = reasons(NewFLAC().Capabilities(nil, small))
	if want := "a value is 2.0 KiB, over the 1.0 KiB an Ogg header packet holds under the allocation limit"; got[core.TransferField] != want {
		t.Errorf("Ogg FLAC value under a small allocation limit: %q, want %q", got[core.TransferField], want)
	}
	if want := "the picture is 2.0 KiB, over the 1.0 KiB an Ogg header packet holds under the allocation limit"; got[core.TransferPicture] != want {
		t.Errorf("Ogg FLAC picture under a small allocation limit: %q, want %q", got[core.TransferPicture], want)
	}
}
