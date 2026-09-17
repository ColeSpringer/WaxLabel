package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// notagsMP3: tag-free MP3 destination for ID3 transfer repro.
var notagsMP3 = filepath.Join("..", "..", "testdata", "notags.mp3")

// buildTransferSource copies fixture, applies library edit, returns source path.
// Authors values the CLI edit surface cannot easily express.
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

// runCopyReport runs copy; returns report plus source and destination dumps.
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

// fieldItem returns transfer report field item for key.
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

// assertReportMatchesReality: carried = exact source values; dropped = absent; lossy = present but reduced.
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

// TestCopyMatroskaMultiTitleLossy: multi-value TITLE -> Matroska Info.Title grades lossy; first value kept.
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

// TestCopyID3TotalDroppedWhenNumberNonNumeric: non-numeric TRACKNUMBER drops TRACKTOTAL on ID3 dest.
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

// TestCopyVorbisReservedNamespaceDropped: CHAPTER050NAME in reserved Vorbis namespace grades dropped.
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

// TestCopyVorbisOrdinaryKeysCarried: MYKEY and ARTIST stay carried (negative guard for reserved classifier).
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

// TestCopyNumericGenreToID3IsLossy: bare numeric GENRE on ID3 reads back as name (lossy); named carries;
// MP4 stores literal text.
func TestCopyNumericGenreToID3IsLossy(t *testing.T) {
	lossyGenre := regexp.MustCompile(`(?m)^  lossy\s+GENRE:`)
	src := buildTransferSource(t, notagsFLAC, func(e *wl.Editor) *wl.Editor { return e.Set(tag.Genre, "17") })

	dst := copyFixture(t, notagsMP3)
	stdout, stderr, code := runCLI(t, "copy", src, dst)
	if code != 0 {
		t.Fatalf("copy to mp3 failed: %s", stderr)
	}
	if !lossyGenre.MatchString(stdout) {
		t.Errorf("copy of GENRE=17 to mp3 reported no lossy GENRE line:\n%s", stdout)
	}

	m4a := copyFixture(t, notagsM4A)
	stdout, stderr, code = runCLI(t, "copy", src, m4a)
	if code != 0 {
		t.Fatalf("copy to m4a failed: %s", stderr)
	}
	if lossyGenre.MatchString(stdout) {
		t.Errorf("copy of GENRE=17 to m4a graded lossy; the literal text is stored:\n%s", stdout)
	}

	named := buildTransferSource(t, notagsFLAC, func(e *wl.Editor) *wl.Editor { return e.Set(tag.Genre, "Rock") })
	dst2 := copyFixture(t, notagsMP3)
	stdout, stderr, code = runCLI(t, "copy", named, dst2)
	if code != 0 {
		t.Fatalf("copy of a named genre failed: %s", stderr)
	}
	if lossyGenre.MatchString(stdout) {
		t.Errorf("copy of GENRE=Rock to mp3 graded lossy:\n%s", stdout)
	}
}
