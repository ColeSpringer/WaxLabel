package asf

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
)

// Plan refuses to rewrite an ASF file, but only after the no-op fast path: copying a
// file unchanged is always safe, even for a format WaxLabel will not write, so an
// unedited `copy` or `SaveAsFile` still produces a whole file and SaveBack still
// skips it. Anything that would actually change bytes returns the refusal.
func (Codec) Plan(ctx context.Context, base, edited *core.Media, _ core.WriteOptions) (*core.WritePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := edited.Native.(*doc)
	if !ok || d == nil {
		return nil, fmt.Errorf("asf: edited media has no ASF native document")
	}
	report := core.WriteReport{Format: core.FormatWMA, BytesBefore: edited.Identity.Size}
	if base.Tags.Equal(edited.Tags) && core.EqualPictures(base.Pictures, edited.Pictures) &&
		core.EqualChapters(base.Chapters, edited.Chapters) && core.EqualSyncedLyrics(base.SyncedLyrics, edited.SyncedLyrics) {
		return core.NoOpPlan(report, edited.Identity.Size, base), nil
	}
	// The same predicate Capabilities reports ReadOnly from, so the advertised
	// capability and the actual write outcome cannot diverge.
	return nil, d.refuseWrite()
}
