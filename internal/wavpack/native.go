// Package wavpack implements WavPack (.wv) metadata for the public waxlabel
// package. The codec is internal.
//
// Layout: "wvpk" blocks, optional APEv2, optional trailing ID3v1. APEv2
// (internal/ape) is authoritative; ID3v1 is preserved only. Blocks are copied
// verbatim; only the tail is rewritten.
//
// Correction files (.wvc) are out of scope; editing .wv leaves a companion .wvc
// valid because audio bytes do not move.
//
// Reimplemented from the public WavPack block-format docs; reference code informed
// design only.
package wavpack

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// doc is the native document: first-block config plus trailing APEv2/ID3v1.
// Trailer.Start is the end of audio. Implements core.NativeDoc.
type doc struct {
	trailer ape.Trailer

	header blockHeader // first block; properties and essence config
	track  core.AudioTrack

	size int64
}

func (d *doc) Format() core.Format { return core.FormatWavPack }

// Clone deep-copies so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.trailer.Tag = d.trailer.Tag.Clone()
	c.trailer.ID3v1 = slices.Clone(d.trailer.ID3v1)
	return &c
}

// Describe summarizes native structure for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	return d.trailer.Describe("WavPack blocks", d.track.Codec)
}
