package waxlabel_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// failWriter errors after accepting limit bytes, simulating a write failure
// (e.g. a full disk) partway through.
type failWriter struct {
	limit, written int
}

func (w *failWriter) Write(p []byte) (int, error) {
	if w.written+len(p) > w.limit {
		n := w.limit - w.written
		w.written = w.limit
		return n, errors.New("simulated write failure")
	}
	w.written += len(p)
	return len(p), nil
}

func TestWriteToFailingWriterPropagatesError(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	src, _ := os.ReadFile(sampleFLAC)
	plan, err := doc.Edit().Set(tag.Title, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = plan.Execute(context.Background(), wl.WriteTo(&failWriter{limit: 100}, wl.BytesSource(src)))
	if err == nil {
		t.Fatal("expected an error from a failing writer")
	}
}

func TestContextCancellationStopsExecute(t *testing.T) {
	path := copyToTemp(t, sampleFLAC)
	before, _ := os.ReadFile(path)
	doc := mustParseFile(t, path)
	plan, err := doc.Edit().Set(tag.Title, "X").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("cancelled SaveBack modified the file")
	}
}

// SaveBack must be atomic and leave no temp litter, and a no-op must write
// nothing.
func TestSaveBackLeavesNoTempLitter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.flac")
	data, _ := os.ReadFile(sampleFLAC)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	doc := mustParseFile(t, path)
	plan, _ := doc.Edit().Set(tag.Title, "Committed").Prepare()
	if _, res, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil || !res.Committed {
		t.Fatalf("SaveBack: err=%v committed=%v", err, res.Committed)
	}
	assertNoTempFiles(t, dir)

	doc2 := mustParseFile(t, path)
	plan2, _ := doc2.Edit().Prepare()
	if _, res, err := plan2.Execute(context.Background(), wl.SaveBack()); err != nil || res.Committed {
		t.Fatalf("no-op SaveBack: err=%v committed=%v (want committed=false)", err, res.Committed)
	}
	assertNoTempFiles(t, dir)
}

func TestSaveAsFileToBadDirFailsCleanly(t *testing.T) {
	doc := mustParseFile(t, sampleFLAC)
	plan, _ := doc.Edit().Set(tag.Title, "X").Prepare()

	bad := filepath.Join(t.TempDir(), "does-not-exist", "out.flac")
	_, _, err := plan.Execute(context.Background(), wl.SaveAsFile(bad))
	if err == nil {
		t.Fatal("expected error writing into a nonexistent directory")
	}
	if _, statErr := os.Stat(bad); statErr == nil {
		t.Error("a partial output file was left behind")
	}
}

// mtime is updated by default (so scanners notice the edit) and kept with
// WithPreserveModTime.
func TestModTimePolicy(t *testing.T) {
	past := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("default updates mtime", func(t *testing.T) {
		path := copyToTemp(t, sampleFLAC)
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
		doc := mustParseFile(t, path)
		plan, _ := doc.Edit().Set(tag.Title, "a").Prepare()
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}
		if got := statMod(t, path); got.Equal(past) {
			t.Error("default SaveBack should have updated mtime, but it kept the old one")
		}
	})

	t.Run("WithPreserveModTime keeps mtime", func(t *testing.T) {
		path := copyToTemp(t, sampleFLAC)
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
		doc := mustParseFile(t, path)
		plan, _ := doc.Edit().Set(tag.Title, "b").Prepare(wl.WithPreserveModTime())
		if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
			t.Fatal(err)
		}
		if got := statMod(t, path); !got.Equal(past) {
			t.Errorf("WithPreserveModTime: mtime = %v, want %v", got, past)
		}
	})
}

func statMod(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime().UTC()
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".waxlabel-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
