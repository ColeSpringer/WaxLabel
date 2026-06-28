package waxlabel_test

import (
	"bytes"
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

// TestSaveReturnedDocCarriesFingerprint checks that a Document returned from a write keeps
// the structural fingerprint, so later save-back change detection catches metadata tamper
// even when size, mtime, and inode are preserved.
func TestSaveReturnedDocCarriesFingerprint(t *testing.T) {
	ctx := context.Background()

	// tamperFlip changes one byte of marker (which must sit in the metadata region)
	// while preserving the file's size, mtime, and inode, so only the structural
	// fingerprint differs from what the prior parse recorded.
	tamperFlip := func(t *testing.T, path, marker string) {
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
