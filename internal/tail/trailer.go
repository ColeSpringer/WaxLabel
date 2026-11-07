package tail

import (
	"fmt"
	"slices"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/id3"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Trailer is the tail of a stream whose APE tag is its tag store (WavPack, Monkey's Audio, Musepack),
// as [Scan] found it. The APE tag nearest the end of the file is the store, and any other is a
// duplicate a rewrite drops. The ID3v1 tags and Lyrics3 blocks are legacy, kept unless stripped, and
// the junk is kept ahead of the store.
type Trailer struct {
	Layout
}

// Clone deep-copies t so a Document holding it stays detached.
func (t Trailer) Clone() Trailer { return Trailer{t.Layout.Clone()} }

// Items returns the store's items, or nil without a store.
func (t Trailer) Items() []ape.Item {
	if s := t.Tag(); s != nil {
		return s.Items
	}
	return nil
}

// Warnings reports t as [Warnings] does, and a store the element limit cut short.
func (t Trailer) Warnings(subject string) []core.Warning {
	ws := Warnings(t.Layout, subject)
	if s := t.Tag(); s != nil && s.Truncated {
		ws = core.Warn(ws, core.WarnElementCap,
			"the APE tag has more items than the element limit allows; the rest are not read and the file cannot be rewritten")
	}
	return ws
}

// LegacyFamilies projects t's ID3v1 tags into legacy family entries, none promoted into auth, and
// reports whether t holds legacy content they cannot carry: a Lyrics3 block. The store is projected
// as the canonical set; a duplicate is not projected.
func LegacyFamilies(auth tag.TagSet, t Trailer) ([]core.FamilyValue, bool) {
	legacy := t.Without(KindAPEv2)
	return Families(auth, legacy), legacy.Opaque()
}

// Project fills m from t, the leading ID3v2 tag kept ahead of the stream at streamAt and the bytes
// between them: the essence, the store's tags and pictures, and the family view, where the ID3
// containers are legacy only. It returns the store's warnings, how many bytes the essence covers,
// the figure an average bitrate counts, and how many bytes of the leading tag could not be read,
// for [Trailer.Describe].
func (t Trailer) Project(m *core.Media, leading []byte, streamAt int64, maxElements int) (ws []core.Warning, audioLen, leadingUnread int64) {
	audioLen = t.SetEssence(m, streamAt)
	store := t.Tag()
	proj := ape.Project(store)
	m.Tags, m.Pictures = proj.Tags, proj.Pictures
	ws = slices.Concat(proj.Warnings, ape.EncoderNoise(t.Items()), ape.InvalidUTF8Warnings(store), ape.InvalidKeyWarnings(store))
	trailing, trailingOpaque := LegacyFamilies(m.Tags, t)
	lead, leadOpaque, leadingUnread := id3.LegacyV2Families(m.Tags, leading, maxElements)
	m.Families = slices.Concat(proj.Families, trailing, lead)
	m.LegacyOpaqueContent = trailingOpaque || leadOpaque || streamAt > int64(len(leading))
	return ws, audioLen, leadingUnread
}

// Describe lists t for the native view: the leading ID3v2 tag if any, leadingUnread of its bytes
// unreadable ([Trailer.Project]), and the bytes after it up to streamAt, the audio from streamAt
// (named audioKind), then each region after it in file order, the store's items under it.
func (t Trailer) Describe(leading []byte, leadingUnread, streamAt int64, audioKind, codec string) []core.NativeEntry {
	var out []core.NativeEntry
	if len(leading) > 0 {
		out = append(out, core.LeadingID3Entry(len(leading), leadingUnread))
	}
	if gap := streamAt - int64(len(leading)); gap > 0 {
		out = append(out, core.LeadingBytesEntry(gap))
	}
	out = append(out, core.NativeEntry{Kind: audioKind, Size: int(t.Start - streamAt), Note: codec})
	store := t.Tag()
	for _, r := range t.Regions {
		if r.Kind != KindAPEv2 {
			out = append(out, r.entry())
			continue
		}
		e := core.NativeEntry{Kind: fmt.Sprintf("APEv%d", r.Tag.Version/1000), Size: int(r.Len),
			Note: fmt.Sprintf("%d items", len(r.Tag.Items))}
		if r.Tag != store {
			e.Note += ", duplicate, dropped on rewrite"
			out = append(out, e)
			continue
		}
		out = append(out, e)
		for _, it := range r.Tag.Items {
			note := ""
			if it.NonText() {
				note = "binary"
			}
			out = append(out, core.NativeEntry{Kind: "  " + it.Key, Size: len(it.Payload()), Note: note})
		}
	}
	return out
}

// TrailerPlan is a rebuilt trailer: the items the rewritten store holds and their rendering, nil when
// none remain, which drops the store; the operations to report; and what the rebuild could not write.
type TrailerPlan struct {
	Items      []ape.Item
	Bytes      []byte
	Operations []string
	Rebuild    ape.RebuildInfo

	trailer Trailer
	strip   bool // the Lyrics3 blocks and ID3v1 tags go
}

// RebuildTrailer rebuilds t's store from the edit in the store's version and header shape, and plans
// the rest of the trailer: the junk goes ahead of the rebuilt store, the duplicates go, and
// stripLegacy drops the Lyrics3 blocks and ID3v1 tags. A store the element limit cut short is refused,
// since a rewrite would lose the items it did not read.
func RebuildTrailer(t Trailer, base, edited tag.TagSet, pictures []core.Picture,
	tagsChanged, picturesChanged, stripLegacy bool) (TrailerPlan, error) {

	store := t.Tag()
	if store != nil && store.Truncated {
		return TrailerPlan{}, fmt.Errorf("%w: the APE tag has more items than the element limit allows; "+
			"rewriting it would drop the ones that were not read (raise the limit to edit this file)",
			waxerr.ErrSizeTooLarge)
	}
	p := TrailerPlan{trailer: t, strip: stripLegacy}
	p.Items, p.Rebuild = ape.Rebuild(t.Items(), base, edited, pictures, picturesChanged)
	if tagsChanged {
		p.Operations = append(p.Operations, "APEv2 rewrite")
	}
	if picturesChanged {
		// Count written covers only (slot-dropped are warned, not claimed).
		p.Operations = append(p.Operations, fmt.Sprintf("pictures: %d", len(pictures)-len(p.Rebuild.SlotDroppedCovers)))
	}
	if len(p.Items) > 0 {
		// Keep the store's version and header shape (APEv1 must not be relabelled APEv2).
		version, hasHeader := ape.WriteVersion, true
		if store != nil {
			version, hasHeader = store.Version, store.HasHeader
		}
		var err error
		if p.Bytes, err = ape.Render(p.Items, version, hasHeader); err != nil {
			return TrailerPlan{}, err
		}
	} else if store != nil {
		p.Operations = append(p.Operations, "APEv2 drop (no items remain)")
	}
	var moved int64
	for _, r := range t.Regions {
		switch r.Kind {
		case KindAPEv2:
			if r.Tag != store {
				p.Operations = append(p.Operations, "duplicate APE tag drop")
			}
		case KindJunk:
			if p.Bytes != nil && store != nil && r.Off > store.Offset {
				moved += r.Len
			}
		}
	}
	if moved > 0 {
		p.Operations = append(p.Operations, fmt.Sprintf("trailing bytes moved ahead of the APE tag (%d bytes)", moved))
	}
	if stripLegacy {
		p.Operations = append(p.Operations, Operations(t.Without(KindAPEv2, KindJunk), true)...)
	}
	return p, nil
}

// Segments copies what follows the audio in the rewritten file: the junk, the rebuilt store, then the
// Lyrics3 blocks and ID3v1 tags in file order unless stripped. No duplicate is copied.
func (p TrailerPlan) Segments() []bits.Segment {
	segs := Segments(p.trailer.Without(KindAPEv2, KindLyrics3v1, KindLyrics3v2, KindID3v1), false)
	if p.Bytes != nil {
		segs = append(segs, bits.Lit(p.Bytes))
	}
	return append(segs, Segments(p.trailer.Without(KindAPEv2, KindJunk), p.strip)...)
}

// duplicates is what each APE tag but the store holds that the file holds nowhere else, for the
// warning a rewrite that drops them owes: its values (graded by [core.AppendDuplicateBlockDropped]),
// the pictures neither before nor after holds, and the items the canonical view does not carry that
// the store holds neither as parsed nor as written. Pictures and items are indexed once, by hash.
func (t Trailer) duplicates(written []ape.Item, before, after *core.Media) []core.DuplicateContent {
	store := t.Tag()
	var pictures, items core.HashSet
	var out []core.DuplicateContent
	for _, r := range t.Regions {
		if r.Kind != KindAPEv2 || r.Tag == store {
			continue
		}
		if pictures == nil {
			pictures, items = core.PictureHashes(before, after), core.HashSet{}
			for _, it := range slices.Concat(t.Items(), written) {
				items.Add(itemHash(it))
			}
		}
		p := ape.Project(r.Tag)
		c := core.DuplicateContent{Tags: p.Tags}
		for _, pic := range p.Pictures {
			if !pictures.Has(core.PictureHash(pic)) {
				c.Pictures++
			}
		}
		var other core.Tally
		for _, it := range r.Tag.Items {
			if !ape.Carried(it) && !items.Has(itemHash(it)) {
				other.Add(fmt.Sprintf("%q item(s)", it.Key), 1)
			}
		}
		c.Other = other.Counts()
		out = append(out, c)
	}
	return out
}

// itemHash hashes an APE item's name, flags and payload.
func itemHash(it ape.Item) uint64 {
	h := core.ContentHash()
	fmt.Fprintf(h, "%s\x00%d\x00", it.Key, it.Flags)
	h.Write(it.Payload())
	return h.Sum64()
}

// TrailingWrite is what [PlanTrailingWrite] rewrites: an optional leading ID3v2 tag (Leading) and the
// bytes after it up to StreamAt, which a legacy strip drops together, the audio from StreamAt up to
// the trailer, and the trailer.
type TrailingWrite struct {
	Format   core.Format
	Trailer  Trailer
	Leading  []byte
	StreamAt int64
}

// PlanTrailingWrite plans the rewrite of a stream whose APE tag is its tag store: the audio copied
// verbatim, then the trailer as [TrailerPlan.Segments] lays it out. A no-op keeps every byte, the
// duplicates included. result builds the codec's plan result from the rebuilt trailer and the
// output's essence, [audioStart, audioEnd): the audio and, while the audio end is unknown, the junk
// the rewrite moves up to it. It is not called for a no-op.
func PlanTrailingWrite(w TrailingWrite, base, edited *core.Media, opts core.WriteOptions,
	result func(tp TrailerPlan, audioStart, audioEnd int64) *core.Media) (*core.WritePlan, error) {

	tagsChanged := !base.Tags.Equal(edited.Tags)
	picturesChanged := !core.EqualPictures(base.Pictures, edited.Pictures)
	strip := opts.Legacy == core.LegacyStrip
	stripLeading := strip && len(w.Leading) > 0
	stripTrailing := strip && len(w.Trailer.Without(KindAPEv2, KindJunk).Regions) > 0

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
	report.Warnings = ape.RebuildWarnings(report.Warnings, tp.Rebuild)
	// Warn on non-front/back role or description (same predicate as transfer).
	if picturesChanged && core.PicturesLoseMetadata(edited.Pictures, core.PictureLossNonCoverRoleAndDescription) {
		report.Warnings = core.Warn(report.Warnings, core.WarnPictureMetadataDropped,
			"the Cover Art convention stores only front and back covers: another role reads back as the cover name it was stored under, and descriptions are dropped")
	}

	var segs []bits.Segment
	newLeadingLen := w.StreamAt
	switch {
	case stripLeading:
		report.Operations = append(report.Operations, core.LeadingStripOp(int64(len(w.Leading)), w.StreamAt))
		newLeadingLen = 0
	case newLeadingLen > 0:
		segs = append(segs, bits.Copy(0, newLeadingLen))
	}
	segs = append(segs, bits.Copy(w.StreamAt, w.Trailer.Start-w.StreamAt))
	segs = append(segs, tp.Segments()...)
	report.BytesAfter = bits.OutputLen(segs)

	res := result(tp, newLeadingLen, newLeadingLen+covered(w.Trailer.Essence(w.StreamAt)))
	report.Warnings = core.AppendDuplicateBlockDropped(report.Warnings, "APE tag", base.Tags, res.Tags, w.Trailer.duplicates(tp.Items, base, res))
	// Downgrade only catches rebuild drops; legacy strip is structural change.
	if np := core.DowngradeNoOp(w.Format, edited.Identity.Size, base, res,
		base.Tags.Equal(res.Tags), stripLeading || stripTrailing, report.Warnings); np != nil {
		return np, nil
	}
	return &core.WritePlan{Segments: segs, NoOp: false, Report: report, Result: res}, nil
}
