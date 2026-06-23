package waxlabel_test

import (
	"context"
	"errors"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestNoAudioMP3RefusesHashAndWrite (H1): a non-empty text file named .mp3 parses
// (detected by extension) with no audio frames - the parser flags WarnNoAudioFrames
// even though it set a non-empty essence range over the text bytes. The library must
// refuse to hash, verify, or write it, so HashAudioEssence (and thus verify) and
// Editor.Prepare (and thus set/plan/lint --fix/copy-dest) all fail with ErrInvalidData
// rather than silently succeed over non-audio bytes - a no-audio file lints and verifies
// alike. empty.mp3 was already covered by the all-empty-range path; this is the
// non-empty-range case the digest guard formerly missed.
func TestNoAudioMP3RefusesHashAndWrite(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "notaudio.mp3", []byte("this is not audio, just plain text\n"))
	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !hasWarning(doc, wl.WarnNoAudioFrames) {
		t.Fatal("expected a no-audio warning on a text .mp3")
	}
	// HashAudioEssence refuses (the verify command inherits this).
	if _, err := doc.HashAudioEssence(ctx); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("HashAudioEssence err = %v, want ErrInvalidData", err)
	}
	// Editor.Prepare refuses (set/plan, lint --fix, and a copy's destination editor all
	// funnel through it, so they inherit the guard at one site).
	if _, err := doc.Edit().Set(tag.Title, "Y").Prepare(); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("Prepare err = %v, want ErrInvalidData", err)
	}
}
