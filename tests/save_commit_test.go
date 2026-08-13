package waxlabel_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestInPlaceSaveCommitsCleanly is the end-to-end contract for an in-place write: err
// nil AND Committed true, with the edit on disk. Three bugs broke this at once on
// Windows: a source handle held across its own rename, a directory fsync that always
// failed, and a committed write then counted as a failure.
//
// VerifyEssence is on deliberately. verifyOutput is the code most likely to grow a
// source read later, and the source is now closed before the rename, so that would
// fail here with ErrClosed on every platform.
func TestInPlaceSaveCommitsCleanly(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		dest func(path string) wl.Destination
	}{
		{"SaveBack", func(string) wl.Destination { return wl.SaveBack() }},
		// Reaches the same rename through resolveSource rather than openFileSource.
		{"SaveAsFile onto the source", func(path string) wl.Destination { return wl.SaveAsFile(path) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := copyToTemp(t, sampleFLAC)
			doc := mustParseFile(t, path)
			plan, err := doc.Edit().Set(tag.Title, "Committed").Prepare(wl.WithVerifyEssence())
			if err != nil {
				t.Fatal(err)
			}
			out, res, err := plan.Execute(ctx, tc.dest(path))
			if err != nil {
				t.Fatalf("in-place save: %v (committed=%v)", err, res.Committed)
			}
			if !res.Committed {
				t.Fatal("in-place save reported no error but did not commit")
			}
			if out == nil {
				t.Fatal("a committed save must return the post-write Document")
			}
			// Re-read from disk rather than trusting the returned document.
			if got := mustParseFile(t, path).Fields().Title; got != "Committed" {
				t.Errorf("title on disk = %q, want %q", got, "Committed")
			}
		})
	}
}

// TestFailedSaveReturnsNilDocument pins the other half: a failed write returns no
// Document, matching every other failure path. A SaveAsFile into a missing directory
// fails at the temp create, before any rename.
func TestFailedSaveReturnsNilDocument(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	plan, err := doc.Edit().Set(tag.Title, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "does-not-exist", "out.flac")
	out, res, err := plan.Execute(context.Background(), wl.SaveAsFile(bad))
	if err == nil {
		t.Fatal("expected an error writing into a nonexistent directory")
	}
	if out != nil {
		t.Errorf("a failed save must return a nil Document, got %+v", out)
	}
	if res.Committed {
		t.Error("a failed save must not report Committed")
	}
	if _, statErr := os.Stat(bad); statErr == nil {
		t.Error("a partial output file was left behind")
	}
}
