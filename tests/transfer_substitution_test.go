package waxlabel_test

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

const fffd = string(utf8.RuneError)

type transferGrade struct{ carried, lossy, dropped int }

// transferGrades counts a report's items by disposition under each key, "chapters" and "lyrics".
func transferGrades(r wl.TransferReport) map[string]transferGrade {
	out := map[string]transferGrade{}
	for _, it := range r.Items {
		name := string(it.Key)
		switch it.Kind {
		case wl.TransferChapter:
			name = "chapters"
		case wl.TransferSyncedLyric:
			name = "lyrics"
		}
		g := out[name]
		switch it.Disposition {
		case wl.Carried:
			g.carried += it.Count
		case wl.Lossy:
			g.lossy += it.Count
		case wl.Dropped:
			g.dropped += it.Count
		}
		out[name] = g
	}
	return out
}

func checkTransferGrades(t *testing.T, r wl.TransferReport, want map[string]transferGrade) {
	t.Helper()
	got := transferGrades(r)
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s graded %+v, want %+v (%+v)", name, got[name], w, r.Items)
		}
	}
}

// Text the source read with U+FFFD in place of bytes that are not UTF-8 is not the source's
// text once copied: the copy is lossy, onto a file that holds the substitution already too, and
// carried only onto a file that holds the same bytes, as the source itself does.
func TestTransferOfTextReadWithASubstitutionIsLossy(t *testing.T) {
	srcBytes := flacWithComments("TITLE=caf\xe9", "ALBUM=A",
		"CHAPTER001=00:00:00.000", "CHAPTER001NAME=Intr\xf6",
		"CHAPTER002=00:00:00.500", "CHAPTER002NAME=Verse",
		"SYNCEDLYRICS=[00:00.10]caf\xe9")
	src := mustParseBytes(t, srcBytes)
	lossy := map[string]transferGrade{"TITLE": {lossy: 1}, "ALBUM": {carried: 1}, "chapters": {carried: 1, lossy: 1}, "lyrics": {lossy: 1}}
	named := func(t *testing.T, r wl.TransferReport) {
		t.Helper()
		for _, it := range r.Items {
			if it.Disposition == wl.Lossy && !strings.Contains(it.Reason, "U+FFFD") {
				t.Errorf("lossy for %q, want the substitution named", it.Reason)
			}
		}
	}

	t.Run("a format alone", func(t *testing.T) {
		for _, f := range []wl.Format{wl.FormatFLAC, wl.FormatMP3} {
			r, err := src.PlanTransfer(f)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			checkTransferGrades(t, r, lossy)
			named(t, r)
		}
	})

	dstBytes := readFixture(t, "../testdata/notags.flac")
	plan, report, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatal(err)
	}
	checkTransferGrades(t, report, lossy)
	written := applyToBytes(t, dstBytes, plan)
	if !bytes.Contains(written, []byte("TITLE=caf"+fffd)) {
		t.Fatal("the copy did not store the substitution")
	}

	t.Run("a destination holding the substitution", func(t *testing.T) {
		plan, report, err := src.PrepareTransfer(mustParseBytes(t, written))
		if err != nil {
			t.Fatal(err)
		}
		checkTransferGrades(t, report, lossy)
		named(t, report)
		for _, it := range report.Items {
			if it.Disposition == wl.Lossy && !strings.Contains(it.Reason, "other bytes") {
				t.Errorf("%q, want a reason that says nothing is stored", it.Reason)
			}
		}
		if !plan.IsNoOp() {
			t.Errorf("the repeat writes: %s", plan)
		}
	})

	t.Run("the source itself", func(t *testing.T) {
		plan, report, err := src.PrepareTransfer(mustParseBytes(t, srcBytes))
		if err != nil {
			t.Fatal(err)
		}
		checkTransferGrades(t, report, map[string]transferGrade{"TITLE": {carried: 1}, "ALBUM": {carried: 1}, "chapters": {carried: 2}, "lyrics": {carried: 1}})
		if !plan.IsNoOp() {
			t.Errorf("a copy onto the same bytes writes: %s", plan)
		}
	})

	// A file that holds U+FFFD as its text reads with no substitution, so its copy is carried.
	t.Run("a source holding U+FFFD", func(t *testing.T) {
		plan, report, err := mustParseBytes(t, written).PrepareTransfer(mustParseBytes(t, dstBytes))
		if err != nil {
			t.Fatal(err)
		}
		checkTransferGrades(t, report, map[string]transferGrade{"TITLE": {carried: 1}, "chapters": {carried: 2}, "lyrics": {carried: 1}})
		if plan.IsNoOp() {
			t.Error("nothing written")
		}
	})
}

