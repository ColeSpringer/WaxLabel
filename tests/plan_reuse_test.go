package waxlabel_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// TestPlanReuseGuardSaveAsFileInPlace checks that SaveAsFile to the source path spends
// the plan just like SaveBack. Later Execute calls would read bytes that no longer match
// the planned segments, so they are refused.
func TestPlanReuseGuardSaveAsFileInPlace(t *testing.T) {
	ctx := context.Background()
	work := copyToTemp(t, sampleFLAC)
	doc := mustParseFile(t, work)
	plan, err := doc.Edit().Set(tag.Title, "InPlace").Prepare()
	if err != nil {
		t.Fatal(err)
	}

	if _, res, err := plan.Execute(ctx, wl.SaveAsFile(work)); err != nil || !res.Committed {
		t.Fatalf("SaveAsFile in place: err=%v committed=%v", err, res.Committed)
	}

	other := filepath.Join(t.TempDir(), "other.flac")
	_, _, err = plan.Execute(ctx, wl.SaveAsFile(other))
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Fatalf("second Execute after in-place SaveAsFile: err=%v, want refusal", err)
	}
	if _, statErr := os.Stat(other); statErr == nil {
		t.Error("a refused Execute still wrote an output file")
	}
}

// TestPlanReuseSaveAsFileOtherPathsValid checks that repeated SaveAsFile runs to other
// paths stay valid because the source bytes remain stable.
func TestPlanReuseSaveAsFileOtherPathsValid(t *testing.T) {
	ctx := context.Background()
	doc := mustParseFile(t, sampleFLAC) // the read-only fixture is the stable source
	plan, err := doc.Edit().Set(tag.Title, "Multi").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"a.flac", "b.flac"} {
		out := filepath.Join(dir, name)
		if _, res, err := plan.Execute(ctx, wl.SaveAsFile(out)); err != nil || !res.Committed {
			t.Fatalf("SaveAsFile %s: err=%v committed=%v", name, err, res.Committed)
		}
		if _, err := wl.ParseFile(ctx, out); err != nil {
			t.Errorf("output %s does not parse: %v", name, err)
		}
	}
}

