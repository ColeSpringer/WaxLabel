package waxlabel_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestSaveAsFileDetachedDocRemedy covers the fix: SaveAsFile on a detached Parse document
// (which carries no source bytes) fails with ErrInvalidData, and the remedy names the applicable
// next step - WriteTo with an explicit source - rather than the WithHashSource remedy that only
// fits the hashing path. Both callers shared one half-wrong message before this.
func TestSaveAsFileDetachedDocRemedy(t *testing.T) {
	ctx := context.Background()
	src, err := os.ReadFile(sampleFLAC)
	if err != nil {
		t.Fatal(err)
	}
	// Parse (not ParseFile/OpenSource) yields a detached document with no resolvable source.
	doc, err := wl.Parse(ctx, wl.BytesSource(src))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := doc.Edit().Set(tag.Title, "Detached").Prepare()
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out.flac")
	_, _, err = plan.Execute(ctx, wl.SaveAsFile(dst))
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Fatalf("Execute err = %v, want ErrInvalidData", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "WriteTo") {
		t.Errorf("remedy %q does not mention WriteTo", msg)
	}
	if msg := err.Error(); strings.Contains(msg, "WithHashSource") {
		t.Errorf("remedy %q wrongly mentions WithHashSource (that is the hash-path remedy)", msg)
	}
	// Nothing should have been written to the target path.
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("SaveAsFile wrote %s despite the no-source failure (stat err = %v)", dst, statErr)
	}
}