// What was read with a substitution is told by the bytes each file stores, not by a warning
// beside a U+FFFD: only that text is lossy, a destination holds it only with the same bytes,
// whichever of the two stores the invalid ones, and one that holds the text another way is
// graded and left alone.
func TestTransferSubstitutionIsToldByTheStoredBytes(t *testing.T) {
	chapter := func(title string) []string {
		return []string{"CHAPTER001=00:00:00.000", "CHAPTER001NAME=" + title}
	}
	flac := func(entries ...string) []byte { return flacWithComments(entries...) }
	for _, c := range []struct {
		name     string
		src, dst []byte
		want     map[string]transferGrade
	}{
		{"a title holding U+FFFD beside invalid lyrics",
			flac(append(chapter("Real "+fffd+" title"), "SYNCEDLYRICS=[00:00.10]caf\xe9")...), nil,
			map[string]transferGrade{"chapters": {carried: 1}, "lyrics": {lossy: 1}}},
		// No lyric line is read from a header.
		{"invalid bytes in an LRC header alone",
			flac("SYNCEDLYRICS=[ar:Caf\xe9]\n[00:00.50]hello"), nil,
			map[string]transferGrade{"lyrics": {carried: 1}}},
		{"a destination holding the U+FFFD beside invalid text of its own",
			flac(chapter("Intr\xf6")...), flac(append(chapter("Intr"+fffd), "SYNCEDLYRICS=[00:00.10]x\xe9")...),
			map[string]transferGrade{"chapters": {lossy: 1}}},
		{"other invalid bytes that read alike",
			flac("TITLE=caf\xe9"), flac("TITLE=caf\xe8"),
			map[string]transferGrade{"TITLE": {lossy: 1}}},
		{"U+FFFD onto the bytes that read as it",
			flac("TITLE=caf" + fffd), flac("TITLE=caf\xe9"),
			map[string]transferGrade{"TITLE": {lossy: 1}}},
		// Naming the key would settle the destination's other store for it.
		{"a destination with a second item under the key",
			flac("TITLE=caf\xe9"), testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "caf"+fffd), mp4Freeform("com.apple.iTunes", "TITLE", "Other")),
			map[string]transferGrade{"TITLE": {lossy: 1}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := mustParseBytes(t, c.src)
			if c.dst == nil {
				r, err := src.PlanTransfer(wl.FormatFLAC)
				if err != nil {
					t.Fatal(err)
				}
				checkTransferGrades(t, r, c.want)
				return
			}
			plan, report, err := src.PrepareTransfer(mustParseBytes(t, c.dst))
			if err != nil {
				t.Fatal(err)
			}
			checkTransferGrades(t, report, c.want)
			if !plan.IsNoOp() {
				t.Errorf("the destination reads the same text and is written: %s", plan)
			}
		})
	}

	// Chapters and lyrics the caller gives in place of the source's are the caller's text.
	t.Run("a replacement", func(t *testing.T) {
		src := mustParseBytes(t, flac(append(chapter("Intr\xf6"), "SYNCEDLYRICS=[00:00.10]caf\xe9")...))
		r, err := src.Transfer().
			SetChapters(wl.Chapter{Title: "Mine " + fffd}).
			SetSyncedLyrics(wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: time.Second, Text: "mine " + fffd}}}).
			Plan(wl.FormatFLAC)
		if err != nil {
			t.Fatal(err)
		}
		checkTransferGrades(t, r, map[string]transferGrade{"chapters": {carried: 1}, "lyrics": {carried: 1}})
	})

	// The format's own word on a stored field outranks the substitution.
	t.Run("a key the format stores under another name", func(t *testing.T) {
		r, err := mustParseBytes(t, flac("MUSICBRAINZ ALBUM ID=v\xe9")).PlanTransfer(wl.FormatMP4)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Items) != 1 || r.Items[0].Disposition != wl.Lossy || !strings.Contains(r.Items[0].Reason, "stored as") {
			t.Errorf("items = %+v, want lossy for the other name", r.Items)
		}
	})
}

