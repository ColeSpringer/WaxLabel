package waxlabel

import (
	"fmt"

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
// chapters; a source with none leaves dest alone.
func (t *Transfer) SetChapters(chs ...Chapter) *Transfer {
	t.chapters = normalizeChapters(chs)
	t.chaptersSet = true
	return t
}

// SetSyncedLyrics replaces synced lyrics carried. Empty clears dest; source with none leaves dest alone.
func (t *Transfer) SetSyncedLyrics(sls ...SyncedLyrics) *Transfer {
	t.syncedLyrics = normalizeSyncedLyrics(sls)
	t.syncedLyricsSet = true
	return t
}

// source is shallow read-only media used for grading and writing onto a destination of
// capabilities dst. The language the source format's writer fills in for a comment or lyric
// (ID3's eng) is no language where dst fills in the same one or stores none.
func (t *Transfer) source(dst Capabilities) *core.Media {
	m := *t.doc.media
	if filler := t.doc.Capabilities().TextDefaultLanguage(); filler != "" {
		if dst.TextDefaultLanguage() == filler || dst.Field(tag.Comment).TextLoss == core.TextLossDescriptionAndLanguage {
			m.Comments = core.WithoutLanguage(m.Comments, filler)
		}
		if dst.TextDefaultLanguage() == filler || dst.Field(tag.Lyrics).TextLoss == core.TextLossDescriptionAndLanguage {
			m.Lyrics = core.WithoutLanguage(m.Lyrics, filler)
		}
	}
	if t.chaptersSet {
		m.Chapters = t.chapters
	} else {
		m.Chapters = core.OpenRunToEOFEnd(m.Chapters, m.Properties.Duration())
	}
	if t.syncedLyricsSet {
		m.SyncedLyrics = t.syncedLyrics
	}
	return &m
}

// Plan simulates a copy into format dst without writing. Read-only formats report
// everything dropped (does not refuse like Prepare).
func (t *Transfer) Plan(dst Format, opts ...WriteOption) (TransferReport, error) {
	if t.doc.zero() {
		return TransferReport{}, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}
	codec, ok := core.ForFormat(dst)
	if !ok {
		return TransferReport{}, fmt.Errorf("%w: %s", waxerr.ErrUnsupportedFormat, dst)
	}
	caps := codec.Capabilities(nil, resolveWriteOptions(opts))
	m := t.source(caps)
	return TransferReport{
		Source: m.Format,
		Dest:   dst,
		Items:  core.ProjectTransfer(m, caps),
	}, nil
}

// Prepare projects transfer metadata onto dst and returns a [Plan] plus [TransferReport].
// Source keys replace dest keys; pictures/chapters/lyrics replace when representable.
// Dest-only keys kept. Byte-level validity failures return err with the report.
func (t *Transfer) Prepare(dst *Document, opts ...WriteOption) (*Plan, TransferReport, error) {
	if t.doc.zero() || dst.zero() {
		return nil, TransferReport{}, fmt.Errorf("%w: document is not initialized; use ParseFile/Parse", waxerr.ErrInvalidData)
	}
	caps := dst.Capabilities(opts...)
	m := t.source(caps)
	items := core.ProjectTransfer(m, caps)
	report := TransferReport{Source: m.Format, Dest: dst.media.Format, Items: items}

	// Read-only with drops: refuse (else NoOpPlan exits 0). Empty transfer may succeed.
	if caps.ReadOnly && report.HasDropped() {
		return nil, report, readOnlyRefusal(caps)
	}

	ed := dst.Edit()
	ed.carried = true

	if !caps.ReadOnly && caps.Pictures.Write != core.AccessNone {
		representable, _, _ := core.PartitionRepresentable(caps.Pictures, core.ClonePictures(m.Pictures))
		representable, _, _ = core.PartitionPictureSlots(caps.Pictures, representable)
		if len(representable) > 0 {
			// The source's pictures take the place of the destination's. That is no ClearPictures:
			// nobody asked for the pictures the destination holds unread to go.
			ed.dropPictures()
			for _, p := range representable {
				ed.AddPicture(p)
			}
		}
	}

	srcCaps := t.doc.Capabilities()
	for _, it := range items {
		if it.Disposition == Dropped || it.Disposition == Excluded {
			continue
		}
		switch it.Kind {
		case core.TransferField:
			// A comment or lyric goes with the qualifiers the source's format holds for it.
			switch it.Key {
			case tag.Comment:
				carried := core.CarryTextItems(m.Comments, dst.media.Comments, srcCaps.Field(it.Key).TextLoss)
				ed.SetComments(core.TextItemsAs[Comment](carried)...)
			case tag.Lyrics:
				carried := core.CarryTextItems(m.Lyrics, dst.media.Lyrics, srcCaps.Field(it.Key).TextLoss)
				ed.SetLyrics(core.TextItemsAs[Lyrics](carried)...)
			default:
				if vals, ok := m.Tags.Get(it.Key); ok {
					ed.Set(it.Key, vals...)
				}
			}
		case core.TransferChapter:
			ed.SetChapters(core.CloneChapters(m.Chapters)...)
		case core.TransferSyncedLyric:
			ed.SetSyncedLyrics(core.CloneSyncedLyrics(m.SyncedLyrics)...)
		}
	}

	if t.chaptersSet && len(m.Chapters) == 0 && len(dst.media.Chapters) > 0 {
		ed.ClearChapters()
	}
	if t.syncedLyricsSet && len(m.SyncedLyrics) == 0 && len(dst.media.SyncedLyrics) > 0 {
		ed.ClearSyncedLyrics()
	}

	plan, err := ed.Prepare(append([]WriteOption{WithUnrecognizedPictures()}, opts...)...)
	if err != nil {
		return nil, report, err
	}
	return plan, report, nil
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
