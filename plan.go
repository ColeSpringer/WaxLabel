package waxlabel

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan is a resolved write from [Editor.Prepare]. Report and Execute share state.
// Not safe for concurrent Execute; prepare one Plan per goroutine.
type Plan struct {
	doc  *Document
	plan *core.WritePlan
	opts core.WriteOptions
	// True after SaveBack or SaveAsFile onto the source path; further Execute refused.
	committed bool
}

// zero reports an uninitialized plan. Safe on a nil receiver.
func (p *Plan) zero() bool { return p == nil || p.plan == nil || p.doc.zero() }

// Report describes what Execute will do. No I/O. Slices are cloned.
func (p *Plan) Report() WriteReport {
	if p.zero() {
		return WriteReport{}
	}
	r := p.plan.Report
	r.Operations = slices.Clone(r.Operations)
	r.Warnings = core.CloneWarnings(r.Warnings)
	return r
}

// IsNoOp reports whether the plan would not change bytes. No-op SaveBack writes
// nothing; no-op SaveAsFile/WriteTo still write a complete file. Uninitialized is not no-op.
func (p *Plan) IsNoOp() bool {
	if p.zero() {
		return false
	}
	return p.plan.NoOp
}

// String renders the preview: changes then WriteReport. Terminal-safe.
func (p *Plan) String() string {
	if p.zero() {
		return "<uninitialized plan>"
	}
	report := p.Report()
	changes := p.Changes()
	if len(changes) == 0 {
		return report.String()
	}
	var b strings.Builder
	b.WriteString("changes:\n")
	for _, c := range changes {
		b.WriteString("    ")
		b.WriteString(c.String())
		b.WriteByte('\n')
	}
	b.WriteString(report.String())
	return b.String()
}

// Changes reports field-level delta vs post-codec projection. No I/O.
func (p *Plan) Changes() []tag.Change {
	if p.zero() {
		return nil
	}
	base := p.doc.media
	edited := p.plan.Result
	if edited == nil {
		edited = base // no computed result changes nothing
	}
	changes := tag.Diff(base.Tags, edited.Tags)
	if !core.EqualPictures(base.Pictures, edited.Pictures) {
		changes = append(changes, countChange("pictures", len(base.Pictures), len(edited.Pictures)))
	}
	if !core.EqualChapters(base.Chapters, edited.Chapters) {
		changes = append(changes, countChange("chapters", len(base.Chapters), len(edited.Chapters)))
	}
	if !core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics) {
		changes = append(changes, countChange("synced lyrics", len(base.SyncedLyrics), len(edited.SyncedLyrics)))
	}
	if before, after := base.Properties.First().OutputGain, edited.Properties.First().OutputGain; before != after {
		changes = append(changes, tag.Change{
			Key:  "output gain",
			Kind: tag.ChangeChanged,
			Old:  []string{core.OutputGainDB(before)},
			New:  []string{core.OutputGainDB(after)},
		})
	}
	return changes
}

// countChange renders a picture/chapter/lyrics count delta as a [tag.Change].
func countChange(key tag.Key, before, after int) tag.Change {
	c := tag.Change{Key: key}
	switch {
	case before == 0 && after > 0:
		c.Kind = tag.ChangeAdded
		c.New = []string{strconv.Itoa(after)}
		c.Count = after
	case after == 0 && before > 0:
		c.Kind = tag.ChangeRemoved
		c.Old = []string{strconv.Itoa(before)}
		c.Count = before
	default:
		c.Kind = tag.ChangeChanged
		c.Old = []string{strconv.Itoa(before)}
		c.New = []string{strconv.Itoa(after)}
		c.Count = after
	}
	return c
}

// SaveResult is the outcome of a save. Committed means bytes are in place (rename
// succeeded), even if a later directory fsync failed. See [Plan.Execute].
type SaveResult struct {
	Committed bool
	Dest      Identity
	Doc       *Document
}

// Execute runs the plan against [SaveBack], [SaveAsFile], or [WriteTo].
//
// Failed write: err != nil AND Committed false.
//   - err nil, Committed true: bytes landed.
//   - err nil, Committed false: no-op (writes nothing); not a failure.
//   - err non-nil, Committed true: bytes landed, later step failed; plan is spent.
//   - err non-nil, Committed false: nothing written; Document is nil.
//
// Reusable until an execution writes over the source path.
func (p *Plan) Execute(ctx context.Context, dst Destination) (*Document, SaveResult, error) {
	if p.zero() {
		return nil, SaveResult{}, fmt.Errorf("%w: plan is not initialized; call Editor.Prepare to build a plan", waxerr.ErrInvalidData)
	}
	if err := checkContext(ctx); err != nil {
		return nil, SaveResult{}, err
	}
	if p.committed {
		return nil, SaveResult{}, fmt.Errorf("%w: this plan already wrote %s in place; re-edit the returned Document to write again", waxerr.ErrInvalidData, p.doc.path)
	}
	switch dst.kind {
	case destSaveBack:
		return p.saveBack(ctx)
	case destSaveAsFile:
		return p.saveAsFile(ctx, dst.path)
	case destWriteTo:
		return p.writeTo(ctx, dst)
	default:
		return nil, SaveResult{}, fmt.Errorf("%w: unknown destination", waxerr.ErrInvalidData)
	}
}

// resultDocument builds the post-write Document from the codec result.
func (p *Plan) resultDocument(path string, src core.ReaderAtSized, id core.Identity) *Document {
	res := p.plan.Result
	if res == nil {
		res = p.doc.media
	}
	media := res.Clone()
	if path != "" && !id.HasFinger {
		if fSrc, err := openFileSource(path); err == nil {
			if fp, ok := core.Fingerprint(fSrc, media, p.doc.fingerprintLimit()); ok {
				id.Fingerprint, id.HasFinger = fp, true
			}
			fSrc.Close()
		}
	}
	media.Identity = id
	return &Document{media: media, path: path, src: src, limits: p.doc.limits}
}
