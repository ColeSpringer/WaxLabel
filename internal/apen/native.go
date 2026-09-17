package apen

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// doc is the native document: decoded header plus trailing APEv2/ID3v1.
// Trailer.Start is the end of audio. Implements core.NativeDoc.
type doc struct {
	trailer ape.Trailer
	header  header
	track   core.AudioTrack
	size    int64
}

func (d *doc) Format() core.Format { return core.FormatMonkeysAudio }

// Clone deep-copies so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.trailer.Tag = d.trailer.Tag.Clone()
	c.trailer.ID3v1 = slices.Clone(d.trailer.ID3v1)
	return &c
}

// Describe summarizes native structure for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	return d.trailer.Describe("Monkey's Audio frames", d.track.Codec)
}
