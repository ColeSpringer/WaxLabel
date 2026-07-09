package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// notagsMP3 is a tag-free MP3 destination for the ID3 transfer repro (the other fixtures
// are declared in cli_test.go / transfer_test.go).
var notagsMP3 = filepath.Join("..", "..", "testdata", "notags.mp3")

// buildTransferSource copies fixture to a temp file and applies edit through the library,
// so a test can author a source carrying values (multi-value fields, custom keys) the CLI
// edit surface cannot easily express. It returns the written source path.
func buildTransferSource(t *testing.T, fixture string, edit func(*wl.Editor) *wl.Editor) string {
	t.Helper()
	ctx := context.Background()
	path := copyFixture(t, fixture)
	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		t.Fatalf("parse %s: %v", fixture, err)
	}
	plan, err := edit(doc.Edit()).Prepare()
	if err != nil {
		t.Fatalf("prepare edit on %s: %v", fixture, err)
	}
	if _, _, err := plan.Execute(ctx, wl.SaveBack()); err != nil {
		t.Fatalf("save edit on %s: %v", fixture, err)
	}
	return path
}

// runCopyReport runs a real "copy src dst" and returns the parsed report plus the source
// and written-destination dumps, so a test can check both the report disposition and what
// actually landed in the destination.
func runCopyReport(t *testing.T, src, dstFixture string) (jsonCopy, jsonDocument, jsonDocument) {
	t.Helper()
	dst := copyFixture(t, dstFixture)
	out, _, code := runCLI(t, "--json", "copy", src, dst)
	if code != 0 {
		t.Fatalf("copy %s -> %s exit = %d, want 0\n%s", src, dstFixture, code, out)
	}
	var jc jsonCopy
	if err := json.Unmarshal([]byte(out), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s", err, out)
	}
	return jc, dumpJSON(t, src), dumpJSON(t, dst)
}

// fieldItem returns the transfer report's field item for key.
func fieldItem(t *testing.T, jc jsonCopy, key string) jsonTransferItem {
	t.Helper()
	for _, it := range jc.Transfer {
		if it.Kind == "field" && it.Key == key {
			return it
		}
	}
	t.Fatalf("transfer report has no field item for %q", key)
	return jsonTransferItem{}
}

// assertReportMatchesReality pins the finding's core promise directly: for every field
// item, what the report claims is what the destination actually stored. A carried field
// holds the source's exact values, a dropped field is absent, and a lossy field is present
// but reduced (not the full source values). This is the report-equals-reality property the
// three repros share.
func assertReportMatchesReality(t *testing.T, jc jsonCopy, srcDoc, dstDoc jsonDocument) {
	t.Helper()
	for _, it := range jc.Transfer {
		if it.Kind != "field" {
			continue
		}
		src := tagValues(srcDoc, it.Key)
		dst := tagValues(dstDoc, it.Key)
		switch it.Disposition {
		case "carried":
			if !slices.Equal(dst, src) {
				t.Errorf("%s reported carried but dest = %v, want source %v", it.Key, dst, src)
			}
		case "dropped":
			if len(dst) != 0 {
				t.Errorf("%s reported dropped but dest still has %v", it.Key, dst)
			}
		case "lossy":
			if len(dst) == 0 {
				t.Errorf("%s reported lossy but dest is empty (a lossy value must still be present)", it.Key)
			}
			if slices.Equal(dst, src) {
				t.Errorf("%s reported lossy but dest = source %v (nothing was reduced)", it.Key, dst)
			}
		}
	}
}

// TestCopyMatroskaMultiTitleLossy: a multi-value TITLE copied into Matroska, which homes
// TITLE in the single-valued Info.Title element, is graded lossy (not a clean carry) with
// the first value still written, and the report matches what lands in the destination.
func TestCopyMatroskaMultiTitleLossy(t *testing.T) {
	t.Parallel()
	src := buildTransferSource(t, notagsFLAC, func(e *wl.Editor) *wl.Editor {
		return e.Set(tag.Title, "First", "Second")
	})
	jc, srcDoc, dstDoc := runCopyReport(t, src, notagsMKA)

	title := fieldItem(t, jc, "TITLE")
	if title.Disposition != "lossy" {
		t.Errorf("TITLE disposition = %q, want lossy; reason=%q", title.Disposition, title.Reason)
	}
	if got := tagValues(dstDoc, "TITLE"); len(got) != 1 {
		t.Errorf("dest TITLE = %v, want a single (first) value", got)
	}
	assertReportMatchesReality(t, jc, srcDoc, dstDoc)
}

