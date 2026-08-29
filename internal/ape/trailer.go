package ape

import (
	"fmt"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// WavPack, Monkey's Audio, and Musepack all store their metadata the same way: an
// APEv2 tag appended after the audio, optionally followed by a legacy ID3v1 tag. The
// audio itself is opaque to WaxLabel and copied verbatim, so the three codecs differ
// only in how they find where the audio ends and what they report about it. The
// shared shape lives here so they cannot drift.

// Trailer is the trailing-container region of such a file: the APEv2 tag (the native,
// authoritative store), any legacy ID3v1 after it, and the offset where the region
// begins - which is also the end of the audio.
type Trailer struct {
	Tag    *Tag
	TagLen int64
	ID3v1  []byte
	Start  int64
}

// PeelTrailer finds the trailing containers by walking in from the end of the file:
// the ID3v1 tag last-written sits after the APEv2 tag, so it is peeled first.
//
// minStart is the earliest offset a tag may begin at - the end of the container's
// first audio unit. An "APETAGEX" or "TAG" run before it is essence that happens to
// spell the preamble, not a tag; without the floor a crafted file can place a footer
// so the tag swallows the header the parser already read, and the rewrite then emits
// bytes that no longer parse.
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

// Items returns the parsed tag's items, or nil when the file carries no APEv2 tag.
func (t Trailer) Items() []Item {
	if t.Tag == nil {
		return nil
	}
	return t.Tag.Items
}

// Describe renders the trailing region for the dump/native views. audioKind names
// the container's audio run ("WavPack blocks", "Monkey's Audio frames").
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

// TrailerPlan is the rebuilt trailing region: the new item list, the rendered APEv2
// bytes (nil when no item survives, so an emptied tag is dropped rather than left as
// an empty container), the ID3v1 to keep, and the operations to report.
type TrailerPlan struct {
	Items      []Item
	Bytes      []byte
	ID3v1      []byte
	Operations []string
	// Rebuild records what it could not write here, for PlanTrailingWrite to turn into
	// warnings; see [RebuildInfo].
	Rebuild RebuildInfo
}

// RebuildTrailer applies an edit to the trailing region. It is the whole write side
// these codecs share: the audio before Start is copied verbatim, and everything after
// it comes from here.
//
// It refuses when the source tag's item list was truncated by the element cap: Items
// is then not the whole tag, and rebuilding from it would silently delete every item
// the parse could not see.
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
		p.Operations = append(p.Operations, fmt.Sprintf("pictures: %d", len(pictures)))
	}
	if len(p.Items) > 0 {
		// Keep the source tag's version and header shape: an APEv1 tag relabelled APEv2
		// would claim a UTF-8 guarantee its preserved bytes may not meet.
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

// Segments renders the rebuilt trailing region as rewrite segments, following the
// caller's verbatim copy of the audio. The ID3v1 is copied from the source rather
// than re-emitted as a literal, so a legacy tag round-trips byte for byte.
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

// Result is the post-write trailing region, for the result document a codec returns
// without re-parsing.
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

// LegacyFamilies projects a trailing ID3v1 tag into family/source entries. The
// canonical set stays APE-only, so this surfaces a value living only in the legacy
// container (and flags it when it disagrees) without promoting it.
func LegacyFamilies(auth tag.TagSet, id3v1 []byte) []core.FamilyValue {
	return id3.LegacyV1Families(auth, id3v1)
}

// CarryWarnings rebuilds the post-write warning set from what was actually written:
// the projection's own warnings and the inherited-encoder check over the written
// items, plus the source warnings that still hold. The trailing-ID3v1 note is dropped
// when a strip removed it, so the result matches a fresh parse of the output rather
// than echoing the source parse.
func CarryWarnings(prior []core.Warning, proj Projection, items []Item, id3v1 []byte) []core.Warning {
	var out []core.Warning
	for _, w := range prior {
		switch w.Code {
		case core.WarnInvalidPicture, core.WarnInheritedEncoder:
			continue // recomputed below from the written items
		case core.WarnTrailingID3v1:
			if len(id3v1) == 0 {
				continue
			}
		}
		out = append(out, w)
	}
	out = append(out, proj.Warnings...)
	return append(out, EncoderNoise(items)...)
}

// TrailingWrite is the input a codec whose only writable metadata is a trailing APEv2
// tag hands [PlanTrailingWrite]. Everything before Start is audio and is copied
// verbatim; Leading is an optional preserved region ahead of it (Musepack's stray front
// ID3v2), which a legacy strip drops.
type TrailingWrite struct {
	Format  core.Format
	Trailer Trailer
	Size    int64
	Leading []byte
}

// PlanTrailingWrite computes the whole rewrite for such a container. WavPack, Monkey's
// Audio, and Musepack differ only in how they find where the audio ends, so the write
// itself lives here: a fix to the no-op gate, the strip policy, or the segment layout
// lands once rather than three times.
//
// result builds the codec's own post-write Media from the rebuilt trailer; it is the
// only part that cannot be shared, because each codec owns its native document type. It
// is not called when the plan collapses to a no-op.
func PlanTrailingWrite(w TrailingWrite, base, edited *core.Media, opts core.WriteOptions,
	result func(tp TrailerPlan, newLeadingLen, newSize int64) *core.Media) (*core.WritePlan, error) {

	tagsChanged := !base.Tags.Equal(edited.Tags)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	strip := opts.Legacy == core.LegacyStrip
	stripLeading := strip && len(w.Leading) > 0
	stripTrailing := strip && len(w.Trailer.ID3v1) > 0

	report := core.WriteReport{Format: w.Format, BytesBefore: edited.Identity.Size}

	// Fast path: nothing changed. NoOpPlan emits a verbatim copy (so SaveAsFile and
	// WriteTo still produce a whole file) flagged NoOp so SaveBack skips it. APE has no
	// chapter or synced-lyrics convention, so neither can force a write here.
	if !tagsChanged && !picturesChanged && !stripLeading && !stripTrailing {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}

	tp, err := RebuildTrailer(w.Trailer, base.Tags, edited.Tags, edited.Pictures, tagsChanged, picturesChanged, stripTrailing)
	if err != nil {
		return nil, err
	}
	report.Operations = append(report.Operations, tp.Operations...)
	report.Warnings = RebuildWarnings(report.Warnings, tp.Rebuild)

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
	// APE stores values verbatim, so this downgrade catches only what the rebuild dropped
	// (an empty value, a key with no canonical mapping). A legacy strip is a real write no
	// tag comparison can see, so it is the structural-change flag.
	if np := core.DowngradeNoOp(w.Format, edited.Identity.Size, base, res,
		base.Tags.Equal(res.Tags), stripLeading || stripTrailing, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: res}, nil
}
