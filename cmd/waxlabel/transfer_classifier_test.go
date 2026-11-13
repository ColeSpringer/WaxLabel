package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// Tag-free destinations for ID3 transfer repros.
var (
	notagsMP3 = filepath.Join("..", "..", "testdata", "notags.mp3")
	notagsWAV = filepath.Join("..", "..", "testdata", "notags.wav")
)

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

// rawFLAC writes a FLAC holding the Vorbis comments as given, which no edit would write so.
func rawFLAC(t *testing.T, comments ...string) string {
	t.Helper()
	return writeTempFile(t, "source.flac", testfile.TaggedFLAC("test", comments...))
}

// strictCopy runs copy --strict --json onto a copy of dstFixture, then diff, and holds the two to
// each other: a lossless report exits 0 and leaves nothing to diff on the keys it carried, a
// lossy one exits 2 and writes nothing.
func strictCopy(t *testing.T, src, dstFixture string, flags ...string) (jsonCopy, string) {
	t.Helper()
	dst := copyFixture(t, dstFixture)
	before, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runCLI(t, append(append([]string{"--json", "copy", "--strict"}, flags...), src, dst)...)
	var jc jsonCopy
	if err := json.Unmarshal([]byte(out), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s\n%s", err, out, stderr)
	}
	lossless := true
	for _, it := range jc.Transfer {
		if it.Disposition == "lossy" || it.Disposition == "dropped" {
			lossless = false
		}
	}
	after, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case lossless && code != 0:
		t.Errorf("copy --strict exit = %d with a lossless report\n%s", code, out)
	case !lossless && code != 2:
		t.Errorf("copy --strict exit = %d with a report that is not lossless, want 2\n%s", code, out)
	case !lossless && !bytes.Equal(before, after):
		t.Error("copy --strict refused and still changed the destination")
	}
	return jc, dst
}

// Custom keys spelled like ID3 frame IDs are stored as user-text frames and read back as given,
// so a copy carries them. One ID3 reads under another name says so.
func TestCopyFrameIDKeysOntoID3(t *testing.T) {
	t.Parallel()
	src := rawFLAC(t, "TITLE=T", "TSRC=USRC17607839", "TORY=1999", "TMED=CD", "TPE1=Someone", "ARTIST=Real")
	jc, dst := strictCopy(t, src, notagsMP3)
	for _, k := range []string{"TSRC", "TORY", "TMED", "TPE1", "ARTIST"} {
		if it := fieldItem(t, jc, k); it.Disposition != "carried" {
			t.Errorf("%s = %s (%s), want carried", k, it.Disposition, it.Reason)
		}
	}
	if out, _, code := runCLI(t, "diff", src, dst); code != 0 {
		t.Errorf("diff after the copy exit = %d\n%s", code, out)
	}

	jc, _ = strictCopy(t, rawFLAC(t, "TCMP=1"), notagsMP3)
	if it := fieldItem(t, jc, "TCMP"); it.Disposition != "lossy" || it.Reason != "stored as COMPILATION" {
		t.Errorf("TCMP = %s (%s), want lossy, stored as COMPILATION", it.Disposition, it.Reason)
	}
	jc, _ = strictCopy(t, rawFLAC(t, "TCMP=1", "COMPILATION=0"), notagsMP3)
	if it := fieldItem(t, jc, "TCMP"); it.Disposition != "dropped" {
		t.Errorf("TCMP beside COMPILATION = %s (%s), want dropped", it.Disposition, it.Reason)
	}
	if it := fieldItem(t, jc, "COMPILATION"); it.Disposition != "carried" {
		t.Errorf("COMPILATION beside a custom TCMP = %s (%s), want carried", it.Disposition, it.Reason)
	}
}

// A second value on a key whose ID3 frame holds one is lossy on every ID3 destination, and
// --strict refuses the copy.
func TestCopySingleSlotID3KeysAreLossy(t *testing.T) {
	t.Parallel()
	src := rawFLAC(t, "DATE=2023", "YEAR=2022", "TRACKNUMBER=3", "TRACKTOTAL=12", "TOTALTRACKS=13",
		"RELEASEDATE=2001", "RELEASEDATE=2002")
	rf64 := filepath.Join("..", "..", "testdata", "sample-rf64.wav")
	aac := filepath.Join("..", "..", "testdata", "notags.aac")
	for _, dst := range []string{notagsMP3, notagsWAV, notagsAIFF, aac, rf64} {
		jc, _ := strictCopy(t, src, dst)
		for _, k := range []string{"RECORDINGDATE", "TRACKTOTAL"} {
			if it := fieldItem(t, jc, k); it.Disposition != "lossy" || it.Reason != "only the first of 2 values is stored" {
				t.Errorf("%s onto %s = %s (%s), want lossy, first value only", k, filepath.Base(dst), it.Disposition, it.Reason)
			}
		}
		if it := fieldItem(t, jc, "TRACKNUMBER"); it.Disposition != "carried" {
			t.Errorf("TRACKNUMBER onto %s = %s, want carried", filepath.Base(dst), it.Disposition)
		}
	}
	// ID3v2.3 has no release date frame, so the two values sit in one user-text frame; v2.4's
	// TDRL holds one.
	jc, _ := strictCopy(t, src, notagsMP3)
	if it := fieldItem(t, jc, "RELEASEDATE"); it.Disposition != "carried" {
		t.Errorf("RELEASEDATE onto an ID3v2.3 tag = %s (%s), want carried", it.Disposition, it.Reason)
	}
	jc, _ = strictCopy(t, src, notagsWAV)
	if it := fieldItem(t, jc, "RELEASEDATE"); it.Disposition != "lossy" {
		t.Errorf("RELEASEDATE onto an ID3v2.4 tag = %s (%s), want lossy", it.Disposition, it.Reason)
	}
}