// TestCopyID3TotalDroppedWhenNumberNonNumeric: a TRACKTOTAL beside a non-numeric
// TRACKNUMBER copied to an ID3-backed destination is graded dropped (ID3 stores a total
// only as the second half of "number/total", which a non-numeric number cannot form),
// while the number itself carries. The destination confirms it: the total is absent, the
// number present.
func TestCopyID3TotalDroppedWhenNumberNonNumeric(t *testing.T) {
	t.Parallel()
	src := buildTransferSource(t, notagsFLAC, func(e *wl.Editor) *wl.Editor {
		return e.Set(tag.TrackNumber, "A1").Set(tag.TrackTotal, "12")
	})
	jc, srcDoc, dstDoc := runCopyReport(t, src, notagsMP3)

	if total := fieldItem(t, jc, "TRACKTOTAL"); total.Disposition != "dropped" {
		t.Errorf("TRACKTOTAL disposition = %q, want dropped; reason=%q", total.Disposition, total.Reason)
	}
	if num := fieldItem(t, jc, "TRACKNUMBER"); num.Disposition != "carried" {
		t.Errorf("TRACKNUMBER disposition = %q, want carried", num.Disposition)
	}
	if got := tagValues(dstDoc, "TRACKTOTAL"); len(got) != 0 {
		t.Errorf("dest TRACKTOTAL = %v, want absent (dropped)", got)
	}
	assertReportMatchesReality(t, jc, srcDoc, dstDoc)
}

// TestCopyVorbisReservedNamespaceDropped: a custom key whose native Vorbis name lands in a
// reserved namespace (CHAPTER050NAME) copied Matroska->FLAC is graded dropped, because a
// Vorbis writer refuses to emit a stray comment a reader would consume as structured
// chapter data. The destination confirms the key is absent.
func TestCopyVorbisReservedNamespaceDropped(t *testing.T) {
	t.Parallel()
	src := buildTransferSource(t, notagsMKA, func(e *wl.Editor) *wl.Editor {
		return e.Set(tag.Key("CHAPTER050NAME"), "Intro")
	})
	jc, srcDoc, dstDoc := runCopyReport(t, src, notagsFLAC)

	if it := fieldItem(t, jc, "CHAPTER050NAME"); it.Disposition != "dropped" {
		t.Errorf("CHAPTER050NAME disposition = %q, want dropped; reason=%q", it.Disposition, it.Reason)
	}
	if got := tagValues(dstDoc, "CHAPTER050NAME"); len(got) != 0 {
		t.Errorf("dest CHAPTER050NAME = %v, want absent (dropped)", got)
	}
	assertReportMatchesReality(t, jc, srcDoc, dstDoc)
}

// TestCopyVorbisOrdinaryKeysCarried is the negative guard for the reserved-namespace
// classifier: an ordinary custom key (MYKEY) and a normal canonical key (ARTIST) copied
// Matroska->FLAC must stay carried, since neither native Vorbis name is reserved. This
// pins the VorbisName direction so the classifier cannot over-drop a legitimate field.
func TestCopyVorbisOrdinaryKeysCarried(t *testing.T) {
	t.Parallel()
	src := buildTransferSource(t, notagsMKA, func(e *wl.Editor) *wl.Editor {
		return e.Set(tag.Key("MYKEY"), "val").Set(tag.Artist, "me")
	})
	jc, srcDoc, dstDoc := runCopyReport(t, src, notagsFLAC)

	for _, key := range []string{"MYKEY", "ARTIST"} {
		if it := fieldItem(t, jc, key); it.Disposition != "carried" {
			t.Errorf("%s disposition = %q, want carried", key, it.Disposition)
		}
	}
	assertReportMatchesReality(t, jc, srcDoc, dstDoc)
}
