package waxlabel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

// tempFixture copies a testdata file into a fresh temp dir.
func tempFixture(t *testing.T, name string) (path string, data []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, data
}

// TestParseFileIdentityIsTheParsedFile: the identity describes the file ParseFile read, not
// whatever the path names by the time it returns.
func TestParseFileIdentityIsTheParsedFile(t *testing.T) {
	path, data := tempFixture(t, "sample.flac")
	orig, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Built before the swap: Windows loads a path stat's file index on first use.
	want := core.FileIdentity(path, orig)
	afterParseHook = func() {
		next := path + ".next"
		if err := os.WriteFile(next, append(data, 0, 0, 0), 0o644); err != nil {
			t.Error(err)
		}
		if err := os.Rename(next, path); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterParseHook = nil })

	doc, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	got := doc.Identity()
	if got.Size != orig.Size() || got.ModTimeUnixNano != want.ModTimeUnixNano || got.ChangeTimeUnixNano != want.ChangeTimeUnixNano ||
		got.INode != want.INode || got.Device != want.Device {
		t.Errorf("identity = %+v, want the parsed file's %+v", got, want)
	}
	if ok, why := got.MatchesContent(want); !ok {
		t.Errorf("identity names another file: %s", why)
	}
}

var errDisk = errors.New("disk failure")

// failingReaderAt reports size bytes but fails every read with errDisk.
type failingReaderAt struct{ size int64 }

func (f failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, errDisk }
func (f failingReaderAt) Size() int64                       { return f.size }

// TestShortFingerprintRereadIsSourceChanged: a source that serves fewer bytes than its size when the
// save re-reads the fingerprint is a changed file, not corrupt data.
func TestShortFingerprintRereadIsSourceChanged(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	doc, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, samePath := range []bool{true, false} {
		err := doc.verifySource(cutShort{size: doc.media.Identity.Size}, doc.media.Identity, samePath)
		if !errors.Is(err, waxerr.ErrSourceChanged) || errors.Is(err, waxerr.ErrInvalidData) {
			t.Errorf("samePath=%v: err = %v, want ErrSourceChanged and no invalid data", samePath, err)
		}
	}
}

// TestUnreadableFingerprintRefuses: a parse-time fingerprint the save cannot re-read is a
// refusal, not a skipped check, and the refusal keeps the read's cause.
func TestUnreadableFingerprintRefuses(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	doc, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.media.Identity.HasFinger {
		t.Fatal("setup: sample.flac has no metadata fingerprint")
	}
	for _, samePath := range []bool{true, false} {
		err := doc.verifySource(failingReaderAt{size: doc.media.Identity.Size}, doc.media.Identity, samePath)
		if !errors.Is(err, waxerr.ErrSourceChanged) || !errors.Is(err, errDisk) {
			t.Errorf("samePath=%v: err = %v, want ErrSourceChanged wrapping the read error", samePath, err)
		}
	}
}
