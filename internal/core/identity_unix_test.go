//go:build unix

package core

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestFileIdentityIsPlainDataOnUnix: where inode numbers identify the file, two stats of an
// unchanged file give equal identities, so callers can compare them with == or DeepEqual.
func TestFileIdentityIsPlainDataOnUnix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	x, y := FileIdentity(path, a), FileIdentity(path, b)
	if x != y || !reflect.DeepEqual(x, y) {
		t.Errorf("identities of an unchanged file differ: %+v vs %+v", x, y)
	}
	if x.INode == 0 {
		t.Error("no inode recorded")
	}
}
