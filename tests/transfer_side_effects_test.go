package waxlabel_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// An item the destination stores nothing of is not written at all, so a key the destination
// holds and the source does not is left as it was: a custom key that would only read back as a
// second value of the destination's own or take its place, a slashed number whose total the
// destination already states.
func TestTransferLeavesTheDestinationsOwnKeysAlone(t *testing.T) {
	for _, c := range []struct {
		name, dst string
		held      [2]string
		src       []string
		dropped   tag.Key
	}{
		{"a key ID3 reads as COMPILATION onto a file holding one", notagsMP3, [2]string{"COMPILATION", "0"}, []string{"TCMP=1"}, "TCMP"},
		{"a name ID3 reads as another key onto a file holding it", notagsMP3, [2]string{"MUSICBRAINZ_ALBUMID", "zzz"},
			[]string{"MUSICBRAINZ ALBUM ID=abc"}, "MUSICBRAINZ ALBUM ID"},
		{"a name MP4 reads as another key onto a file holding it", "../testdata/notags.m4a", [2]string{"MUSICBRAINZ_ALBUMID", "zzz"},
			[]string{"MUSICBRAINZ ALBUM ID=abc"}, "MUSICBRAINZ ALBUM ID"},
		{"a slashed number onto a Matroska file holding a total", "../testdata/notags.mka", [2]string{"TRACKTOTAL", "99"},
			[]string{"TRACKNUMBER=3/12", "TRACKNUMBER=4"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			held := tag.Key(c.held[0])
			dstBytes := writeBack(t, c.dst, func(e *wl.Editor) { e.Set(held, c.held[1]) })
			src := mustParseBytes(t, testfile.TaggedFLAC("v", c.src...))
			dst := mustParseBytes(t, dstBytes)
			plan, report, err := src.PrepareTransfer(dst)
			if err != nil {
				t.Fatal(err)
			}
			after := dst
			if !plan.IsNoOp() {
				after = mustParseBytes(t, applyToBytes(t, dstBytes, plan))
			}
			if got, _ := after.Get(held); !slices.Equal(got, []string{c.held[1]}) {
				t.Errorf("%s = %q after the copy, want the destination's own %q", held, got, c.held[1])
			}
			if c.dropped == "" {
				return
			}
			if it := transferItemFor(t, report, c.dropped); it.Disposition != wl.Dropped {
				t.Errorf("%s = %s (%s), want dropped", c.dropped, it.Disposition, it.Reason)
			}
			if !plan.IsNoOp() {
				t.Errorf("a copy that stores nothing plans a write: %+v", plan.Report().Operations)
			}
		})
	}
}