// An ID3 frame and a Matroska tag or title show U+FFFD for bytes that are no text too: they
// report it, by key, and a copy of the value is lossy unless the destination stores the bytes.
func TestTransferOfSubstitutedTextFromID3AndMatroska(t *testing.T) {
	for name, data := range map[string][]byte{
		"an ID3 frame in UTF-8": slices.Concat(testfile.ID3v2(4,
			testfile.ID3Frame(4, "TIT2", []byte("\x03caf\xe9")), testfile.ID3Frame(4, "TPE1", []byte("\x03ok"))),
			readFixture(t, notagsMP3)),
		"a Matroska tag": testfile.Matroska("matroska", "", testfile.El(testfile.IDTags,
			testfile.TagGroup(0, testfile.SimpleTag("TITLE", "caf\xe9"), testfile.SimpleTag("ARTIST", "ok")))),
		"a Matroska title": testfile.Matroska("matroska", "caf\xe9", testfile.El(testfile.IDTags,
			testfile.TagGroup(0, testfile.SimpleTag("ARTIST", "ok")))),
	} {
		t.Run(name, func(t *testing.T) {
			doc := mustParseBytes(t, data)
			if got := doc.Fields().Title; got != "caf"+fffd {
				t.Fatalf("TITLE = %q, want the substitution", got)
			}
			keyed := false
			for _, w := range doc.Warnings() {
				keyed = keyed || w.Code == wl.WarnInvalidText && slices.Equal(w.Keys, []tag.Key{tag.Title})
			}
			if !keyed || !lintHasCode(doc, "invalid-text") {
				t.Errorf("no invalid-text warning keyed TITLE, or none in lint: %v", doc.Warnings())
			}
			r, err := doc.PlanTransfer(wl.FormatFLAC)
			if err != nil {
				t.Fatal(err)
			}
			checkTransferGrades(t, r, map[string]transferGrade{"TITLE": {lossy: 1}, "ARTIST": {carried: 1}})
			plan, report, err := doc.PrepareTransfer(mustParseBytes(t, data))
			if err != nil {
				t.Fatal(err)
			}
			checkTransferGrades(t, report, map[string]transferGrade{"TITLE": {carried: 1}, "ARTIST": {carried: 1}})
			if !plan.IsNoOp() {
				t.Errorf("a copy onto the same bytes writes: %s", plan)
			}
		})
	}
}

// Grading a copy looks each key up once, however many were read with a substitution.
func TestTransferGradesSubstitutedKeysInOnePass(t *testing.T) {
	const n = 400_000
	entries := make([]string, n)
	for i := range entries {
		entries[i] = fmt.Sprintf("K%06d=caf\xe9", i)
	}
	doc, err := wl.Parse(context.Background(), wl.BytesSource(flacWithComments(entries...)), wl.WithLimits(wl.Limits{MaxElements: 2 * n}))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r, err := doc.PlanTransfer(wl.FormatFLAC)
	if err != nil {
		t.Fatal(err)
	}
	// A search through the keys for each of them takes minutes at this size.
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("grading %d keys took %s", n, d)
	}
	if _, lossy, _ := r.Counts(); lossy != n {
		t.Errorf("%d lossy, want %d", lossy, n)
	}
}
