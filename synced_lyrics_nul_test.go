package waxlabel

import (
	"context"
	"errors"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestSyncedLyricsNULRejectedAtCodec: codec Plan rejects NUL in SYLT line text
// (ErrInvalidData) on MP3/WAV/AIFF/AAC. Bypasses Editor; all four share id3.RebuildError.
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
			// Bypass editor: inject NUL into modeled line text.
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
