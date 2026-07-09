package waxlabel

import (
	"context"
	"errors"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestSyncedLyricsNULRejectedAtCodec checks that a synced-lyrics model carrying an embedded NUL in
// its line text fails with waxerr.ErrInvalidData rather than writing a silently truncated SYLT
// frame, on every ID3-backed codec (MP3, WAV, AIFF, AAC). The Editor already rejects an authored
// NUL, so this deliberately bypasses it to reach the codec-level guard directly: a white-box test
// is the only way in. It reaches doc.media because a codec's Plan needs the full base Media, which
// the Document exposes only as projected views; and it lives here rather than in the codec packages
// because this is the one package where all four codecs are registered, and MP3/AAC have no
// in-memory parse harness to build a base Media from. All four route the guard through the shared
// id3.RebuildError next to their id3.CheckSize call.
func TestSyncedLyricsNULRejectedAtCodec(t *testing.T) {
	ctx := context.Background()
	for _, fixture := range []string{
		"testdata/notags.mp3",
		"testdata/notags.wav",
		"testdata/notags.aiff",
		"testdata/notags.aac",
	} {
		fixture := fixture
		t.Run(fixture, func(t *testing.T) {
			doc, err := ParseFile(ctx, fixture)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			base := doc.media
			codec, ok := core.ForFormat(base.Format)
			if !ok {
				t.Fatalf("no codec registered for %v", base.Format)
			}
			// A library caller that bypasses the editor injects a NUL into the modeled line text.
			edited := base.Clone()
			edited.SyncedLyrics = []core.SyncedLyrics{{
				Lines: []core.SyncedLine{{Time: 0, Text: "before\x00after"}},
			}}
			if _, err := codec.Plan(ctx, base, edited, core.DefaultWriteOptions()); !errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("Plan with a NUL synced-lyric line = %v, want waxerr.ErrInvalidData (not a truncated frame)", err)
			}
		})
	}
}
