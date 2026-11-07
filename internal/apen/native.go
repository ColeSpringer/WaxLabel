package apen

import (
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/tail"
)

// doc is the native document: decoded header, optional leading ID3v2, the trailing tags.
// trailer.Start is the end of audio. Implements core.NativeDoc.
type doc struct {
	leadingID3 []byte // stray ID3v2 before the MAC marker, preserved
	streamAt   int64  // MAC marker offset: past leadingID3 and any leading bytes kept with it

	trailer tail.Trailer
	header  header
	track   core.AudioTrack
}

func (d *doc) Format() core.Format { return core.FormatMonkeysAudio }

// Clone deep-copies so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.leadingID3 = slices.Clone(d.leadingID3)
	c.trailer = d.trailer.Clone()
	return &c
}

// Describe summarizes native structure for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	return d.trailer.Describe(d.leadingID3, d.streamAt, "Monkey's Audio frames", d.track.Codec)
}
