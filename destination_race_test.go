package waxlabel

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// fileLocks is whether SaveBack holds a file lock here (flock platforms).
const fileLocks = runtime.GOOS == "linux" || runtime.GOOS == "android" || runtime.GOOS == "darwin" ||
	runtime.GOOS == "ios" || runtime.GOOS == "freebsd" || runtime.GOOS == "netbsd" ||
	runtime.GOOS == "openbsd" || runtime.GOOS == "dragonfly"

// setSaveHook runs fn at the named save stage for the rest of the test.
func setSaveHook(t *testing.T, stage string, fn func()) {
	t.Helper()
	saveHook = func(s string) {
		if s == stage {
			fn()
		}
	}
	t.Cleanup(func() { saveHook = nil })
}

// titlePlan parses path and plans a TITLE edit.
func titlePlan(t *testing.T, path string) *Plan {
	t.Helper()
	doc, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := doc.Edit().Set(tag.Title, "Raced").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func appendTo(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if IsTempFileName(e.Name()) {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s holds %d bytes, want the %d it held before the refused save", path, len(got), len(want))
	}
}

func refusedAsChanged(t *testing.T, res SaveResult, err error) {
	t.Helper()
	if !errors.Is(err, waxerr.ErrSourceChanged) {
		t.Fatalf("err = %v, want ErrSourceChanged", err)
	}
	if res.Committed {
		t.Error("a refused save reports Committed")
	}
}

// TestSaveBackRefusesAppendDuringCopy: bytes appended while the copy ran are not lost under the
// rewrite.
func TestSaveBackRefusesAppendDuringCopy(t *testing.T) {
	path, data := tempFixture(t, "sample.flac")
	plan := titlePlan(t, path)
	setSaveHook(t, "copied", func() { appendTo(t, path) })
	_, res, err := plan.Execute(context.Background(), SaveBack())
	refusedAsChanged(t, res, err)
	assertBytes(t, path, append(data, 0))
	assertNoTemps(t, filepath.Dir(path))
}

// TestSaveBackRefusesTruncationDuringCopy: a source cut short under the copy is a changed
// source, not corrupt data.
func TestSaveBackRefusesTruncationDuringCopy(t *testing.T) {
	path, data := tempFixture(t, "sample.flac")
	plan := titlePlan(t, path)
	setSaveHook(t, "copy", func() {
		if err := os.Truncate(path, int64(len(data)/2)); err != nil {
			t.Fatal(err)
		}
	})
	_, res, err := plan.Execute(context.Background(), SaveBack())
	refusedAsChanged(t, res, err)
	if errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("err = %v, also classed as invalid data", err)
	}
	assertBytes(t, path, data[:len(data)/2])
	assertNoTemps(t, filepath.Dir(path))
}

