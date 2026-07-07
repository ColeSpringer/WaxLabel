package waxlabel_test

import (
	"context"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestReservedNamespaceDropOnSet drives the public edit flow on every Vorbis-comment container
// (FLAC, Ogg Vorbis, Opus) and pins that setting a custom key in one of the three reserved
// namespaces - SYNCEDLYRICS synced lyrics, METADATA_BLOCK_PICTURE cover art - via --set is
// dropped-with-warning rather than written. The payloads are deliberately *valid* (a real LRC
// line and a real base64 cover) so this pins the v1.0 decision: a valid payload set through --set
// no longer sneaks in through a side channel as structured data. The write collapses to a no-op
// (result re-projects to base) carrying a value-dropped warning, and the file gains no synced
// lyrics or cover. A later reader must not "restore" the side channel; that would reopen the
// silent-loss / silent-structuring split the guard closes.
func TestReservedNamespaceDropOnSet(t *testing.T) {
	ctx := context.Background()
	// A valid base64 METADATA_BLOCK_PICTURE value (the same encoding a real Ogg cover uses).
	validCover := commentPictureValue(wl.Picture{Type: wl.PicFrontCover, MIME: "image/png", Data: tinyPNG()})

	for _, fx := range []string{"../testdata/sample.flac", "../testdata/sample.ogg", "../testdata/sample.opus"} {
		for _, tc := range []struct {
			label string
			key   tag.Key
			value string
		}{
			{"synced lyrics", tag.Key("SYNCEDLYRICS"), "[00:01.00]hi"},
			{"cover art", tag.Key("METADATA_BLOCK_PICTURE"), validCover},
		} {
			t.Run(fx+"/"+tc.label, func(t *testing.T) {
				src := readFixture(t, fx)
				doc := mustParseBytes(t, src)
				baseLyrics, basePics := len(doc.SyncedLyrics()), len(doc.Pictures())

				plan, err := doc.Edit().Set(tc.key, tc.value).Prepare()
				if err != nil {
					t.Fatalf("Prepare: %v", err)
				}
				// The valid payload re-projects to base (it is dropped, not stored), so the write is a
				// no-op that still must carry the value-dropped warning - never a silent exit 0.
				if !plan.IsNoOp() {
					t.Errorf("expected a no-op write (the reserved key is dropped, nothing else changed); got a real write: %v", plan.Report().Operations)
				}
				if !hasKeyedValueDropped(plan.Report().Warnings, tc.key) {
					t.Errorf("no value-dropped warning for the dropped reserved key %s; got %v", tc.key, plan.Report().Warnings)
				}

				// Execute and re-parse: the file must gain no structured lyrics or cover.
				var buf writerTo
				if _, _, err := plan.Execute(ctx, wl.WriteTo(&buf, wl.BytesSource(src))); err != nil {
					t.Fatalf("Execute: %v", err)
				}
				out := mustParseBytes(t, buf.b)
				if got := len(out.SyncedLyrics()); got != baseLyrics {
					t.Errorf("synced-lyrics count changed %d -> %d; a --set reserved key must not add structured lyrics", baseLyrics, got)
				}
				if got := len(out.Pictures()); got != basePics {
					t.Errorf("picture count changed %d -> %d; a --set reserved key must not add a cover", basePics, got)
				}
			})
		}
	}
}
