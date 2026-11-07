package aac

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/internal/tail"
)

// doc is the AAC native document: optional front ID3v2 tag (sole writable store),
// ADTS geometry, first-frame config for the essence digest, and the legacy tail.
// Satisfies [core.NativeDoc].
type doc struct {
	id3    *id3.Tag // front ID3v2 (nil if none)
	id3Len int64    // on-disk ID3v2 region length (0 if none)
	id3Off int64    // where it sits: past the empty or unreadable tags ahead of it (0 if none)
	// dups are the other ID3v2 tags in the chain, which a rewrite drops, and frontEnd where the
	// chain ends.
	dups     []id3.LeadingDup
	frontEnd int64

	// firstFrameOff is the first ADTS frame: where the tags end, or past a gap, which a rewrite keeps
	// after the new tag, out of the essence.
	firstFrameOff int64
	header        adtsHeader // first frame header (essence config)
	track         core.AudioTrack

	// layout is what follows the frames, which end at layout.Start; every tag in it is legacy.
	layout tail.Layout
}

func (d *doc) Format() core.Format { return core.FormatAAC }

// Clone deep-copies so Document accessors stay detached.
func (d *doc) Clone() core.NativeDoc {
	c := *d
	if d.id3 != nil {
		c.id3 = d.id3.Clone()
	}
	c.dups = slices.Clone(d.dups) // digests are never modified, so the copies share them
	c.layout = d.layout.Clone()
	return &c
}

// PaddingBytes is free padding inside the front ID3v2 region (0 if no front tag).
func (d *doc) PaddingBytes() int64 { return id3.FrontTagPadding(d.id3) }

// Describe summarizes native structure for dump/native views.
func (d *doc) Describe() []core.NativeEntry {
	before, after := id3.SplitDups(d.dups, d.id3Off)
	out := id3.DupEntries(before)
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
	out = append(out, id3.DupEntries(after)...)
	if gap := d.firstFrameOff - d.frontEnd; gap > 0 {
		out = append(out, core.LeadingBytesEntry(gap))
	}
	out = append(out, core.NativeEntry{
		Kind: "ADTS audio", Size: int(d.layout.Start - d.firstFrameOff),
		Note: d.track.Codec,
	})
	return append(out, d.layout.Entries("ADTS audio")...)
}
