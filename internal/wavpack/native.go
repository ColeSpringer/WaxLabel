// Package wavpack implements WavPack (.wv) metadata for the public waxlabel
// package. The codec is internal.
//
// Layout: optional leading ID3v2, "wvpk" blocks, then the tail internal/tail reads: APE tags,
// Lyrics3 blocks, ID3v1 tags and junk. The APE tag nearest the end of the file (internal/ape) is
// authoritative; the ID3 containers and Lyrics3 blocks are preserved as legacy only. Blocks are
// copied verbatim; only the tags around them are rewritten.
//
// Correction files (.wvc) are out of scope; editing .wv leaves a companion .wvc
// valid because the blocks are unchanged.
//
// Reimplemented from the public WavPack block-format docs; reference code informed
// design only.
package wavpack

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/tail"
)

// doc is the native document: first-block config, optional leading ID3v2, the trailing tags.
// trailer.Start is the end of audio. Implements core.NativeDoc.
type doc struct {
	leadingID3 []byte // stray ID3v2 before the first block, preserved
	// leadingUnread is how many bytes of leadingID3 the parse could not read.
	leadingUnread int64
	streamAt      int64 // first block offset: past leadingID3 and any leading bytes kept with it

	trailer tail.Trailer

	header blockHeader // first block; properties and essence config
	track  core.AudioTrack
}

func (d *doc) Format() core.Format { return core.FormatWavPack }

// Clone deep-copies so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.leadingID3 = slices.Clone(d.leadingID3)
	c.trailer = d.trailer.Clone()
	return &c
}

// Describe summarizes native structure for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	return d.trailer.Describe(d.leadingID3, d.leadingUnread, d.streamAt, "WavPack blocks", d.track.Codec)
}
