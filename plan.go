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

// Plan is a resolved, ready-to-execute write produced by [Editor.Prepare]. It owns the
// byte-level rewrite and its report together, so [Plan.Report] cannot drift from what
// [Plan.Execute] carries out.
//
// Execute may mutate the Plan, so one Plan is not safe for concurrent Execute calls;
// prepare a fresh Plan per goroutine. The read surface is pure.
type Plan struct {
	doc  *Document
	plan *core.WritePlan
	opts core.WriteOptions
	// committed records that this plan already wrote over its own source file, via
	// [SaveBack] or a [SaveAsFile] onto the source path. Once set, [Plan.Execute] refuses
	// further writes: the segments describe a transform against the source AS PARSED, so
	// re-reading the rewritten file would corrupt the output. Writes to other paths never
	// set it and stay valid while the source bytes are stable.
	committed bool
}

// zero reports whether p is uninitialized: nil, hand-built, or holding a zero document.
// The read methods return empty values in that state and Execute returns a regular error.
// Safe on a nil receiver.
func (p *Plan) zero() bool { return p == nil || p.plan == nil || p.doc.zero() }

// Report describes what executing the plan will do: operations, before/after sizes,
// padding, warnings. No I/O. An uninitialized plan reports the empty WriteReport.
//
// The slices are cloned, warnings deeply, so a caller editing a returned warning's Keys
// cannot corrupt a later Report of the same plan.
func (p *Plan) Report() WriteReport {
	if p.zero() {
		return WriteReport{}
	}
	r := p.plan.Report
	r.Operations = slices.Clone(r.Operations)
	r.Warnings = core.CloneWarnings(r.Warnings)
	return r
}

// IsNoOp reports whether the plan would not change the file's bytes. A no-op
// [SaveBack] writes nothing; a no-op [SaveAsFile] or [WriteTo] still produces a
// complete output (a fresh destination must be whole). An uninitialized plan is not a
// no-op because it cannot be executed.
func (p *Plan) IsNoOp() bool {
	if p.zero() {
		return false
	}
	return p.plan.NoOp
}

// String renders the human-readable preview: the changes block, then the [WriteReport]
// body. Terminal-safe by construction, since the only untrusted values are sanitized by
// [tag.Change.String]. No path header, no trailing newline.
func (p *Plan) String() string {
	// Return before Report or Changes so an uninitialized plan prints a clear sentinel
	// instead of a misleading all-zero rewrite report.
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
		// Deeper than the report's 2-space operations, so a removed-key change nests
		// under "changes:" instead of reading as a sibling operation.
		b.WriteString("    ")
		b.WriteString(c.String())
		b.WriteByte('\n')
	}
	b.WriteString(report.String())
	return b.String()
}

// Changes reports the field-level delta: each canonical key added, removed, or changed,
// plus picture, chapter, and synced-lyrics count deltas. It diffs the pre-edit tags
// against the post-codec-projection result, so the preview matches what the write lands,
// normalization included. No I/O.
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
	return changes
}

// countChange renders a picture- or chapter-set change as a [tag.Change] under a reserved
// lowercase pseudo-key, lowercase so it cannot collide with a canonical key while still
// using the one shared render/JSON path. Old/New are bare integers so a machine consumer
// can parse them; an equal-count content change reports ChangeChanged with matching counts.
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
	// Count mirrors the integer the text render highlights, so a JSON consumer reads it
	// directly rather than the stringified Old/New.
	return c
}

// SaveResult reports the outcome of a save. Committed is true once the new bytes are
// in place (the rename succeeded); a later directory-fsync error is still returned,
// but with Committed true. Committed, not the error, is what says whether the file
// changed. See [Plan.Execute] for how the two combine.
//
// Dest is the identity of what is at the destination now: the written file after a
// commit, and otherwise whatever is still there (the untouched original for an
// in-place write, or the zero value when nothing exists at the target). Doc is the
// post-write document, also returned directly; it is nil only for a failed write.
type SaveResult struct {
	Committed bool
	Dest      Identity
	Doc       *Document
}

// Execute carries out the plan against dst, one of [SaveBack], [SaveAsFile], or
// [WriteTo]. It returns the post-write [Document] and a [SaveResult].
//
// A failed write is err != nil AND Committed false; neither alone is enough, because
// the two vary independently:
//
//   - err nil, Committed true: the bytes landed.
//   - err nil, Committed false: a no-op plan, which writes nothing by contract. The
//     Document is the unchanged one. Not a failure.
//   - err non-nil, Committed true: the write succeeded and a step after it did not
//     (see [SaveResult]). The edit is applied and the plan is spent, so treat it as a
//     success carrying a warning; retrying is refused and would be wrong.
//   - err non-nil, Committed false: nothing was written. The Document is nil, since
//     there is no post-write file to describe.
//
// A plan may be executed more than once as long as no execution writes over its own
// source file. Repeated [WriteTo] or [SaveAsFile] runs to other paths are valid while the
// source bytes stay stable, or while a stable source is passed to [WriteTo]. Once an
// execution writes over the source, [Execute] refuses later runs; re-edit the returned
// Document to write again.
func (p *Plan) Execute(ctx context.Context, dst Destination) (*Document, SaveResult, error) {
	if p.zero() {
		return nil, SaveResult{}, fmt.Errorf("%w: plan is not initialized; call Editor.Prepare to build a plan", waxerr.ErrInvalidData)
	}
	if err := checkContext(ctx); err != nil {
		return nil, SaveResult{}, err
	}
	// An in-place commit spends the plan for EVERY destination, not just a second
	// SaveBack: a resized metadata region shifts every later copy offset, so re-reading
	// the rewritten bytes would corrupt the output. Refused before dispatch.
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

// resultDocument builds the post-write Document from the codec's computed
// result, attaching the given path and in-memory source for further edits.
func (p *Plan) resultDocument(path string, src core.ReaderAtSized, id core.Identity) *Document {
	res := p.plan.Result
	if res == nil {
		res = p.doc.media
	}
	media := res.Clone()
	// A returned Document needs the structural fingerprint so later SaveBack calls keep
	// strong change detection; fileIdentity omits it. writeTo passes path="" and skips this.
	if path != "" && !id.HasFinger {
		if fSrc, err := openFileSource(path); err == nil {
			// The document's own parse limit, so a returned Document's identity agrees with a
			// fresh ParseFile under the same options (see Document.fingerprintLimit).
			if fp, ok := core.Fingerprint(fSrc, media, p.doc.fingerprintLimit()); ok {
				id.Fingerprint, id.HasFinger = fp, true
			}
			fSrc.Close()
		}
	}
	media.Identity = id
	// Inherit the base parse limits so a re-edit verifies under the ceilings the original
	// parse cleared.
	return &Document{media: media, path: path, src: src, limits: p.doc.limits}
}