// --id3-multi slash joins a key's values into one, which a copy grades lossy and an edit warns of.
func TestSlashJoinedValuesAreReported(t *testing.T) {
	t.Parallel()
	src := rawFLAC(t, "ARTIST=AC/DC", "ARTIST=Queen", "TITLE=T")
	jc, _ := strictCopy(t, src, notagsMP3, "--id3-multi", "slash")
	if it := fieldItem(t, jc, "ARTIST"); it.Disposition != "lossy" || it.Reason != `the values are stored joined as "AC/DC / Queen"` {
		t.Errorf("ARTIST = %s (%s), want lossy, joined", it.Disposition, it.Reason)
	}
	if jc, _ := strictCopy(t, src, notagsMP3); fieldItem(t, jc, "ARTIST").Disposition != "carried" {
		t.Error("ARTIST without the slash policy should carry")
	}

	dst := copyFixture(t, notagsMP3)
	out, stderr, code := runCLI(t, "--json", "set", "--id3-multi", "slash", dst, "--set", "ARTIST=AC/DC", "--add", "ARTIST=Queen")
	if code != 0 {
		t.Fatalf("set exit = %d\n%s", code, stderr)
	}
	var reports []jsonReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatalf("set JSON: %v\n%s", err, out)
	}
	if len(reports) != 1 || !slices.ContainsFunc(reports[0].Warnings, func(w jsonWarning) bool {
		return w.Code == "value-dropped" && strings.Contains(w.Message, "ARTIST")
	}) {
		t.Errorf("set --id3-multi slash of two values warns nothing of the join:\n%s", out)
	}
	_, _, code = runCLI(t, "set", "--strict", "--id3-multi", "slash", copyFixture(t, notagsMP3), "--set", "ARTIST=AC/DC", "--add", "ARTIST=Queen")
	if code != 2 {
		t.Errorf("set --strict --id3-multi slash exit = %d, want 2", code)
	}
	// One value is stored as given, and v2.4 separates values natively.
	_, stderr, code = runCLI(t, "set", "--strict", "--id3-multi", "slash", copyFixture(t, notagsMP3), "--set", "ARTIST=AC/DC")
	if code != 0 {
		t.Errorf("set --strict of one value exit = %d\n%s", code, stderr)
	}
}

// Chapter starts finer than a millisecond are cut by the formats that count in milliseconds.
func TestCopyGradesSubMillisecondChapters(t *testing.T) {
	t.Parallel()
	mpc := filepath.Join("..", "..", "testdata", "chapters.mpc")
	for _, dst := range []string{notagsMP3, notagsFLAC} {
		jc, _ := strictCopy(t, mpc, dst)
		var lossy int
		for _, it := range jc.Transfer {
			if it.Kind == "chapter" && it.Disposition == "lossy" {
				lossy += it.Count
				if it.Reason != "a chapter time finer than the 1ms the format counts in is not stored exactly" {
					t.Errorf("chapters onto %s: reason %q", filepath.Base(dst), it.Reason)
				}
			}
		}
		if lossy != 3 {
			t.Errorf("chapters onto %s: %d lossy, want the 3 that start off the millisecond", filepath.Base(dst), lossy)
		}
	}
}

// An empty value an APEv2 item cannot end with is lossy on the APEv2 formats alone.
func TestCopyGradesEmptyValuesOntoAPE(t *testing.T) {
	t.Parallel()
	src := rawFLAC(t, "ARTIST=A", "ARTIST=", "ARTIST=B", "GENRE=X", "GENRE=")
	for _, name := range []string{"notags.wv", "notags.ape", "chapters.mpc"} {
		jc, _ := strictCopy(t, src, filepath.Join("..", "..", "testdata", name))
		if it := fieldItem(t, jc, "ARTIST"); it.Disposition != "carried" {
			t.Errorf("ARTIST onto %s = %s (%s), want carried: an empty value between two is stored", name, it.Disposition, it.Reason)
		}
		if it := fieldItem(t, jc, "GENRE"); it.Disposition != "lossy" {
			t.Errorf("GENRE onto %s = %s (%s), want lossy: the empty value at the end is not stored", name, it.Disposition, it.Reason)
		}
	}
	if jc, _ := strictCopy(t, src, notagsFLAC); fieldItem(t, jc, "GENRE").Disposition != "carried" {
		t.Error("GENRE onto FLAC should carry its empty value")
	}
}

