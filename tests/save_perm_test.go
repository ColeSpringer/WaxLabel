//go:build unix || windows

package waxlabel_test

import (
	"context"
	"os"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// saveTitle writes sampleFLAC with a TITLE edit to dst.
func saveTitle(t *testing.T, dst wl.Destination) {
	t.Helper()
	plan, err := mustParseFile(t, sampleFLAC).Edit().Set(tag.Title, "x").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), dst); err != nil {
		t.Fatal(err)
	}
}

func filePerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
