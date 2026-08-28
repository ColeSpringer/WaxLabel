package musepack

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// doc is the Musepack native document: the decoded stream header, any leading ID3v2
// tag preserved verbatim, and the trailing containers whose start is the end of the
// audio. It is the preservation-first base for rewrites and satisfies core.NativeDoc.
type doc struct {
	leadingID3 []byte // stray ID3v2 before the stream marker, preserved
	streamAt   int64  // offset of the Musepack marker (== len(leadingID3))

	trailer ape.Trailer
	header  header
	track   core.AudioTrack
	size    int64
}

func (d *doc) Format() core.Format { return core.FormatMusepack }

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.leadingID3 = slices.Clone(d.leadingID3)
	c.trailer.Tag = d.trailer.Tag.Clone()
	c.trailer.ID3v1 = slices.Clone(d.trailer.ID3v1)
	return &c
}

// Describe summarizes the native structure for the dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	var out []core.NativeEntry
	if len(d.leadingID3) > 0 {
		out = append(out, core.NativeEntry{Kind: "ID3v2", Size: len(d.leadingID3), Note: "leading, legacy, preserved"})
	}
	kind := fmt.Sprintf("Musepack SV%d stream", d.header.streamVersion)
	if d.header.streamVersion >= 8 {
		kind = "Musepack SV8 packets"
	}
	return append(out, d.trailer.Describe(kind, d.track.Codec)...)
}
