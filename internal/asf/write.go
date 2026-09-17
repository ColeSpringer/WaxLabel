package asf

import (
	"context"
	"fmt"

	"github.com/colespringer/waxlabel/internal/core"
)

// Plan refuses ASF rewrites after the no-op fast path (unchanged copy is always
// safe). Any real byte change returns the refusal.
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
	// Same predicate Capabilities uses for ReadOnly.
	return nil, refuseWrite()
}
