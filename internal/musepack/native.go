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

	// chapters is the projection of the SV8 chapter packets, in start order, and
	// ctStart/ctEnd the extent of the run's packets that were read (equal when there
	// is none). The run sits inside the verbatim-copied stream, so the projection is
	// read-only.
	chapters []core.Chapter
	ctStart  int64
	ctEnd    int64
}

func (d *doc) Format() core.Format { return core.FormatMusepack }

// chapterStore reports whether the file has a chapter store the reader can project:
// the SV8 packet stream, with a known sample rate to place the chapters by. The parse
// and the capability answer from this one predicate.
func (d *doc) chapterStore() bool { return d.header.streamVersion >= 8 && d.header.sampleRate > 0 }

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	c.leadingID3 = slices.Clone(d.leadingID3)
	c.trailer.Tag = d.trailer.Tag.Clone()
	c.trailer.ID3v1 = slices.Clone(d.trailer.ID3v1)
	c.chapters = core.CloneChapters(d.chapters)
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
	entries := d.trailer.Describe(kind, d.track.Codec)
	if d.ctEnd > d.ctStart {
		// Indented under the stream entry, whose size already counts it, the way the
		// APEv2 entry's items are listed under the tag.
		entries = slices.Insert(entries, 1, core.NativeEntry{
			Kind: "  CT chapter packets", Size: int(d.ctEnd - d.ctStart), Note: fmt.Sprintf("%d chapters", len(d.chapters)),
		})
	}
	return append(out, entries...)
}