// TestSaveBackRefusesRenameBeforeCommit: a competitor that renames its file over the path after
// the copy keeps its file; only the path shows the change.
func TestSaveBackRefusesRenameBeforeCommit(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	other, err := os.ReadFile(filepath.Join("testdata", "notags.flac"))
	if err != nil {
		t.Fatal(err)
	}
	plan := titlePlan(t, path)
	setSaveHook(t, "commit", func() {
		next := path + ".competitor"
		if err := os.WriteFile(next, other, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(next, path); err != nil {
			t.Fatal(err)
		}
	})
	_, res, err := plan.Execute(context.Background(), SaveBack())
	refusedAsChanged(t, res, err)
	assertBytes(t, path, other)
	assertNoTemps(t, filepath.Dir(path))
}

// TestDerivedWritesRefuseAppendDuringCopy: SaveAsFile and WriteTo check the source after the copy
// too; WriteTo's streamed bytes are then to be discarded.
func TestDerivedWritesRefuseAppendDuringCopy(t *testing.T) {
	t.Run("SaveAsFile", func(t *testing.T) {
		path, _ := tempFixture(t, "sample.flac")
		plan := titlePlan(t, path)
		setSaveHook(t, "copied", func() { appendTo(t, path) })
		out := filepath.Join(t.TempDir(), "out.flac")
		_, res, err := plan.Execute(context.Background(), SaveAsFile(out))
		refusedAsChanged(t, res, err)
		if _, err := os.Stat(out); err == nil {
			t.Error("a refused SaveAsFile left an output file")
		}
		assertNoTemps(t, filepath.Dir(out))
	})
	t.Run("WriteTo", func(t *testing.T) {
		path, _ := tempFixture(t, "sample.flac")
		plan := titlePlan(t, path)
		setSaveHook(t, "copied", func() { appendTo(t, path) })
		var buf bytes.Buffer
		_, res, err := plan.Execute(context.Background(), WriteTo(&buf, nil))
		refusedAsChanged(t, res, err)
		if !strings.Contains(err.Error(), "during") {
			t.Errorf("err = %v, want it to say the source changed during the write", err)
		}
	})
}

// TestConcurrentSaveBacksSerialize: two saves of one file never both commit. With a file lock the
// save that started first commits and the other refuses; without one, the path re-check still
// refuses whichever renames second.
func TestConcurrentSaveBacksSerialize(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	first, second := titlePlan(t, path), titlePlan(t, path)
	paused, release := make(chan struct{}), make(chan struct{})
	var taken atomic.Bool
	saveHook = func(stage string) {
		if stage == "copied" && taken.CompareAndSwap(false, true) {
			close(paused)
			<-release
		}
	}
	t.Cleanup(func() { saveHook = nil })

	type outcome struct {
		res SaveResult
		err error
	}
	run := func(p *Plan, out chan<- outcome) {
		_, res, err := p.Execute(context.Background(), SaveBack())
		out <- outcome{res, err}
	}
	a, b := make(chan outcome, 1), make(chan outcome, 1)
	go run(first, a)
	<-paused
	go run(second, b)
	time.Sleep(100 * time.Millisecond) // let the second save reach the lock
	close(release)
	ra, rb := <-a, <-b

	if ra.res.Committed == rb.res.Committed {
		t.Fatalf("committed: first=%v (%v) second=%v (%v), want exactly one", ra.res.Committed, ra.err, rb.res.Committed, rb.err)
	}
	loser := rb
	if rb.res.Committed {
		loser = ra
	}
	if fileLocks {
		if !ra.res.Committed || !errors.Is(rb.err, waxerr.ErrSourceChanged) {
			t.Errorf("first=%v (%v), second err=%v; want the first to commit and the second to refuse", ra.res.Committed, ra.err, rb.err)
		}
	} else if loser.err == nil {
		// Without a lock (Windows) the loser refuses as changed, or fails on the winner's handle.
		t.Error("the save that did not commit reported no error")
	}
	assertNoTemps(t, filepath.Dir(path))
}

// failAfter accepts n bytes, then fails.
type failAfter struct{ n int }

func (w *failAfter) Write(p []byte) (int, error) {
	if len(p) <= w.n {
		w.n -= len(p)
		return len(p), nil
	}
	k := w.n
	w.n = 0
	return k, errors.New("disk full")
}

// TestWriteToFailureIsUncommitted: a stream that fails partway reports nothing committed and no
// Document; the bytes it already took are the caller's to discard.
func TestWriteToFailureIsUncommitted(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	doc, res, err := titlePlan(t, path).Execute(context.Background(), WriteTo(&failAfter{n: 100}, nil))
	if err == nil || res.Committed || doc != nil {
		t.Errorf("failed WriteTo = (doc %v, committed %v, err %v), want (nil, false, an error)", doc != nil, res.Committed, err)
	}
}

// TestNoReplaceCommitsOnce: two no-replace writes racing to one new path commit exactly once; the
// other finds the target there and leaves no temp.
func TestNoReplaceCommitsOnce(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	out := filepath.Join(t.TempDir(), "new.flac")
	plans := make([]*Plan, 2)
	for i := range plans {
		doc, err := ParseFile(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if plans[i], err = doc.Edit().Set(tag.Title, "Raced").Prepare(WithNoReplace()); err != nil {
			t.Fatal(err)
		}
	}
	var arrived sync.WaitGroup
	arrived.Add(len(plans))
	saveHook = func(stage string) {
		if stage == "commit" {
			arrived.Done()
			arrived.Wait() // both writers pass every check before either commits
		}
	}
	t.Cleanup(func() { saveHook = nil })

	type outcome struct {
		res SaveResult
		err error
	}
	results := make(chan outcome, len(plans))
	for _, p := range plans {
		go func() {
			_, res, err := p.Execute(context.Background(), SaveAsFile(out))
			results <- outcome{res, err}
		}()
	}
	var committed, exist int
	for range plans {
		r := <-results
		if r.res.Committed {
			committed++
		} else if errors.Is(r.err, fs.ErrExist) {
			exist++
		} else {
			t.Errorf("uncommitted write failed with %v, want fs.ErrExist", r.err)
		}
	}
	if committed != 1 || exist != 1 {
		t.Errorf("committed %d, found the target %d; want one of each", committed, exist)
	}
	assertNoTemps(t, filepath.Dir(out))
}

// TestSaveBackRefusesMetadataEditBeforeCommit: a same-size metadata edit with the mtime put back,
// made just before the commit, is caught by the fingerprint re-read through the path. That is the
// whole guard where the stat has no change time (Windows).
func TestSaveBackRefusesMetadataEditBeforeCommit(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	plan := titlePlan(t, path)
	var edited []byte
	setSaveHook(t, "commit", func() {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data[bytes.Index(data, []byte("Original Title"))] ^= 0xFF
		if err := os.WriteFile(path, data, info.Mode()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		edited = data
	})
	_, res, err := plan.Execute(context.Background(), SaveBack())
	refusedAsChanged(t, res, err)
	if !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("err = %v, want it to cite the fingerprint", err)
	}
	assertBytes(t, path, edited)
	assertNoTemps(t, filepath.Dir(path))
}

// TestSaveBackRefusesVanishedSourceBeforeCommit: a source deleted after the copy is not recreated.
func TestSaveBackRefusesVanishedSourceBeforeCommit(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	plan := titlePlan(t, path)
	setSaveHook(t, "commit", func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	})
	_, res, err := plan.Execute(context.Background(), SaveBack())
	refusedAsChanged(t, res, err)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want it to wrap fs.ErrNotExist", err)
	}
	if _, serr := os.Stat(path); serr == nil {
		t.Error("the refused save recreated the deleted source")
	}
	assertNoTemps(t, filepath.Dir(path))
}