// Synced lyric sets are graded each by its own fate: a set left out for text no format stores
// does not cost the sets beside it their grade or their place under a format's limit, and a
// language the frame cuts is lossy.
func TestTransferGradesEachSyncedLyricsSet(t *testing.T) {
	set := func(language, text string) wl.SyncedLyrics {
		return wl.SyncedLyrics{Language: language, Lines: []wl.SyncedLine{{Time: time.Second, Text: text}}}
	}
	src := mustParseBytes(t, testfile.TaggedFLAC("v", "TITLE=T"))
	count := func(r wl.TransferReport) (carried, lossy, dropped int) {
		for _, it := range r.Items {
			if it.Kind != wl.TransferSyncedLyric {
				continue
			}
			switch it.Disposition {
			case wl.Carried:
				carried += it.Count
			case wl.Lossy:
				lossy += it.Count
			case wl.Dropped:
				dropped += it.Count
			}
		}
		return carried, lossy, dropped
	}
	for _, c := range []struct {
		name, dst               string
		sets                    []wl.SyncedLyrics
		carried, lossy, dropped int
		written                 int
	}{
		{"an unwritable set ahead of a good one onto MP3", notagsMP3, []wl.SyncedLyrics{set("", "bad\x00"), set("", "good")}, 1, 0, 1, 1},
		{"an unwritable set between good ones onto MP3", notagsMP3, []wl.SyncedLyrics{set("", "one"), set("", "bad\x00"), set("deu", "drei")}, 2, 0, 1, 2},
		{"an unwritable set ahead of a good one onto FLAC, which holds one", "../testdata/notags.flac", []wl.SyncedLyrics{set("", "bad\x00"), set("", "good")}, 1, 0, 1, 1},
		{"two good sets onto FLAC, which holds one", "../testdata/notags.flac", []wl.SyncedLyrics{set("", "one"), set("", "two")}, 0, 0, 2, 0},
		{"a language longer than the frame's three bytes onto MP3", notagsMP3, []wl.SyncedLyrics{set("english", "la")}, 0, 1, 0, 1},
		{"a three-letter language onto MP3", notagsMP3, []wl.SyncedLyrics{set("deu", "la")}, 1, 0, 0, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			dstBytes := readFixture(t, c.dst)
			dst := mustParseBytes(t, dstBytes)
			tr := src.Transfer().SetSyncedLyrics(c.sets...)
			plan, report, err := tr.Prepare(dst)
			if err != nil {
				t.Fatal(err)
			}
			if carried, lossy, dropped := count(report); carried != c.carried || lossy != c.lossy || dropped != c.dropped {
				t.Errorf("graded %d carried, %d lossy, %d dropped, want %d, %d, %d: %+v", carried, lossy, dropped, c.carried, c.lossy, c.dropped, report.Items)
			}
			if got := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).SyncedLyrics(); len(got) != c.written {
				t.Errorf("%d sets written, want %d", len(got), c.written)
			}
			assertPlanMatchesPrepare(t, tr, dst)
		})
	}
}

// A slashed number among several values beside a total the source states itself keeps that
// total, and the format-only simulation says the same as the prepared copy.
func TestTransferOfASlashedNumberBesideATotal(t *testing.T) {
	src := mustParseBytes(t, testfile.TaggedFLAC("v", "TRACKNUMBER=3/12", "TRACKNUMBER=4", "TRACKTOTAL=10"))
	dstBytes := readFixture(t, "../testdata/notags.mka")
	dst := mustParseBytes(t, dstBytes)
	plan, report, err := src.PrepareTransfer(dst)
	if err != nil {
		t.Fatal(err)
	}
	if it := transferItemFor(t, report, tag.TrackTotal); it.Disposition != wl.Carried {
		t.Errorf("TRACKTOTAL = %s (%s), want carried", it.Disposition, it.Reason)
	}
	if got, _ := mustParseBytes(t, applyToBytes(t, dstBytes, plan)).Get(tag.TrackTotal); !slices.Equal(got, []string{"10"}) {
		t.Errorf("TRACKTOTAL = %q, want the source's own 10", got)
	}
	assertPlanMatchesPrepare(t, src.Transfer(), dst)
}