// TestPlanReuseHardlinkAliasStaysSafe checks that writing to a hardlink alias does not
// spend the plan. The atomic rename replaces the alias directory entry while leaving the
// source path's bytes intact.
func TestPlanReuseHardlinkAliasStaysSafe(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.flac")
	data := readFixture(t, sampleFLAC)
	if err := os.WriteFile(source, data, 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.flac")
	if err := os.Link(source, alias); err != nil {
		t.Skipf("hardlinks unsupported here: %v", err)
	}

	doc := mustParseFile(t, source)
	plan, err := doc.Edit().Set(tag.Title, "Hardlink").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, res, err := plan.Execute(ctx, wl.SaveAsFile(alias)); err != nil || !res.Committed {
		t.Fatalf("SaveAsFile to hardlink alias: err=%v committed=%v", err, res.Committed)
	}

	// Guard must be unarmed: a further write to another path still works.
	if _, _, err := plan.Execute(ctx, wl.SaveAsFile(filepath.Join(dir, "other.flac"))); err != nil {
		t.Fatalf("plan should remain valid after a hardlink-alias write: %v", err)
	}
	// The rename broke the link: the source keeps its original bytes, the alias does not.
	if got, _ := os.ReadFile(source); !bytes.Equal(got, data) {
		t.Error("source bytes changed - a hardlink-alias write should leave them intact")
	}
	if got, _ := os.ReadFile(alias); bytes.Equal(got, data) {
		t.Error("alias was not rewritten")
	}
}

// TestPlanReuseSymlinkToSourceIsGuarded checks that writing through a symlink to the
// source spends the plan because the real source file is rewritten.
func TestPlanReuseSymlinkToSourceIsGuarded(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.flac")
	if err := os.WriteFile(source, readFixture(t, sampleFLAC), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.flac")
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	doc := mustParseFile(t, source)
	plan, err := doc.Edit().Set(tag.Title, "ViaSymlink").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, res, err := plan.Execute(ctx, wl.SaveAsFile(link)); err != nil || !res.Committed {
		t.Fatalf("SaveAsFile via symlink: err=%v committed=%v", err, res.Committed)
	}
	other := filepath.Join(dir, "other.flac")
	if _, _, err := plan.Execute(ctx, wl.SaveAsFile(other)); !errors.Is(err, waxerr.ErrInvalidData) {
		t.Fatalf("plan should be spent after writing through a symlink to the source: err=%v", err)
	}
}

// tamperFlip changes one byte of marker (which must sit in the metadata region) while
// preserving the file's size, mtime, and inode, so only the structural fingerprint differs
// from what the prior parse recorded - driving the fingerprint branch of change detection
// specifically (size/mtime/inode all match).
func tamperFlip(t *testing.T, path, marker string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(data), marker)
	if i < 0 {
		t.Fatalf("marker %q not found in the metadata region of %s", marker, path)
	}
	data[i] ^= 0xFF // same length, so size is preserved; in-place write keeps the inode
	if err := os.WriteFile(path, data, info.Mode()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
}

// TestSaveReturnedDocCarriesFingerprint checks that a Document returned from a write keeps
// the structural fingerprint, so later save-back change detection catches metadata tamper
// even when size, mtime, and inode are preserved.
func TestSaveReturnedDocCarriesFingerprint(t *testing.T) {
	ctx := context.Background()

	// The Document returned by SaveBack should refuse the tamper.
	t.Run("returned doc", func(t *testing.T) {
		work := copyToTemp(t, sampleFLAC)
		doc := mustParseFile(t, work)
		plan, _ := doc.Edit().Set(tag.Title, "PriorWrite").Prepare()
		resDoc, res, err := plan.Execute(ctx, wl.SaveBack())
		if err != nil || !res.Committed {
			t.Fatalf("prior SaveBack: err=%v committed=%v", err, res.Committed)
		}
		plan2, err := resDoc.Edit().Set(tag.Title, "Reedit").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		tamperFlip(t, work, "PriorWrite")
		_, _, err = plan2.Execute(ctx, wl.SaveBack())
		if !errors.Is(err, waxerr.ErrSourceChanged) || !strings.Contains(err.Error(), "fingerprint") {
			t.Fatalf("re-edit SaveBack after tamper: err=%v, want ErrSourceChanged citing the fingerprint", err)
		}
	})

	// Control: a freshly parsed doc refuses the same tamper identically.
	t.Run("fresh ParseFile", func(t *testing.T) {
		work := copyToTemp(t, sampleFLAC)
		seed := mustParseFile(t, work)
		sp, _ := seed.Edit().Set(tag.Title, "PriorWrite").Prepare()
		if _, _, err := sp.Execute(ctx, wl.SaveBack()); err != nil {
			t.Fatal(err)
		}
		doc := mustParseFile(t, work)
		plan, err := doc.Edit().Set(tag.Title, "Reedit").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		tamperFlip(t, work, "PriorWrite")
		_, _, err = plan.Execute(ctx, wl.SaveBack())
		if !errors.Is(err, waxerr.ErrSourceChanged) || !strings.Contains(err.Error(), "fingerprint") {
			t.Fatalf("fresh-parse SaveBack after tamper: err=%v, want ErrSourceChanged citing the fingerprint", err)
		}
	})
}

// appendByte grows a file by one byte, changing its size (and mtime). It drives the
// size branch of change detection, the same tamper TestSourceChangedDetected uses.
func appendByte(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, 0), 0o644); err != nil {
		t.Fatal(err)
	}
}

// changedSourceCase is one way a source can change under a parsed document, plus the substring
// the ErrSourceChanged reason must cite. TestSaveAsFileGuardsChangedSource and
// TestWriteToGuardsChangedSource share it so both write paths exercise the same size change
// (the inode/size branch) and fingerprint-only change (same size/mtime/inode, so the
// fingerprint branch fires).
type changedSourceCase struct {
	name   string
	tamper func(t *testing.T, path string)
	reason string
}

func changedSourceCases() []changedSourceCase {
	return []changedSourceCase{
		{"size change", appendByte, "size changed"},
		{"fingerprint only", func(t *testing.T, path string) { tamperFlip(t, path, "Original Title") }, "fingerprint"},
	}
}