// A value holding a NUL is dropped from the copy and the rest is written; onto a file that
// already holds it, there is nothing to write.
func TestCopyDropsNULValues(t *testing.T) {
	t.Parallel()
	src := rawFLAC(t, "TITLE=abc\x00", "ARTIST=A", "ALBUM=Al")
	dst := copyFixture(t, notagsMP3)
	out, stderr, code := runCLI(t, "--json", "copy", src, dst)
	if code != 0 {
		t.Fatalf("copy exit = %d\n%s\n%s", code, out, stderr)
	}
	var jc jsonCopy
	if err := json.Unmarshal([]byte(out), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s", err, out)
	}
	if it := fieldItem(t, jc, "TITLE"); it.Disposition != "dropped" || it.Reason != "a value contains a NUL byte" {
		t.Errorf("TITLE = %s (%s), want dropped for its NUL", it.Disposition, it.Reason)
	}
	got := dumpJSON(t, dst)
	if len(tagValues(got, "TITLE")) != 0 || !slices.Equal(tagValues(got, "ARTIST"), []string{"A"}) || !slices.Equal(tagValues(got, "ALBUM"), []string{"Al"}) {
		t.Errorf("destination tags = %v, want ARTIST and ALBUM without TITLE", got.Tags)
	}
	strictCopy(t, src, notagsMP3)

	// The file's own NUL is no loss on a copy onto itself, even under --strict.
	self := rawFLAC(t, "TITLE=abc\x00", "ARTIST=A", "ALBUM=Al")
	out, stderr, code = runCLI(t, "--json", "copy", "--strict", self, self)
	if code != 0 {
		t.Fatalf("copy onto itself exit = %d\n%s\n%s", code, out, stderr)
	}
	if err := json.Unmarshal([]byte(out), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s", err, out)
	}
	if !jc.NoOp || jc.Committed {
		t.Errorf("copy onto itself: noOp %v, committed %v, want a no-op", jc.NoOp, jc.Committed)
	}
}

// An edit of a key whose value holds a NUL names the key; an edit of another key goes through.
func TestPlanBesideANULValue(t *testing.T) {
	t.Parallel()
	path := rawFLAC(t, "TITLE=abc\x00", "ARTIST=A")
	if _, stderr, code := runCLI(t, "plan", path, "--add", "ARTIST=second"); code != 0 {
		t.Errorf("plan --add ARTIST exit = %d\n%s", code, stderr)
	}
	_, stderr, code := runCLI(t, "plan", path, "--add", "TITLE=second")
	if code != 4 || !strings.Contains(stderr, `"TITLE"`) || !strings.Contains(stderr, "NUL") {
		t.Errorf("plan --add TITLE exit = %d, want 4 naming TITLE\n%s", code, stderr)
	}
}

// A link picture is dropped from a copy onto an APEv2 format with a reason a person can read.
func TestCopyDropsLinkPictureOntoAPE(t *testing.T) {
	t.Parallel()
	url := "http://example.com/cover.jpg"
	body := slices.Concat(
		testfile.BE32(3), testfile.BE32(3), []byte("-->"), testfile.BE32(0),
		testfile.BE32(1), testfile.BE32(1), testfile.BE32(24), testfile.BE32(0),
		testfile.BE32(len(url)), []byte(url))
	src := writeTempFile(t, "link.flac", testfile.FLAC(testfile.CommentBlock("test", "TITLE=Linked"), testfile.FLACBlock(6, body)))
	jc, dst := strictCopy(t, src, filepath.Join("..", "..", "testdata", "notags.wv"))
	found := false
	for _, it := range jc.Transfer {
		if it.Kind == "picture" {
			found = true
			if it.Disposition != "dropped" || it.Reason != "WavPack cannot store a linked (URL) picture" {
				t.Errorf("picture = %s (%s), want dropped as a link", it.Disposition, it.Reason)
			}
		}
	}
	if !found {
		t.Error("the report has no picture item")
	}
	if got := dumpJSON(t, dst); len(got.Pictures) != 0 {
		t.Errorf("--strict refused, but the destination holds %d pictures", len(got.Pictures))
	}
	dst = copyFixture(t, filepath.Join("..", "..", "testdata", "notags.wv"))
	if out, stderr, code := runCLI(t, "copy", src, dst); code != 0 || !strings.Contains(out, "dropped pictures (1): WavPack cannot store a linked (URL) picture") {
		t.Errorf("copy exit = %d\n%s\n%s", code, out, stderr)
	}
	if got := dumpJSON(t, dst); len(got.Pictures) != 0 || !slices.Equal(tagValues(got, "TITLE"), []string{"Linked"}) {
		t.Errorf("destination: %d pictures, TITLE %v, want the title without a picture", len(got.Pictures), tagValues(got, "TITLE"))
	}
}
