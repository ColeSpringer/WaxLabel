package apen

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// doc is the Monkey's Audio native document: the decoded header and the trailing
// containers (the APEv2 tag it writes and any legacy ID3v1 it preserves), whose start
// is the end of the audio. It is the preservation-first base for rewrites and
// satisfies core.NativeDoc.
type doc struct {
	trailer ape.Trailer
	header  header
	track   core.AudioTrack
	size    int64
}

func (d *doc) Format() core.Format { return core.FormatMonkeysAudio }

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.trailer.Tag = d.trailer.Tag.Clone()
	c.trailer.ID3v1 = slices.Clone(d.trailer.ID3v1)
	return &c
}

// Describe summarizes the native structure for the dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	return d.trailer.Describe("Monkey's Audio frames", d.track.Codec)
}