// TestSaveAsFileGuardsChangedSource checks the F1 guard on SaveAsFile: a ParseFile source
// that changed on disk since parse is refused with ErrSourceChanged, so the stale byte
// offsets never copy the wrong bytes (and an in-place target is never silently corrupted).
// Both a size change and a fingerprint-only change are caught, on an in-place target and another path.
func TestSaveAsFileGuardsChangedSource(t *testing.T) {
	ctx := context.Background()
	for _, tc := range changedSourceCases() {
		t.Run(tc.name+"/other path", func(t *testing.T) {
			work := copyToTemp(t, sampleFLAC)
			plan, err := mustParseFile(t, work).Edit().Set(tag.Title, "Changed").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			tc.tamper(t, work)
			other := filepath.Join(t.TempDir(), "out.flac")
			_, _, err = plan.Execute(ctx, wl.SaveAsFile(other))
			if !errors.Is(err, waxerr.ErrSourceChanged) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("SaveAsFile(other) after %s: err=%v, want ErrSourceChanged citing %q", tc.name, err, tc.reason)
			}
			if _, statErr := os.Stat(other); statErr == nil {
				t.Error("a refused SaveAsFile still wrote an output file")
			}
		})
		t.Run(tc.name+"/in place", func(t *testing.T) {
			work := copyToTemp(t, sampleFLAC)
			plan, err := mustParseFile(t, work).Edit().Set(tag.Title, "Changed").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			tc.tamper(t, work)
			if _, _, err := plan.Execute(ctx, wl.SaveAsFile(work)); !errors.Is(err, waxerr.ErrSourceChanged) {
				t.Fatalf("SaveAsFile(source) after %s: err=%v, want ErrSourceChanged", tc.name, err)
			}
		})
	}
}

// TestWriteToGuardsChangedSource checks the F1 guard on WriteTo(w, nil): a ParseFile source
// that changed on disk is refused before any bytes are streamed. A streaming writer never
// clobbers the source, so this is a derived write - the precise inode+size+fingerprint check.
func TestWriteToGuardsChangedSource(t *testing.T) {
	ctx := context.Background()
	for _, tc := range changedSourceCases() {
		t.Run(tc.name, func(t *testing.T) {
			work := copyToTemp(t, sampleFLAC)
			plan, err := mustParseFile(t, work).Edit().Set(tag.Title, "Changed").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			tc.tamper(t, work)
			var buf bytes.Buffer
			_, _, err = plan.Execute(ctx, wl.WriteTo(&buf, nil))
			if !errors.Is(err, waxerr.ErrSourceChanged) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("WriteTo(w, nil) after %s: err=%v, want ErrSourceChanged citing %q", tc.name, err, tc.reason)
			}
			if buf.Len() != 0 {
				t.Errorf("a refused WriteTo still streamed %d bytes", buf.Len())
			}
		})
	}
}

// TestDerivedWriteUnchangedSourceSucceeds is the happy path that actually enters the F1
// guard and passes: a ParseFile document whose source is unchanged writes cleanly via both
// SaveAsFile(otherPath) and WriteTo(w, nil). Without it, an always-fire regression in the
// guard would slip past the change-detection tests, which never reach a passing guard.
func TestDerivedWriteUnchangedSourceSucceeds(t *testing.T) {
	ctx := context.Background()
	work := copyToTemp(t, sampleFLAC)
	plan, err := mustParseFile(t, work).Edit().Set(tag.Title, "Fresh").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "out.flac")
	if _, res, err := plan.Execute(ctx, wl.SaveAsFile(other)); err != nil || !res.Committed {
		t.Fatalf("SaveAsFile(other) on unchanged source: err=%v committed=%v", err, res.Committed)
	}
	if _, err := wl.ParseFile(ctx, other); err != nil {
		t.Errorf("SaveAsFile output does not parse: %v", err)
	}
	var buf bytes.Buffer
	if _, res, err := plan.Execute(ctx, wl.WriteTo(&buf, nil)); err != nil || !res.Committed {
		t.Fatalf("WriteTo(w, nil) on unchanged source: err=%v committed=%v", err, res.Committed)
	}
	if buf.Len() == 0 {
		t.Error("WriteTo streamed no bytes")
	}
}

