// Package mp3: MP3 metadata for waxlabel. Internal. Layout: optional front ID3v2,
// MPEG frames, optional trailing APEv2 then ID3v1. ID3v2 is authoritative (internal/id3);
// legacy tags are family-viewed, preserved, warned.

package mp3

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
)

// doc: ID3v2 tag, audio geometry, first frame header, trailing legacy. Implements core.NativeDoc.

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

// Clone deep-copies so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	if d.id3 != nil {
		c.id3 = d.id3.Clone()
	}
	c.ape = slices.Clone(d.ape)
	c.id3v1 = slices.Clone(d.id3v1)
	return &c
}

// PaddingBytes is free space in the front ID3v2 region (0 if no front tag).

func (d *doc) PaddingBytes() int64 { return id3.FrontTagPadding(d.id3) }

// Describe summarizes native structure for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	var out []core.NativeEntry
	if d.id3 != nil {
		out = append(out, core.NativeEntry{
			Kind: fmt.Sprintf("ID3v2.%d", d.id3.SrcVersion()),
			Size: int(d.id3Len),
			Note: id3.FramesNote(d.id3),
		})
		for _, f := range d.id3.Frames() {
			out = append(out, core.NativeEntry{Kind: "  " + f.ID, Size: len(f.Body), Note: id3.FrameNote(f)})
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
