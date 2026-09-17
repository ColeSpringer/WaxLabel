package ape

import (
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Shared trailing store for WavPack, Monkey's Audio, and Musepack: APEv2 after
// audio, optional ID3v1. Audio is opaque and copied verbatim.

// Trailer: APEv2 (authoritative), optional ID3v1, Start (= end of audio).
type Trailer struct {
	Tag    *Tag
	TagLen int64
	ID3v1  []byte
	Start  int64
}

// PeelTrailer walks end-first (ID3v1 after APEv2).
// minStart: earliest tag offset (end of first audio unit). Without it, a crafted
// footer can swallow the header and rewrite emits unparseable bytes.
func PeelTrailer(src core.ReaderAtSized, size, minStart, limit int64, maxElements int) (Trailer, []core.Warning) {
	var t Trailer
	var warnings []core.Warning
	end := size
	if size-128 >= minStart {
		if tail, err := bits.ReadSlice(src, size-128, 128, limit); err == nil && id3.LooksLikeID3v1(tail) {
			t.ID3v1 = tail
			end = size - 128
			warnings = core.Warn(warnings, core.WarnTrailingID3v1, "legacy ID3v1 tag follows the audio; preserved")
		}
	}
	if at, ok, _ := ParseAt(src, end, limit, maxElements); ok && at.Offset >= minStart {
		t.Tag = at
		t.TagLen = at.Size
		end = at.Offset
		if at.Truncated {
			warnings = core.Warn(warnings, core.WarnElementCap,
				"the APE tag has more items than the element limit allows; the rest are not read and the file cannot be rewritten")
		}
	}
	t.Start = end
	return t, warnings
}

// Items returns tag items, or nil if no APEv2.
func (t Trailer) Items() []Item {
	if t.Tag == nil {
		return nil
	}
	return t.Tag.Items
}

// Describe for dump/native views. audioKind names the audio run.
func (t Trailer) Describe(audioKind, codec string) []core.NativeEntry {
	out := []core.NativeEntry{{Kind: audioKind, Size: int(t.Start), Note: codec}}
	if t.Tag != nil {
		out = append(out, core.NativeEntry{
			Kind: fmt.Sprintf("APEv%d", t.Tag.Version/1000),
			Size: int(t.TagLen),
			Note: fmt.Sprintf("%d items", len(t.Tag.Items)),
		})
		for _, it := range t.Tag.Items {
			note := ""
			if it.NonText() {
				note = "binary"
			}
			out = append(out, core.NativeEntry{Kind: "  " + it.Key, Size: len(it.Payload()), Note: note})
		}
	}
	if len(t.ID3v1) > 0 {
		out = append(out, core.NativeEntry{Kind: "ID3v1", Size: len(t.ID3v1), Note: "legacy, preserved"})
	}
	return out
}

// TrailerPlan: rebuilt items, rendered APEv2 (nil if empty => drop tag), ID3v1, ops.
type TrailerPlan struct {
	Items      []Item
	Bytes      []byte
	ID3v1      []byte
	Operations []string
	// Rebuild: unwritable items for PlanTrailingWrite warnings; see [RebuildInfo].
	Rebuild RebuildInfo
}

// RebuildTrailer: shared write side (audio before Start copied verbatim).
// Refuses if source Items were truncated by the element cap.
func RebuildTrailer(t Trailer, base, edited tag.TagSet, pictures []core.Picture,
	tagsChanged, picturesChanged, stripLegacy bool) (TrailerPlan, error) {

	var p TrailerPlan
	if t.Tag != nil && t.Tag.Truncated {
		return p, fmt.Errorf("%w: the APE tag has more items than the element limit allows; "+
			"rewriting it would drop the ones that were not read (raise the limit to edit this file)",
			waxerr.ErrSizeTooLarge)
	}
	p.Items, p.Rebuild = Rebuild(t.Items(), base, edited, pictures, picturesChanged)
	if tagsChanged {
		p.Operations = append(p.Operations, "APEv2 rewrite")
	}
	if picturesChanged {
		// Count written covers only (slot-dropped are warned, not claimed).
		p.Operations = append(p.Operations, fmt.Sprintf("pictures: %d", len(pictures)-len(p.Rebuild.SlotDroppedCovers)))
	}
	if len(p.Items) > 0 {
		// Keep source version/header shape (APEv1 must not be relabelled APEv2).
		version, hasHeader := writeVersion, true
		if t.Tag != nil {
			version, hasHeader = t.Tag.Version, t.Tag.HasHeader
		}
		var err error
		if p.Bytes, err = Render(p.Items, version, hasHeader); err != nil {
			return TrailerPlan{}, err
		}
	} else if t.Tag != nil {
		p.Operations = append(p.Operations, "APEv2 drop (no items remain)")
	}
	switch {
	case stripLegacy && len(t.ID3v1) > 0:
		p.Operations = append(p.Operations, "trailing ID3v1 strip")
	default:
		p.ID3v1 = t.ID3v1
	}
	return p, nil
}

// Segments after the caller's audio copy. ID3v1 is copied from source (byte-faithful).
func (p TrailerPlan) Segments(size int64) []bits.Segment {
	var segs []bits.Segment
	if p.Bytes != nil {
		segs = append(segs, bits.Lit(p.Bytes))
	}
	if n := int64(len(p.ID3v1)); n > 0 {
		segs = append(segs, bits.Copy(size-n, n))
	}
	return segs
}

// Result builds the post-write trailer without re-parsing.
func (p TrailerPlan) Result(start int64, src Trailer) Trailer {
	var newTag *Tag
	if len(p.Items) > 0 {
		newTag = NewEmpty()
		if src.Tag != nil {
			newTag.Version, newTag.HasHeader = src.Tag.Version, src.Tag.HasHeader
		}
		newTag.Items = p.Items
		newTag.Offset = start
		newTag.Size = int64(len(p.Bytes))
	}
	return Trailer{Tag: newTag, TagLen: int64(len(p.Bytes)), ID3v1: p.ID3v1, Start: start}
}

// LegacyFamilies: ID3v1 into family view without promoting into canonical.
func LegacyFamilies(auth tag.TagSet, id3v1 []byte) []core.FamilyValue {
	return id3.LegacyV1Families(auth, id3v1)
}

// CarryWarnings: recompute item-derived codes from written items so the result
// matches a fresh parse (rewrite may remove the item a prior warning described).
func CarryWarnings(prior []core.Warning, proj Projection, items []Item, id3v1 []byte) []core.Warning {
	var out []core.Warning
	for _, w := range prior {
		switch w.Code {
		case core.WarnInvalidPicture, core.WarnInheritedEncoder, core.WarnInvalidText, core.WarnInvalidTagKey:
			continue // recomputed below from the written items
		case core.WarnTrailingID3v1:
			if len(id3v1) == 0 {
				continue
			}
		}
		out = append(out, w)
	}
	out = append(out, proj.Warnings...)
	written := &Tag{Items: items}
	out = append(out, InvalidUTF8Warnings(written)...)
	out = append(out, InvalidKeyWarnings(written)...)
	return append(out, EncoderNoise(items)...)
}

// TrailingWrite inputs [PlanTrailingWrite]: audio before Start; optional Leading
// (Musepack front ID3v2), dropped by legacy strip.
type TrailingWrite struct {
	Format  core.Format
	Trailer Trailer
	Size    int64
	Leading []byte
}

// PlanTrailingWrite: shared rewrite for trailing-APEv2 containers.
// result builds codec-specific post-write Media (not called on no-op).
func PlanTrailingWrite(w TrailingWrite, base, edited *core.Media, opts core.WriteOptions,
	result func(tp TrailerPlan, newLeadingLen, newSize int64) *core.Media) (*core.WritePlan, error) {

	tagsChanged := !base.Tags.Equal(edited.Tags)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	strip := opts.Legacy == core.LegacyStrip
	stripLeading := strip && len(w.Leading) > 0
	stripTrailing := strip && len(w.Trailer.ID3v1) > 0

	report := core.WriteReport{Format: w.Format, BytesBefore: edited.Identity.Size}

	// No-op: verbatim copy flagged NoOp. APE has no chapter/synced-lyrics write.
	if !tagsChanged && !picturesChanged && !stripLeading && !stripTrailing {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	tp, err := RebuildTrailer(w.Trailer, base.Tags, edited.Tags, edited.Pictures, tagsChanged, picturesChanged, stripTrailing)
	if err != nil {
		return nil, err
	}
	report.Operations = append(report.Operations, tp.Operations...)
	report.Warnings = RebuildWarnings(report.Warnings, tp.Rebuild)
	// Warn on non-front/back role or description (same predicate as transfer).
	if picturesChanged && core.PicturesLoseMetadata(edited.Pictures, core.PictureLossNonCoverRoleAndDescription) {
		report.Warnings = core.Warn(report.Warnings, core.WarnPictureMetadataDropped,
			"the Cover Art convention stores only front and back covers: another role reads back as the cover name it was stored under, and descriptions are dropped")
	}

	var segs []bits.Segment
	newLeadingLen := int64(len(w.Leading))
	switch {
	case stripLeading:
		report.Operations = append(report.Operations, fmt.Sprintf("leading ID3v2 strip (%d bytes)", len(w.Leading)))
		newLeadingLen = 0
	case newLeadingLen > 0:
		segs = append(segs, bits.Copy(0, newLeadingLen))
	}
	segs = append(segs, bits.Copy(int64(len(w.Leading)), w.Trailer.Start-int64(len(w.Leading))))
	segs = append(segs, tp.Segments(w.Size)...)

	newSize := bits.OutputLen(segs)
	report.BytesAfter = newSize

	res := result(tp, newLeadingLen, newSize)
	// Downgrade only catches rebuild drops; legacy strip is structural change.
	if np := core.DowngradeNoOp(w.Format, edited.Identity.Size, base, res,
		base.Tags.Equal(res.Tags), stripLeading || stripTrailing, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: res}, nil
}
