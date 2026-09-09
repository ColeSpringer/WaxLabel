package matroska

import (
	"context"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
)

// TestPlanRefusesNonCoverPictureMIME covers the Plan-time guard that no editor path can now
// reach: Editor.AddPicture runs the authoritative sniff, which settles every added picture at
// an image type, the unrecognized MIME, or the link sentinel. The guard is what keeps a
// picture arriving under any other MIME from being dropped by the reprojection and collapsing
// the edit into a silent no-op, so it is exercised here at the codec boundary instead.
func TestPlanRefusesNonCoverPictureMIME(t *testing.T) {
	src := segBytes(cat(mkInfo("Title"), emptyCluster()))
	base := parseMKA(t, src)

	for _, mime := range []string{"text/plain", "application/pdf", core.LinkMIME} {
		edited := base.Clone()
		edited.Pictures = []core.Picture{{Type: core.PicFrontCover, MIME: mime, Data: []byte("not cover art")}}
		_, err := Codec{}.Plan(context.Background(), base, edited, core.DefaultWriteOptions())
		if err == nil {
			t.Errorf("MIME %q: Plan succeeded, want a refusal rather than a silently dropped picture", mime)
			continue
		}
		if !strings.Contains(err.Error(), "cover art") {
			t.Errorf("MIME %q: error = %v, want one naming cover art", mime, err)
		}
	}

	// The two MIMEs an attachment does read back as a cover are accepted.
	for _, mime := range []string{"image/png", core.UnrecognizedMIME} {
		edited := base.Clone()
		edited.Pictures = []core.Picture{{Type: core.PicFrontCover, MIME: mime, Data: []byte("bytes")}}
		_, err := Codec{}.Plan(context.Background(), base, edited, core.DefaultWriteOptions())
		if err != nil {
			t.Errorf("MIME %q: Plan = %v, want the cover accepted", mime, err)
		}
	}
}
