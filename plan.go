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

// Plan is a resolved, ready-to-execute write produced by [Editor.Prepare]. It
// owns the byte-level rewrite and its report together, so [Plan.Report] is
// exactly what [Plan.Execute] will carry out - the two cannot drift.
//
// [Plan.Execute] may mutate the Plan (a committed [SaveBack] is recorded so further
// writes on it are refused), so a single Plan is not safe for concurrent Execute
// calls; prepare a fresh Plan per goroutine. [Plan.Report]/[Plan.Changes] and the
// rest of the read surface are pure and need no such care.
type Plan struct {
	doc  *Document
	plan *core.WritePlan
	opts core.WriteOptions
	// committed records that a [SaveBack] on this plan already wrote bytes (the
	// rename succeeded). Once set, [Plan.Execute] refuses any further write on this
	// plan: the segments describe the original->edited transform against the source
	// as parsed, so re-reading the now-rewritten file would corrupt the output (and a
	// second SaveBack would otherwise report a confusing "source changed"). Multi-
	// execution without a SaveBack commit - several [WriteTo] or [SaveAsFile] runs -
	// never sets this and stays valid. (M2)
	committed bool
}

// zero reports whether p is uninitialized: a nil Plan, a hand-built &Plan{} that never
// went through [Editor.Prepare], or a plan whose source document is zero. The read
// methods return empty values in that state, and Execute turns it into a regular error
// before reaching saveBack, saveAsFile, or writeTo. It is safe on a nil receiver.
func (p *Plan) zero() bool { return p == nil || p.plan == nil || p.doc.zero() }

// Report describes what executing the plan will do: the operations, the
// before/after sizes, the padding to be written, and any warnings. It performs
// no I/O. An uninitialized plan reports the empty WriteReport.
//
// It returns a defensive copy: the Operations and Warnings slices (the only
// reference types in a report) are cloned, and the warnings are run through the
// deep [core.CloneWarnings] so a caller mutating a returned warning's Keys cannot
// reach back into the plan's own report. Without this a consumer iterating
// Report().Warnings and editing Keys would corrupt a later Report() of the same
// plan.
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

// String renders the full human-readable preview of the plan: the field-level
// changes block (each line through the sanitizing [tag.Change.String]) followed
// by the [WriteReport] body (operations, size, padding, warnings). It is the
// complete preview a library consumer prints with fmt.Println(plan) - safe to
// send to a terminal by construction, since the only untrusted values (the change
// values) are sanitized. It carries no path header (that is CLI display context)
// and no trailing newline; a no-op plan renders just the report's "no changes"
// line.
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
		// Indent the change lines deeper than the report body's operations (2
		// spaces), so a removed-key change ("- KEY: ...") nests under "changes:"
		// rather than reading as a sibling operation line. This mirrors the CLI's
		// hierarchy (changes deeper than operations).
		b.WriteString("    ")
		b.WriteString(c.String())
		b.WriteByte('\n')
	}
	b.WriteString(report.String())
	return b.String()
}

// Changes reports the field-level delta this plan will apply: each canonical key
// added, removed, or changed, plus picture and chapter count-deltas when those
// sets differ. It diffs the pre-edit tags against the plan's
// post-codec-projection result - what the write actually lands, including date
// and number normalization - so the preview matches reality and a no-op plan
// yields no changes. It performs no I/O.
func (p *Plan) Changes() []tag.Change {
	// An uninitialized plan has no delta to report.
	if p.zero() {
		return nil
	}
	base := p.doc.media
	edited := p.plan.Result
	if edited == nil {
		// A plan with no computed result changes nothing; diff base against itself.
		edited = base
	}
	changes := tag.Diff(base.Tags, edited.Tags)
	if !core.EqualPictures(base.Pictures, edited.Pictures) {
		changes = append(changes, countChange("pictures", len(base.Pictures), len(edited.Pictures)))
	}
	if !core.EqualChapters(base.Chapters, edited.Chapters) {
		changes = append(changes, countChange("chapters", len(base.Chapters), len(edited.Chapters)))
	}
	return changes
}

// countChange renders a picture- or chapter-set change as a [tag.Change] under a
// reserved lowercase pseudo-key ("pictures"/"chapters"). The key is
// intentionally lowercase so it can never collide with a canonical tag key
// (which is always uppercase) while still flowing through the one shared change
// render/JSON path. The Old/New values are clean integer counts (no prose), so a
// machine consumer can parse them; an equal-count content change (a cover swap or
// a retitled chapter) is reported as ChangeChanged with matching counts ("N ->
// N"), and a defensive 0->0 lands there too rather than as a bogus "added 0".
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
	// Count mirrors the integer the text render highlights (the added/resulting count,
	// or the removed count) so a JSON consumer reads it directly instead of the bogus
	// stringified Old/New a count change formerly leaked into the machine output (P3).
	return c
}

// SaveResult reports the outcome of a save. Committed is true once the new
// bytes are in place (the rename succeeded); a later directory-fsync error is
// still returned, but with Committed true. Dest is the resulting file's
// identity, and Doc is the post-write document (also returned directly).
type SaveResult struct {
	Committed bool
	Dest      Identity
	Doc       *Document
}

// Execute carries out the plan against dst, one of [SaveBack], [SaveAsFile], or
// [WriteTo]. It returns the post-write [Document] and a [SaveResult]; on error,
// the SaveResult still carries what is known (e.g. Committed=false).
func (p *Plan) Execute(ctx context.Context, dst Destination) (*Document, SaveResult, error) {
	// An uninitialized plan has no rewrite to carry out.
	if p.zero() {
		return nil, SaveResult{}, fmt.Errorf("%w: plan is not initialized; call Editor.Prepare to build a plan", waxerr.ErrInvalidData)
	}
	if err := checkContext(ctx); err != nil {
		return nil, SaveResult{}, err
	}
	// A committed SaveBack spends the plan: its segments describe the original->edited
	// transform against the source as parsed, so re-executing ANY destination after the
	// file was rewritten in place would re-read the already-rewritten bytes (a resized
	// metadata region shifts every later copy offset) and corrupt the output. Refuse it
	// here, before dispatch, so SaveBack-then-SaveAsFile/WriteTo is caught too, not only a
	// second SaveBack. Multi-execution without a SaveBack commit - several WriteTo or
	// SaveAsFile runs - never sets committed and stays valid. (M2)
	if p.committed {
		return nil, SaveResult{}, fmt.Errorf("%w: this plan already saved %s via SaveBack; re-edit the returned Document to write again", waxerr.ErrInvalidData, p.doc.path)
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
	media.Identity = id
	return &Document{media: media, path: path, src: src}
}
