// Package mp3 implements reading and writing MP3 (MPEG audio) metadata for the
// public waxlabel package. The codec itself is internal. An MP3 file is an
// optional front ID3v2 tag, the MPEG audio frames, and optional trailing legacy
// containers (APEv2, then a 128-byte ID3v1). The ID3v2 tag is the authoritative,
// writable container (decoded by internal/id3); the trailing legacy tags are
// surfaced in the family view, preserved verbatim, and warned. The codec is
// reimplemented from the MPEG-1/2 audio and ID3 specifications; reference
// implementations were consulted for design only.
package mp3

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// doc is the MP3 native document: the parsed ID3v2 tag, the audio geometry and
// first MPEG frame header (decoder-critical config), and any preserved trailing
// legacy containers. It is the preservation-first base for rewrites and
// satisfies core.NativeDoc.
type doc struct {
	id3    *id3.Tag // parsed ID3v2 tag (nil if the file has none)
	id3Len int64    // on-disk length of the original ID3v2 region (0 if none)

	audioStart  int64
	audioEnd    int64
	firstHeader [4]byte // first MPEG frame header, for the essence config
	track       core.AudioTrack

	ape       []byte // preserved APEv2 region (nil if absent)
	apeOffset int64
	apeTag    *ape.Tag // parsed APEv2 items, for the family view (read-only after parse)
	id3v1     []byte   // preserved 128-byte ID3v1 trailer (nil if absent)

	size int64
}

func (d *doc) Format() core.Format { return core.FormatMP3 }

// Clone deep-copies the document so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	if d.id3 != nil {
		c.id3 = d.id3.Clone()
	}
	c.ape = slices.Clone(d.ape)
	c.id3v1 = slices.Clone(d.id3v1)
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
		Kind: "MPEG audio", Size: int(d.audioEnd - d.audioStart),
		Note: d.track.Codec,
	})
	if len(d.ape) > 0 {
		out = append(out, core.NativeEntry{Kind: "APEv2 (legacy)", Size: len(d.ape), Note: "preserved"})
	}
	if len(d.id3v1) > 0 {
		out = append(out, core.NativeEntry{Kind: "ID3v1 (trailing)", Size: len(d.id3v1), Note: "preserved"})
	}
	return out
}
