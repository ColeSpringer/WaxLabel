package waxlabel_test

import (
	"context"
	"os"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// TestSaveBackOnAReadOnlyFile: editing a read-only file succeeds and leaves it
// read-only. Windows has to clear the attribute for the rename and put it back, and the
// final assertion fails if that clear ever moves above the mode carry-over.
func TestSaveBackOnAReadOnlyFile(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o666) }) // so TempDir cleanup can remove it

	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, "ReadOnly").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	_, res, err := plan.Execute(context.Background(), wl.SaveBack())
	if err != nil {
		t.Fatalf("SaveBack on a read-only file: %v (committed=%v)", err, res.Committed)
	}
	if !res.Committed {
		t.Fatal("SaveBack on a read-only file reported no error but did not commit")
	}
	if got := mustParseFile(t, path).Fields().Title; got != "ReadOnly" {
		t.Errorf("title on disk = %q, want %q", got, "ReadOnly")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o200 != 0 {
		t.Errorf("the save cleared the read-only attribute: mode = %o, want no write bit", info.Mode().Perm())
	}
}