// TestDerivedWriteIgnoresMtimeTouch pins the precise same-path/derived asymmetry F1
// introduces. Bumping only the source's mtime (bytes identical) must NOT block a derived
// write: a moved audio region always changes size and/or the fingerprint, so mtime says
// nothing about whether the planned offsets are still valid. The same touch DOES block an
// in-place write, which stays conservative about clobbering the source.
func TestDerivedWriteIgnoresMtimeTouch(t *testing.T) {
	ctx := context.Background()
	touch := func(t *testing.T, path string) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		future := info.ModTime().Add(time.Hour)
		if err := os.Chtimes(path, future, future); err != nil {
			t.Fatal(err)
		}
	}
	newTouchedPlan := func(t *testing.T) (*wl.Plan, string) {
		t.Helper()
		work := copyToTemp(t, sampleFLAC)
		plan, err := mustParseFile(t, work).Edit().Set(tag.Title, "Touched").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		touch(t, work)
		return plan, work
	}

	t.Run("SaveAsFile other path succeeds", func(t *testing.T) {
		plan, _ := newTouchedPlan(t)
		other := filepath.Join(t.TempDir(), "out.flac")
		if _, res, err := plan.Execute(ctx, wl.SaveAsFile(other)); err != nil || !res.Committed {
			t.Fatalf("SaveAsFile(other) after mtime touch: err=%v committed=%v", err, res.Committed)
		}
	})
	t.Run("WriteTo succeeds", func(t *testing.T) {
		plan, _ := newTouchedPlan(t)
		var buf bytes.Buffer
		if _, res, err := plan.Execute(ctx, wl.WriteTo(&buf, nil)); err != nil || !res.Committed {
			t.Fatalf("WriteTo(w, nil) after mtime touch: err=%v committed=%v", err, res.Committed)
		}
	})
	t.Run("SaveBack fails", func(t *testing.T) {
		plan, _ := newTouchedPlan(t)
		if _, _, err := plan.Execute(ctx, wl.SaveBack()); !errors.Is(err, waxerr.ErrSourceChanged) {
			t.Fatalf("SaveBack after mtime touch: err=%v, want ErrSourceChanged", err)
		}
	})
	t.Run("SaveAsFile in place fails", func(t *testing.T) {
		plan, work := newTouchedPlan(t)
		if _, _, err := plan.Execute(ctx, wl.SaveAsFile(work)); !errors.Is(err, waxerr.ErrSourceChanged) {
			t.Fatalf("SaveAsFile(source) after mtime touch: err=%v, want ErrSourceChanged", err)
		}
	})
}

// TestGuardBypassedForStableSources checks the F1 escape hatches: when the write copies from
// bytes that cannot go stale, the guard does not run even if a file on disk changed. An
// explicit WriteTo(w, source) uses caller-supplied bytes; an OpenSource document holds its
// bytes in memory.
func TestGuardBypassedForStableSources(t *testing.T) {
	ctx := context.Background()

	t.Run("WriteTo explicit source", func(t *testing.T) {
		data := readFixture(t, sampleFLAC)
		work := copyToTemp(t, sampleFLAC)
		plan, err := mustParseFile(t, work).Edit().Set(tag.Title, "Explicit").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		// Change the on-disk source; the explicit byte source is what gets copied.
		if err := os.WriteFile(work, append(append([]byte(nil), data...), 0), 0o644); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, res, err := plan.Execute(ctx, wl.WriteTo(&buf, wl.BytesSource(data))); err != nil || !res.Committed {
			t.Fatalf("WriteTo(w, explicitSource) over a changed on-disk file: err=%v committed=%v", err, res.Committed)
		}
		if buf.Len() == 0 {
			t.Error("WriteTo streamed no bytes")
		}
	})

	t.Run("OpenSource document", func(t *testing.T) {
		data := readFixture(t, sampleFLAC)
		onDisk := writeTempFile(t, "src.flac", data)
		f, err := os.Open(onDisk)
		if err != nil {
			t.Fatal(err)
		}
		src, err := wl.OpenSource(ctx, f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		// Change the file the stream was read from; OpenSource already teed the bytes.
		if err := os.WriteFile(onDisk, append(append([]byte(nil), data...), 0), 0o644); err != nil {
			t.Fatal(err)
		}
		plan, err := src.Document().Edit().Set(tag.Title, "InMemory").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "out.flac")
		if _, res, err := plan.Execute(ctx, wl.SaveAsFile(out)); err != nil || !res.Committed {
			t.Fatalf("SaveAsFile on an OpenSource document: err=%v committed=%v", err, res.Committed)
		}
	})
}
