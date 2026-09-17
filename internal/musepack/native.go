package musepack

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
)

// doc is the native document: stream header, optional leading ID3v2, trailing
// APEv2/ID3v1. Trailer.Start is the end of audio. Implements core.NativeDoc.
type doc struct {
	leadingID3 []byte // stray ID3v2 before the stream marker, preserved
	streamAt   int64  // Musepack marker offset (== len(leadingID3))

	trailer ape.Trailer
	header  header
	track   core.AudioTrack
	size    int64

	// chapters: SV8 CT projection in start order; ctStart/ctEnd bound the run
	// (equal when none). Inside the verbatim-copied stream: read-only.
	chapters []core.Chapter
	ctStart  int64
	ctEnd    int64
}

func (d *doc) Format() core.Format { return core.FormatMusepack }

// chapterStore: SV8 with a known sample rate. Shared by parse and capability.
func (d *doc) chapterStore() bool { return d.header.streamVersion >= 8 && d.header.sampleRate > 0 }

// Clone deep-copies so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.leadingID3 = slices.Clone(d.leadingID3)
	c.trailer.Tag = d.trailer.Tag.Clone()
	c.trailer.ID3v1 = slices.Clone(d.trailer.ID3v1)
	c.chapters = core.CloneChapters(d.chapters)
	return &c
}

// Describe summarizes native structure for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	var out []core.NativeEntry
	if len(d.leadingID3) > 0 {
		out = append(out, core.NativeEntry{Kind: "ID3v2", Size: len(d.leadingID3), Note: "leading, legacy, preserved"})
	}
	kind := fmt.Sprintf("Musepack SV%d stream", d.header.streamVersion)
	if d.header.streamVersion >= 8 {
		kind = "Musepack SV8 packets"
	}
	entries := d.trailer.Describe(kind, d.track.Codec)
	if d.ctEnd > d.ctStart {
		// Nested under the stream entry (size already includes it), like APEv2 items.
		entries = slices.Insert(entries, 1, core.NativeEntry{
			Kind: "  CT chapter packets", Size: int(d.ctEnd - d.ctStart), Note: fmt.Sprintf("%d chapters", len(d.chapters)),
		})
	}
	return append(out, entries...)
}