// A plan a transfer prepared that writes nothing is headed by the transfer: what could not be
// stored, or nothing left to bring. A transfer is no edit to discard, whatever its plan warns.
func TestTransferPlanThatWritesNothingIsHeadedByTheTransfer(t *testing.T) {
	for _, c := range []struct {
		name, dst string
		held      [2]string
		src       []string
		headline  string
	}{
		{"a second TITLE beside the one Matroska stores", "../testdata/notags.mka", [2]string{"TITLE", "A"}, []string{"TITLE=A", "TITLE=B"},
			"no changes (already up to date)"},
		{"a key the destination stores nothing of", notagsMP3, [2]string{"COMPILATION", "0"}, []string{"TCMP=1"},
			"no changes written; 1 item(s) could not be stored"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dst := mustParseBytes(t, writeBack(t, c.dst, func(e *wl.Editor) { e.Set(tag.Key(c.held[0]), c.held[1]) }))
			plan, report, err := mustParseBytes(t, testfile.TaggedFLAC("v", c.src...)).PrepareTransfer(dst)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.IsNoOp() {
				t.Fatalf("the copy plans a write: %v", plan.Report().Operations)
			}
			if got := report.NoChangesLine(); got != c.headline {
				t.Errorf("report headline %q, want %q", got, c.headline)
			}
			if first, _, _ := strings.Cut(plan.String(), "\n"); first != c.headline {
				t.Errorf("plan prints %q, want %q", first, c.headline)
			}
		})
	}
	// A plan the write options alone make writes, with nothing of the source's to list, and
	// prints what it does.
	held := mustParseBytes(t, writeBack(t, "../testdata/notags.flac", func(e *wl.Editor) { e.Set(tag.Title, "A") }))
	writes, report, err := mustParseBytes(t, testfile.TaggedFLAC("v", "TITLE=A")).PrepareTransfer(held, wl.WithPadding(wl.PaddingPolicy{Target: 1 << 16, Min: 1 << 16}))
	if err != nil {
		t.Fatal(err)
	}
	if writes.IsNoOp() || len(writes.Changes()) != 0 || !report.Lossless() {
		t.Fatalf("setup: want a plan that only pads: no-op %v, changes %v, report %+v", writes.IsNoOp(), writes.Changes(), report.Items)
	}
	if strings.HasPrefix(writes.String(), "no changes") {
		t.Errorf("a copy that only pads prints %q", writes.String())
	}
	// An edit's own plan is headed as before.
	doc := mustParseBytes(t, writeBack(t, "../testdata/notags.mka", func(e *wl.Editor) { e.Set(tag.Title, "A") }))
	plan, err := doc.Edit().Set(tag.Title, "A", "B").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if first, _, _ := strings.Cut(plan.String(), "\n"); first != "no changes written (the edit was discarded)" {
		t.Errorf("an edit's plan prints %q", first)
	}
}

// A WAV file gets an id3 chunk only for what the copy writes. Chapters or synced lyrics the copy
// leaves out bring none, so the fields are graded as the LIST/INFO chunk alone stores them: an
// empty date, which an id3 chunk has no form for, is carried.
func TestTransferGradesWAVFieldsByTheChunksItWrites(t *testing.T) {
	src := mustParseBytes(t, testfile.TaggedFLAC("v", "DATE=", "TITLE=T"))
	dstBytes := readFixture(t, "../testdata/notags.wav")
	unwritable := wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: time.Second, Text: "bad\x00"}}}
	for _, c := range []struct {
		name string
		tr   *wl.Transfer
		want wl.Disposition
	}{
		{"fields alone", src.Transfer(), wl.Carried},
		{"chapters no format stores", src.Transfer().SetChapters(wl.Chapter{Title: "bad\x00"}), wl.Carried},
		{"a synced lyrics set no format stores", src.Transfer().SetSyncedLyrics(unwritable), wl.Carried},
		{"chapters the copy writes", src.Transfer().SetChapters(wl.Chapter{Title: "good"}), wl.Dropped},
		{"a synced lyrics set the copy writes", src.Transfer().SetSyncedLyrics(wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: time.Second, Text: "good"}}}), wl.Dropped},
	} {
		t.Run(c.name, func(t *testing.T) {
			dst := mustParseBytes(t, dstBytes)
			plan, report, err := c.tr.Prepare(dst)
			if err != nil {
				t.Fatal(err)
			}
			if it := transferItemFor(t, report, tag.RecordingDate); it.Disposition != c.want {
				t.Errorf("RECORDINGDATE = %s (%s), want %s", it.Disposition, it.Reason, c.want)
			}
			after := mustParseBytes(t, applyToBytes(t, dstBytes, plan))
			if got, held := after.Get(tag.RecordingDate); held != (c.want == wl.Carried) {
				t.Errorf("RECORDINGDATE after the copy = %q (held %v)", got, held)
			}
			assertPlanMatchesPrepare(t, c.tr, dst)
		})
	}
}
