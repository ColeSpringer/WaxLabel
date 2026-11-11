package ogg

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestPlanRemovesUnreadOwnedCommentsFromACommentPacket: in an Opus comment header too, a clear of
// the chapters, the synced lyrics or the pictures, or of the comment's name, writes the header
// without the owned comments nothing read, and an edit of another kind keeps them.
func TestPlanRemovesUnreadOwnedCommentsFromACommentPacket(t *testing.T) {
	const chapter, lyrics, picture = "CHAPTER001=bogus", "SYNCEDLYRICS=words", "METADATA_BLOCK_PICTURE=bad~"
	src := buildOpusStreamWithGain(t, 0, chapter, lyrics, picture)
	base, err := parse(context.Background(), Codec{}, core.BytesSource(src), core.DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	cleared := func(k tag.Key) core.WriteOptions {
		opts := core.DefaultWriteOptions()
		opts.Touched = map[tag.Key]bool{k: true}
		return opts
	}
	with := func(set func(*core.WriteOptions)) core.WriteOptions {
		opts := core.DefaultWriteOptions()
		set(&opts)
		return opts
	}
	for name, tc := range map[string]struct {
		opts core.WriteOptions
		gain int
		gone string
	}{
		"the chapters cleared":      {opts: with(func(o *core.WriteOptions) { o.ChaptersCleared = true }), gone: chapter},
		"the synced lyrics cleared": {opts: with(func(o *core.WriteOptions) { o.SyncedLyricsCleared = true }), gone: lyrics},
		"the pictures cleared":      {opts: with(func(o *core.WriteOptions) { o.PicturesCleared = true }), gone: picture},
		"a chapter's name cleared":  {opts: cleared("CHAPTER001"), gone: chapter},
		"the lyrics name cleared":   {opts: cleared("SYNCEDLYRICS"), gone: lyrics},
		"the picture name cleared":  {opts: cleared("METADATA_BLOCK_PICTURE"), gone: picture},
		// The gain alone would rewrite the first page and copy the comment header.
		"a name cleared with the gain set": {opts: cleared("SYNCEDLYRICS"), gain: 256, gone: lyrics},
	} {
		edited := base.Clone()
		if tc.gain != 0 {
			props := edited.Properties.Clone()
			props.Tracks[0].OutputGain = tc.gain
			edited.Properties = props
		}
		plan, err := NewOpus().Plan(context.Background(), base, edited, tc.opts)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if plan.NoOp || !slices.ContainsFunc(plan.Report.Operations, func(op string) bool { return strings.Contains(op, "unread") }) {
			t.Errorf("%s: no-op %v, operations %q", name, plan.NoOp, plan.Report.Operations)
			continue
		}
		out := renderPlan(t, src, plan)
		for _, cm := range []string{chapter, lyrics, picture} {
			if gone := !bytes.Contains(out, []byte(cm)); gone != (cm == tc.gone) {
				t.Errorf("%s: %q gone = %v", name, cm, gone)
			}
		}
		re, err := parse(context.Background(), Codec{}, core.BytesSource(out), core.DefaultParseOptions())
		if err != nil || re.Properties.First().OutputGain != tc.gain {
			t.Errorf("%s: the output reads with gain %d, err %v; want %d", name, re.Properties.First().OutputGain, err, tc.gain)
		}
	}

	// An edit that names none of them keeps all three.
	edited := base.Clone()
	edited.Tags.Set(tag.Title, "Tune")
	plan, err := NewOpus().Plan(context.Background(), base, edited, core.DefaultWriteOptions())
	if err != nil {
		t.Fatal(err)
	}
	if out := renderPlan(t, src, plan); !bytes.Contains(out, []byte(chapter)) || !bytes.Contains(out, []byte(lyrics)) || !bytes.Contains(out, []byte(picture)) {
		t.Error("a title edit dropped an owned comment it did not name")
	}
}
