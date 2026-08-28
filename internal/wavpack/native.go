// Package wavpack implements reading and writing WavPack (.wv) metadata for the
// public waxlabel package. The codec itself is internal.
//
// A WavPack file is a run of "wvpk" blocks followed by an optional APEv2 tag and,
// after that, an optional legacy ID3v1 tag. APEv2 is the native, authoritative tag
// store (internal/ape); ID3v1 is preserved but never authoritative, matching how MP3
// treats its own trailing containers. The audio blocks are copied verbatim on every
// write - only the tail is rewritten.
//
// Correction files (.wvc) are a separate stream carrying the hybrid-mode residual;
// they are out of scope, and editing a .wv leaves any companion .wvc untouched and
// still valid, since the audio bytes do not move.
//
// The codec is reimplemented from the public WavPack block-format documentation;
// reference implementations were consulted for design only.
package wavpack

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// doc is the WavPack native document: the decoded first-block configuration and the
// trailing containers (the APEv2 tag it writes and any legacy ID3v1 it preserves),
// whose start is the end of the audio. It is the preservation-first base for rewrites
// and satisfies core.NativeDoc.
type doc struct {
	trailer ape.Trailer

	header blockHeader // first block's decoded header, for properties and the essence config
	track  core.AudioTrack

	size int64
}

func (d *doc) Format() core.Format { return core.FormatWavPack }

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.trailer.Tag = d.trailer.Tag.Clone()
	c.trailer.ID3v1 = slices.Clone(d.trailer.ID3v1)
	return &c
}

// Describe summarizes the native structure for the dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	return d.trailer.Describe("WavPack blocks", d.track.Codec)
}
