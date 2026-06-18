package waxlabel

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// Plan is a resolved, ready-to-execute write produced by [Editor.Prepare]. It
// owns the byte-level rewrite and its report together, so [Plan.Report] is
// exactly what [Plan.Execute] will carry out - the two cannot drift.
type Plan struct {
	doc  *Document
	plan *core.WritePlan
	opts core.WriteOptions
}

// Report describes what executing the plan will do: the operations, the
// before/after sizes, the padding to be written, and any warnings. It performs
// no I/O.
func (p *Plan) Report() WriteReport { return p.plan.Report }

// IsNoOp reports whether the plan would not change the file's bytes. A no-op
// [SaveBack] writes nothing; a no-op [SaveAsFile] or [WriteTo] still produces a
// complete output (a fresh destination must be whole).
func (p *Plan) IsNoOp() bool { return p.plan.NoOp }

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
	if err := ctx.Err(); err != nil {
		return nil, SaveResult{}, err
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
