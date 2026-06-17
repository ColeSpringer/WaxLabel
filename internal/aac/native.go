package aac

import (
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// doc is the AAC native document: the optional front ID3v2 tag (the sole,
// authoritative, writable container) plus the ADTS essence geometry and the
// first frame's decoded configuration (decoder-critical, for the essence
// digest). It is the preservation-first base for rewrites and satisfies
// core.NativeDoc.
type doc struct {
	id3    *id3.Tag // parsed front ID3v2 tag (nil if the file has none)
	id3Len int64    // on-disk length of the original ID3v2 region (0 if none)

	audioStart int64      // first ADTS byte (== id3Len)
	audioEnd   int64      // end of the ADTS stream (EOF)
	header     adtsHeader // first frame's decoded header, for the essence config
	track      core.AudioTrack

	size int64
}

func (d *doc) Format() core.Format { return core.FormatAAC }

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	if d.id3 != nil {
		c.id3 = d.id3.Clone()
	}
	return &c
}

// Describe summarizes the native structure for the dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	var out []core.NativeEntry
	if d.id3 != nil {
		out = append(out, core.NativeEntry{
			Kind: fmt.Sprintf("ID3v2.%d", d.id3.SrcVersion()),
			Size: int(d.id3Len),
			Note: fmt.Sprintf("%d frames", len(d.id3.Frames())),
		})
		for _, f := range d.id3.Frames() {
			note := ""
			if f.Opaque {
				note = "preserved (opaque)"
			}
			out = append(out, core.NativeEntry{Kind: "  " + f.ID, Size: len(f.Body), Note: note})
		}
	}
	out = append(out, core.NativeEntry{
		Kind: "ADTS audio", Size: int(d.audioEnd - d.audioStart),
		Note: d.track.Codec,
	})
	return out
}