// TestSaveBackRefusesASourceThatIsNoLongerAFile: a path that now names a directory is a changed
// source, both when the save reopens it and when the commit re-checks it.
func TestSaveBackRefusesASourceThatIsNoLongerAFile(t *testing.T) {
	toDir := func(t *testing.T, path string) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, stage := range []string{"before", "commit"} {
		t.Run(stage, func(t *testing.T) {
			path, _ := tempFixture(t, "sample.flac")
			plan := titlePlan(t, path)
			if stage == "before" {
				toDir(t, path)
			} else {
				setSaveHook(t, stage, func() { toDir(t, path) })
			}
			_, res, err := plan.Execute(context.Background(), SaveBack())
			refusedAsChanged(t, res, err)
			if errors.Is(err, waxerr.ErrInvalidData) {
				t.Errorf("err = %v, also classed as invalid data", err)
			}
		})
	}
}

// TestDerivedWriteToleratesAChmodDuringCopy: a derived write refuses a content change during the
// copy, which moves the mtime, but not a permission change, which only moves the change time.
func TestDerivedWriteToleratesAChmodDuringCopy(t *testing.T) {
	path, _ := tempFixture(t, "sample.flac")
	plan := titlePlan(t, path)
	setSaveHook(t, "copied", func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	out := filepath.Join(t.TempDir(), "out.flac")
	if _, res, err := plan.Execute(context.Background(), SaveAsFile(out)); err != nil || !res.Committed {
		t.Errorf("SaveAsFile after a chmod during the copy: err=%v committed=%v", err, res.Committed)
	}

	path, _ = tempFixture(t, "sample.flac")
	plan = titlePlan(t, path)
	setSaveHook(t, "copied", func() {
		later := time.Now().Add(time.Hour)
		if err := os.Chtimes(path, later, later); err != nil {
			t.Fatal(err)
		}
	})
	_, res, err := plan.Execute(context.Background(), SaveAsFile(filepath.Join(t.TempDir(), "out.flac")))
	refusedAsChanged(t, res, err)
}
