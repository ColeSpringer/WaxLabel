package musepack

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/tail"
)

// doc is the native document: stream header, optional leading ID3v2, the trailing tags.
// trailer.Start is the end of audio. Implements core.NativeDoc.
type doc struct {
	leadingID3 []byte // stray ID3v2 before the stream marker, preserved
	// leadingUnread is how many bytes of leadingID3 the parse could not read.
	leadingUnread int64
	streamAt      int64 // Musepack marker offset: past leadingID3 and any leading bytes kept with it

	trailer tail.Trailer
	header  header
	track   core.AudioTrack

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
	c.trailer = d.trailer.Clone()
	c.chapters = core.CloneChapters(d.chapters)
	return &c
}

// Describe summarizes native structure for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	kind := fmt.Sprintf("Musepack SV%d stream", d.header.streamVersion)
	if d.header.streamVersion >= 8 {
		kind = "Musepack SV8 packets"
	}
	entries := d.trailer.Describe(d.leadingID3, d.leadingUnread, d.streamAt, kind, d.track.Codec)
	if d.ctEnd > d.ctStart {
		// Nested under the stream entry (size already includes it), like APEv2 items.
		stream := slices.IndexFunc(entries, func(e core.NativeEntry) bool { return e.Kind == kind })
		entries = slices.Insert(entries, stream+1, core.NativeEntry{
			Kind: "  CT chapter packets", Size: int(d.ctEnd - d.ctStart), Note: fmt.Sprintf("%d chapters", len(d.chapters)),
		})
	}
	return entries
}
