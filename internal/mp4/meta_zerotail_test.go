package mp4

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestParseRejectsMetaZeroTailNoIlst covers the size-0-atom fix: a moov.udta.meta with no
// ilst whose last real child (hdlr) is followed by >=8 trailing zero bytes must be rejected,
// not silently absorbed. Before the fix a nested size-0 atom swallowed those zeros as one
// child spanning to meta.end(), hiding the gap from the meta-no-ilst check - so a create-ilst
// edit appended the new ilst after the zero atom and a re-parse re-expanded the zeros over
// the freshly written tags. This is the >=8-zero counterpart to the 1-7-byte gap the sibling
// TestParseRejectsUndersizedMetaGap already covers.
func TestParseRejectsMetaZeroTailNoIlst(t *testing.T) {
	ctx := context.Background()
	for _, tail := range []int{8, 9, 16, 64} {
		meta := renderFullBox(atomName("meta"), append(hdlrAtom(), make([]byte, tail)...))
		_, err := parse(ctx, core.BytesSource(mkMP4WithUdtaMeta(meta)), core.ParseOptions{})
		if !errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("hdlr + %d zero bytes: parse err = %v, want ErrInvalidData", tail, err)
		}
	}
}

// TestParseAcceptsTopLevelSizeZeroFinal is the regression guard for the fix's scope: the
// topLevel branch is unchanged, so a genuine "runs to EOF" last box (declared size 0 at the
// top level, e.g. a streaming mdat) still parses. Only a *nested* size-0 atom stops being
// absorbed.
func TestParseAcceptsTopLevelSizeZeroFinal(t *testing.T) {
	ctx := context.Background()
	ftyp := renderAtom(atomName("ftyp"), []byte("M4A \x00\x00\x00\x00M4A mp42"))
	moov := renderAtom(atomName("moov"), renderAtom(atomName("udta"), renderAtom(atomName("meta"), nil)))
	// A top-level mdat whose 32-bit size field is 0 runs to end-of-file.
	sizeZeroMdat := slices.Concat([]byte{0, 0, 0, 0}, []byte("mdat"), []byte("audiodata"))
	raw := slices.Concat(ftyp, moov, sizeZeroMdat)
	if _, err := parse(ctx, core.BytesSource(raw), core.ParseOptions{}); err != nil {
		t.Errorf("top-level size-0 final mdat: parse err = %v, want nil", err)
	}
}
