package waxlabel

import (
	"fmt"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Transfer copies metadata from one document onto another, with optional timeline
// replacements. [Document.Transfer] starts one; Plan/Prepare resolve it.
type Transfer struct {
	doc             *Document
	chapters        []Chapter
	chaptersSet     bool
	syncedLyrics    []core.SyncedLyrics
	syncedLyricsSet bool
}

// Transfer starts a transfer from this document.
func (d *Document) Transfer() *Transfer { return &Transfer{doc: d} }

// SetChapters replaces chapters carried (sorted by start). Empty list clears dest
// chapters; a source with none leaves dest alone. Their times are checked at Plan and Prepare.
func (t *Transfer) SetChapters(chs ...Chapter) *Transfer {
	t.chapters = normalizeChapters(chs)
	t.chaptersSet = true
	return t
}

// checkChapters refuses chapters set on the transfer that the source does not hold and no
// writer could store as given ([checkChapterTimes]), where the list is written.
func (t *Transfer) checkChapters(written bool) error {
	if !written {
		return nil
	}
	return checkChapterTimes(t.chapters, t.doc.media.Chapters)
}

// SetSyncedLyrics replaces synced lyrics carried. Empty clears dest; source with none leaves dest alone.
func (t *Transfer) SetSyncedLyrics(sls ...SyncedLyrics) *Transfer {
	t.syncedLyrics = normalizeSyncedLyrics(sls)
	t.syncedLyricsSet = true
	return t
}

// source is shallow read-only media used for grading and writing onto a destination of
// capabilities caps holding dst (nil for a format alone). A comment or lyric takes from the
// destination's item of the same text what the source's format has no field for, and what the
// language its writer fills in (ID3's eng) would be where the destination fills in the same one
// or stores none: the destination's, or no language ([core.CarryTextItems]). A last chapter
// that runs to the source's end is opened for the destination to end at its own, unless the
// destination holds the list as it is, or stores an end only where one is given and is no
// shorter than the source.
func (t *Transfer) source(caps Capabilities, dst *core.Media) *core.Media {
	m := *t.doc.media
	srcCaps := t.doc.Capabilities()
	var held core.Media
	var dstDuration time.Duration
	if dst != nil {
		held, dstDuration = *dst, dst.Properties.Duration()
	}
	// filler is the source's default language where it says nothing to this destination.
	filler := func(key tag.Key) string {
		if caps.TextDefaultLanguage() == srcCaps.TextDefaultLanguage() || caps.Field(key).TextLoss == core.TextLossDescriptionAndLanguage {
			return srcCaps.TextDefaultLanguage()
		}
		return ""
	}
	m.Comments = core.CarryTextItems(m.Comments, held.Comments, srcCaps.Field(tag.Comment).TextLoss, filler(tag.Comment))
	m.Lyrics = core.CarryTextItems(m.Lyrics, held.Lyrics, srcCaps.Field(tag.Lyrics).TextLoss, filler(tag.Lyrics))
	switch src := m.Properties.Duration(); {
	case t.chaptersSet:
		m.Chapters = t.chapters
	case dst != nil && core.EqualChapters(m.Chapters, dst.Chapters):
	case !caps.Chapters.ChapterLoss.KeepsEndsAsGiven(),
		dstDuration > 0 && dstDuration.Truncate(time.Millisecond) < src.Truncate(time.Millisecond):
		m.Chapters = core.OpenRunToEOFEnd(m.Chapters, src)
	}
	if t.syncedLyricsSet {
		m.SyncedLyrics = t.syncedLyrics
	}
	return &m
}

// Plan simulates a copy into format dst without writing. Read-only formats report
// everything dropped (does not refuse like Prepare). The report is graded from the format
// alone, as a write onto a file of it holding no metadata stores each item. [Transfer.Prepare]
// knows more: what the destination already holds, and what its plan writes, which it holds
// the report to.
func (t *Transfer) Plan(dst Format, opts ...WriteOption) (TransferReport, error) {
	if t.doc.zero() {
		return TransferReport{}, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}
	codec, ok := core.ForFormat(dst)
	if !ok {
		return TransferReport{}, fmt.Errorf("%w: %s", waxerr.ErrUnsupportedFormat, dst)
	}
	caps := codec.Capabilities(nil, resolveWriteOptions(opts))
	m := t.source(caps, nil)
	proj := core.ProjectTransferOnto(m, nil, caps)
	if err := t.checkChapters(proj.SetChapters); err != nil {
		return TransferReport{}, err
	}
	return TransferReport{Source: m.Format, Dest: dst, Items: proj.Items}, nil
}

// Prepare projects transfer metadata onto dst and returns a [Plan] plus [TransferReport].
// Source keys replace dest keys; pictures/chapters/lyrics replace when representable.
// Dest-only keys kept. What dst already holds as the source gives it is carried and not written
// again, so a transfer onto the same metadata is a no-op plan. The report is graded from what
// the plan stores: an item is carried only when the written file holds it as the source gives
// it. Byte-level validity failures return err with the report.
func (t *Transfer) Prepare(dst *Document, opts ...WriteOption) (*Plan, TransferReport, error) {
	if t.doc.zero() || dst.zero() {
		return nil, TransferReport{}, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}
	caps := dst.Capabilities(opts...)
	m := t.source(caps, dst.media)
	proj := core.ProjectTransferOnto(m, dst.media, caps)
	report := TransferReport{Source: m.Format, Dest: dst.media.Format, Items: proj.Items}

	// Read-only with drops: refuse (else NoOpPlan exits 0). Empty transfer may succeed.
	if caps.ReadOnly && report.HasDropped() {
		return nil, report, readOnlyRefusal(caps)
	}
	if err := t.checkChapters(proj.SetChapters); err != nil {
		return nil, report, err
	}

	// What the write turns out to store nothing of is left unwritten and the plan made again,
	// so an item reported dropped leaves the destination as it was: the value it held under
	// that key, and a key of its own the item would only have read back under.
	for {
		plan, err := t.editor(dst, m, proj).Prepare(append([]WriteOption{WithUnrecognizedPictures()}, opts...)...)
		if err != nil {
			return nil, report, err
		}
		// The grades above come from what the format can store. The plan knows what this write
		// stores, and the report says no more than that.
		stored := dst.media
		if plan.plan.Result != nil {
			stored = plan.plan.Result
		}
		items := core.ReconcileTransfer(proj, m, dst.media, stored)
		next, unwrites := proj.WithoutDropped(items)
		if !unwrites {
			report.Items = items
			plan.noChanges = report.NoChangesLine()
			return plan, report, nil
		}
		proj = next
	}
}

// editor is the destination's editor holding what the projection resolved to write: the fields
// it grades stored, and the pictures, chapters and synced lyrics it sets.
func (t *Transfer) editor(dst *Document, m *core.Media, proj core.TransferProjection) *Editor {
	ed := dst.Edit()
	ed.carried = true
	if proj.SetPictures {
		// The source's pictures take the place of the destination's. That is no ClearPictures:
		// nobody asked for the pictures the destination holds unread to go.
		ed.dropPictures()
		for _, p := range core.ClonePictures(proj.Pictures) {
			ed.addStoredPicture(p)
		}
	}
	for _, k := range proj.Keys {
		// A comment or lyric goes with the qualifiers the source's format holds for it.
		switch k {
		case tag.Comment:
			ed.SetComments(core.TextItemsAs[Comment](m.Comments)...)
		case tag.Lyrics:
			ed.SetLyrics(core.TextItemsAs[Lyrics](m.Lyrics)...)
		default:
			if vals, ok := m.Tags.Get(k); ok {
				ed.Set(k, vals...)
			}
		}
	}
	if proj.SetChapters {
		ed.SetChapters(core.CloneChapters(m.Chapters)...)
	}
	if proj.SetSyncedLyrics {
		ed.SetSyncedLyrics(core.CloneSyncedLyrics(proj.SyncedLyrics)...)
	}
	if t.chaptersSet && len(m.Chapters) == 0 && len(dst.media.Chapters) > 0 {
		ed.ClearChapters()
	}
	if t.syncedLyricsSet && len(m.SyncedLyrics) == 0 && len(dst.media.SyncedLyrics) > 0 {
		ed.ClearSyncedLyrics()
	}
	return ed
}

// PlanTransfer is [Transfer.Plan] with no replacements.
func (d *Document) PlanTransfer(dst Format, opts ...WriteOption) (TransferReport, error) {
	return d.Transfer().Plan(dst, opts...)
}

// PrepareTransfer is [Transfer.Prepare] with no replacements.
func (d *Document) PrepareTransfer(dst *Document, opts ...WriteOption) (*Plan, TransferReport, error) {
	return d.Transfer().Prepare(dst, opts...)
}

func readOnlyRefusal(caps Capabilities) error {
	if err := caps.ReadOnlyReason(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s files cannot be written", waxerr.ErrUnsupportedFormat, caps.Format)
}
