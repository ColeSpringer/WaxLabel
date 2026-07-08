package waxlabel_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// syncedLyricsFixtures maps each synced-lyrics-writable format to a clean fixture. MP4 and
// Matroska are intentionally absent: they carry synced lyrics in a timed-text track, not
// metadata, so their SyncedLyrics capability is AccessNone.
var syncedLyricsFixtures = map[wl.Format]string{
	wl.FormatMP3:       "../testdata/notags.mp3",
	wl.FormatAAC:       "../testdata/notags.aac",
	wl.FormatAIFF:      "../testdata/notags.aiff",
	wl.FormatWAV:       "../testdata/notags.wav",
	wl.FormatFLAC:      "../testdata/notags.flac",
	wl.FormatOggVorbis: "../testdata/notags.ogg",
	wl.FormatOggOpus:   "../testdata/notags.opus",
}

// sampleSyncedLines is the timed-line subset every synced-lyrics format stores (SYLT keeps
// the language and descriptor; the LRC store drops them, so only the lines round-trip
// uniformly). It includes an empty-text clear marker.
var sampleSyncedLines = []wl.SyncedLine{
	{Time: 0, Text: "Opening"},
	{Time: 12 * time.Second, Text: "Verse one"},
	{Time: 17500 * time.Millisecond, Text: "Chorus"},
	{Time: 30 * time.Second, Text: ""}, // clear marker
}

// assertSyncedLines checks a synced-lyrics slice carries exactly one set whose lines match
// want by time and text, the cross-format common subset. path names the source checked.
func assertSyncedLines(t *testing.T, path string, got []wl.SyncedLyrics, want []wl.SyncedLine) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%s: synced-lyrics set count = %d, want 1: %+v", path, len(got), got)
	}
	lines := got[0].Lines
	if len(lines) != len(want) {
		t.Fatalf("%s: line count = %d, want %d: %+v", path, len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i].Time != want[i].Time || lines[i].Text != want[i].Text {
			t.Errorf("%s: line %d = {%v %q}, want {%v %q}",
				path, i, lines[i].Time, lines[i].Text, want[i].Time, want[i].Text)
		}
	}
}

// executeSynced applies the plan and returns the synced lyrics from both the in-memory
// Document and a fresh re-parse of the written bytes, catching a buildResult that writes
// correct bytes but forgets to set Media.SyncedLyrics.
func executeSynced(t *testing.T, src []byte, plan *wl.Plan) (inMemory, reparsed []wl.SyncedLyrics) {
	t.Helper()
	var buf bytes.Buffer
	doc, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(src)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return doc.SyncedLyrics(), mustParseBytes(t, buf.Bytes()).SyncedLyrics()
}

// TestSyncedLyricsWriteInvariant checks every synced-lyrics-writable format with the same
// structured edit: the codec's change-detection gate must include synced lyrics (a missing
// term silently no-ops a SetSyncedLyrics), and the writer must persist the timed lines so
// the projected result equals a fresh parse. A clear must also round-trip to none. It is
// the synced-lyrics analogue of TestChapterWriteInvariant.
func TestSyncedLyricsWriteInvariant(t *testing.T) {
	want := wl.SyncedLyrics{Language: "eng", Description: "Main", Lines: sampleSyncedLines}
	for _, f := range wl.Formats() {
		if wl.CapabilitiesFor(f).SyncedLyrics.Write < wl.AccessPartial {
			continue
		}
		fixture, ok := syncedLyricsFixtures[f]
		if !ok {
			t.Errorf("%s has writable synced lyrics but no fixture in syncedLyricsFixtures; add one", f)
			continue
		}
		t.Run(f.String(), func(t *testing.T) {
			src, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			plan, err := mustParseBytes(t, src).Edit().SetSyncedLyrics(want).Prepare()
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if plan.IsNoOp() {
				t.Fatal("structured-only synced-lyrics edit produced a no-op plan; the codec's change-detection gate omits synced lyrics")
			}
			inMem, reparsed := executeSynced(t, src, plan)
			assertSyncedLines(t, "in-memory result", inMem, sampleSyncedLines)
			assertSyncedLines(t, "reparsed bytes", reparsed, sampleSyncedLines)

			// Clearing must round-trip to none on both paths, exercising the clear branch.
			lyriced := applyToBytes(t, src, plan)
			clearPlan, err := mustParseBytes(t, lyriced).Edit().ClearSyncedLyrics().Prepare()
			if err != nil {
				t.Fatalf("clear Prepare: %v", err)
			}
			if clearPlan.IsNoOp() {
				t.Fatal("ClearSyncedLyrics on a lyriced file produced a no-op plan")
			}
			clearedMem, clearedReparsed := executeSynced(t, lyriced, clearPlan)
			if len(clearedMem) != 0 {
				t.Errorf("in-memory after clear: %d sets, want 0", len(clearedMem))
			}
			if len(clearedReparsed) != 0 {
				t.Errorf("reparsed after clear: %d sets, want 0", len(clearedReparsed))
			}
		})
	}
}

// TestNoOpWriteOnLyricedFLACByteIdentical is the preservation pin: an edit that does not
// touch synced lyrics never re-serializes them through FormatLRC, so a no-op write on a
// lyrics-bearing FLAC is byte-identical and an unrelated title edit leaves the synced lyrics intact
// on re-parse. This is what keeps the new space-separator convention from silently rewriting (and,
// for a file an older WaxLabel already corrupted, re-touching) the lyrics block on every save.
func TestNoOpWriteOnLyricedFLACByteIdentical(t *testing.T) {
	src, err := os.ReadFile("../testdata/notags.flac")
	if err != nil {
		t.Fatal(err)
	}
	set := wl.SyncedLyrics{Lines: sampleSyncedLines} // the LRC store keeps only the timed lines
	lyricPlan, err := mustParseBytes(t, src).Edit().SetSyncedLyrics(set).Prepare()
	if err != nil {
		t.Fatalf("Prepare lyrics: %v", err)
	}
	lyriced := applyToBytes(t, src, lyricPlan)

	// A no-op write must reproduce the exact bytes (the crown-jewel invariant), proving the lyrics
	// block is copied verbatim rather than re-emitted through FormatLRC.
	noop, err := mustParseBytes(t, lyriced).Edit().Prepare()
	if err != nil {
		t.Fatalf("Prepare no-op: %v", err)
	}
	if out := applyToBytes(t, lyriced, noop); !bytes.Equal(out, lyriced) {
		t.Errorf("no-op write on a lyrics-bearing FLAC changed bytes: %d -> %d", len(lyriced), len(out))
	}

	// An unrelated title edit must keep the synced lyrics intact and unchanged on re-parse.
	titlePlan, err := mustParseBytes(t, lyriced).Edit().Set(tag.Title, "Unrelated").Prepare()
	if err != nil {
		t.Fatalf("Prepare title edit: %v", err)
	}
	edited := applyToBytes(t, lyriced, titlePlan)
	assertSyncedLines(t, "after unrelated title edit", mustParseBytes(t, edited).SyncedLyrics(), sampleSyncedLines)
}

// TestSyncedLyricsCapabilityConsistency keeps synced-lyrics capability fields equal across
// codecs that share the same physical store, and confirms the two stores differ in the
// expected way: SYLT is lossless, while the LRC store drops language and descriptor.
func TestSyncedLyricsCapabilityConsistency(t *testing.T) {
	cap := func(f wl.Format) wl.Capability { return wl.CapabilitiesFor(f).SyncedLyrics }
	same := func(group []wl.Format) wl.Capability {
		ref := cap(group[0])
		for _, f := range group[1:] {
			c := cap(f)
			if c.MaxItems != ref.MaxItems || c.SyncedLyricsLoss != ref.SyncedLyricsLoss || c.Write != ref.Write || c.SyncedLyricsTimeMax != ref.SyncedLyricsTimeMax {
				t.Errorf("%s synced lyrics {MaxItems %d, loss %d, write %v, timeMax %v} != %s {MaxItems %d, loss %d, write %v, timeMax %v}",
					f, c.MaxItems, c.SyncedLyricsLoss, c.Write, c.SyncedLyricsTimeMax, group[0], ref.MaxItems, ref.SyncedLyricsLoss, ref.Write, ref.SyncedLyricsTimeMax)
			}
		}
		return ref
	}
	id3 := same([]wl.Format{wl.FormatMP3, wl.FormatAAC, wl.FormatAIFF, wl.FormatWAV})
	vorbis := same([]wl.Format{wl.FormatFLAC, wl.FormatOggVorbis, wl.FormatOggOpus})
	// SYLT is lossless and the LRC store drops language and descriptor, so the two stores
	// must grade differently. The transfer grading test exercises the loss semantics. A
	// zero-value Capability has the no-loss default, so id3 matching it confirms SYLT is
	// graded lossless.
	if id3.SyncedLyricsLoss == vorbis.SyncedLyricsLoss {
		t.Errorf("ID3 (lossless) and Vorbis (language-dropped) synced-lyrics loss should differ, both = %d", id3.SyncedLyricsLoss)
	}
	if id3.SyncedLyricsLoss != (wl.Capability{}).SyncedLyricsLoss {
		t.Errorf("ID3 SYLT loss = %d, want the no-loss default", id3.SyncedLyricsLoss)
	}
}

// lyricedMP3 builds an MP3 carrying one synced-lyrics set with a language, for the transfer
// and preservation tests.
func lyricedMP3(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile("../testdata/notags.mp3")
	if err != nil {
		t.Fatal(err)
	}
	set := wl.SyncedLyrics{Language: "eng", Description: "Main", Lines: sampleSyncedLines}
	plan, err := mustParseBytes(t, src).Edit().SetSyncedLyrics(set).Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return applyToBytes(t, src, plan)
}

// TestSyncedLyricsTransferGrading checks the transfer report grades a synced-lyrics set by
// the destination's capability: lossless onto another SYLT format, Lossy onto an LRC store
// (language dropped), and Dropped onto a format that cannot store synced lyrics at all.
func TestSyncedLyricsTransferGrading(t *testing.T) {
	doc := mustParseBytes(t, lyricedMP3(t))

	syncedItem := func(dst wl.Format) wl.TransferItem {
		t.Helper()
		rep, err := doc.PlanTransfer(dst)
		if err != nil {
			t.Fatalf("PlanTransfer(%s): %v", dst, err)
		}
		for _, it := range rep.Items {
			if it.Kind == wl.TransferSyncedLyric {
				return it
			}
		}
		t.Fatalf("%s: no synced-lyrics transfer item in %+v", dst, rep.Items)
		return wl.TransferItem{}
	}

	// SYLT -> SYLT (MP3 -> AAC): the language survives, so Carried.
	if it := syncedItem(wl.FormatAAC); it.Disposition != wl.Carried {
		t.Errorf("MP3->AAC synced lyrics = %v, want carried (%s)", it.Disposition, it.Reason)
	}
	// SYLT -> LRC (MP3 -> FLAC): the per-set language is dropped, so Lossy.
	if it := syncedItem(wl.FormatFLAC); it.Disposition != wl.Lossy {
		t.Errorf("MP3->FLAC synced lyrics = %v, want lossy", it.Disposition)
	}
	// SYLT -> a format with no synced-lyrics store (MP4, Matroska): Dropped.
	for _, dst := range []wl.Format{wl.FormatMP4, wl.FormatMatroska} {
		if it := syncedItem(dst); it.Disposition != wl.Dropped {
			t.Errorf("MP3->%s synced lyrics = %v, want dropped", dst, it.Disposition)
		}
	}

	// A timestamp the destination must clamp grades Lossy. The LRC store holds a far
	// larger range than SYLT's 32-bit ms field (~49.7 days), so a FLAC line past that field
	// carries onto another LRC store, but onto a SYLT format it is clamped and graded Lossy.
	flacBytes, err := os.ReadFile("../testdata/notags.flac")
	if err != nil {
		t.Fatal(err)
	}
	overSYLT := wl.SyncedLyrics{Lines: []wl.SyncedLine{{Time: 60 * 24 * time.Hour, Text: "way out"}}} // ~60 days > SYLT max
	srcPlan, err := mustParseBytes(t, flacBytes).Edit().SetSyncedLyrics(overSYLT).Prepare()
	if err != nil {
		t.Fatalf("build over-SYLT FLAC: %v", err)
	}
	bigDoc := mustParseBytes(t, applyToBytes(t, flacBytes, srcPlan))
	bigItem := func(dst wl.Format) wl.TransferItem {
		t.Helper()
		rep, err := bigDoc.PlanTransfer(dst)
		if err != nil {
			t.Fatalf("PlanTransfer(%s): %v", dst, err)
		}
		for _, it := range rep.Items {
			if it.Kind == wl.TransferSyncedLyric {
				return it
			}
		}
		t.Fatalf("%s: no synced-lyrics item", dst)
		return wl.TransferItem{}
	}
	// FLAC -> AAC (SYLT): the line exceeds SYLT's ms field, so it clamps -> Lossy.
	if it := bigItem(wl.FormatAAC); it.Disposition != wl.Lossy || it.Reason == "" {
		t.Errorf("over-SYLT FLAC->AAC = %s/%q, want Lossy (SYLT clamps the timestamp)", it.Disposition, it.Reason)
	}
	// FLAC -> FLAC (LRC): the LRC ceiling holds it, and the source set has no language, so Carried.
	if it := bigItem(wl.FormatFLAC); it.Disposition != wl.Carried {
		t.Errorf("over-SYLT FLAC->FLAC = %s, want Carried (the LRC ceiling holds it)", it.Disposition)
	}
}

// TestSyncedLyricsLanguageRoundTrip checks the library accepts a file's own short (1-2
// byte) NUL-padded SYLT language and round-trips it through a read-then-rewrite instead
// of corrupting it to "enX" or blocking the edit. The CLI validates author-provided
// --synced-lyrics-lang values separately; the library stays lenient so parsed files can
// be saved again.
func TestSyncedLyricsLanguageRoundTrip(t *testing.T) {
	src, err := os.ReadFile("../testdata/notags.mp3")
	if err != nil {
		t.Fatal(err)
	}
	roundtrip := func(lang string) string {
		set := wl.SyncedLyrics{Language: lang, Lines: []wl.SyncedLine{{Time: 0, Text: "x"}}}
		plan, err := mustParseBytes(t, src).Edit().SetSyncedLyrics(set).Prepare()
		if err != nil {
			t.Fatalf("Prepare(lang=%q): %v", lang, err)
		}
		out := applyToBytes(t, src, plan)
		got := mustParseBytes(t, out).SyncedLyrics()
		if len(got) != 1 {
			t.Fatalf("lang=%q: %d sets", lang, len(got))
		}
		return got[0].Language
	}
	// A 2-char source code NUL-pads and round-trips verbatim (no "enX" corruption).
	if got := roundtrip("en"); got != "en" {
		t.Errorf("language en round-tripped as %q, want en (NUL-pad, not 'enX')", got)
	}
	// A normal 3-letter code is verbatim; an empty language stays empty.
	if got := roundtrip("eng"); got != "eng" {
		t.Errorf("language eng round-tripped as %q", got)
	}
	if got := roundtrip(""); got != "" {
		t.Errorf("empty language round-tripped as %q, want empty", got)
	}
}

// TestSyncedLyricsEmptySetDropped checks authoring a set with no lines writes nothing and is
// not reported as a written set, so the plan's count never disagrees with the result (the
// codecs skip a line-less set). Combined with a real tag edit it stays a clean tag-only edit.
func TestSyncedLyricsEmptySetDropped(t *testing.T) {
	for _, fx := range []string{"../testdata/notags.mp3", "../testdata/notags.flac"} {
		src, err := os.ReadFile(fx)
		if err != nil {
			t.Fatal(err)
		}
		empty := wl.SyncedLyrics{Language: "eng"} // no Lines
		plan, err := mustParseBytes(t, src).Edit().Set(tag.Title, "T").SetSyncedLyrics(empty).Prepare()
		if err != nil {
			t.Fatalf("%s Prepare: %v", fx, err)
		}
		for _, c := range plan.Changes() {
			if string(c.Key) == "synced lyrics" {
				t.Errorf("%s: plan reports a synced-lyrics change for an empty set: %v", fx, c)
			}
		}
		out := applyToBytes(t, src, plan)
		if got := mustParseBytes(t, out).SyncedLyrics(); len(got) != 0 {
			t.Errorf("%s: wrote %d synced-lyrics sets for an empty author, want 0", fx, len(got))
		}
	}
}

// TestSyncedLyricsTimestampOverflowWarns checks a synced-lyric line past the SYLT 32-bit
// millisecond field surfaces a clamp warning on an ID3-backed write rather than silently
// moving the lyric while the report still implies losslessness.
func TestSyncedLyricsTimestampOverflowWarns(t *testing.T) {
	src, err := os.ReadFile("../testdata/notags.mp3")
	if err != nil {
		t.Fatal(err)
	}
	set := wl.SyncedLyrics{Language: "eng", Lines: []wl.SyncedLine{{Time: 60 * 24 * time.Hour, Text: "x"}}}
	plan, err := mustParseBytes(t, src).Edit().SetSyncedLyrics(set).Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	found := false
	for _, w := range plan.Report().Warnings {
		if w.Code == wl.WarnSyncedLyricsTimestampClamped {
			found = true
		}
	}
	if !found {
		t.Errorf("expected synced-lyrics-timestamp-clamped warning, got %v", plan.Report().Warnings)
	}
}

// TestSyncedLyricsTransferApply checks the transfer apply path carries the timed lines
// onto the destination, not only that the report grades them. An MP3 SYLT set copies onto
// a clean FLAC with the language dropped by the LRC store but every line kept.
func TestSyncedLyricsTransferApply(t *testing.T) {
	src := mustParseBytes(t, lyricedMP3(t))
	dstBytes, err := os.ReadFile("../testdata/notags.flac")
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	out := applyToBytes(t, dstBytes, plan)
	got := mustParseBytes(t, out).SyncedLyrics()
	assertSyncedLines(t, "transferred onto FLAC", got, sampleSyncedLines)
	if got[0].Language != "" {
		t.Errorf("FLAC kept language %q, want it dropped by the LRC store", got[0].Language)
	}
}

// TestSyncedLyricsCarryDoesNotInheritLanguage is a regression guard at the library boundary:
// carrying a no-language synced-lyrics set (a FLAC/Ogg source stores none) onto a destination
// that already has an eng SYLT must read back with no language, not silently inherit the
// destination's - otherwise the transfer report says "carried/lossless" while the bytes gain a
// language the source never had. The id3 unit test pins that an authored line-only edit still
// keeps the destination language (the documented CLI convenience); this pins the carry path.
func TestSyncedLyricsCarryDoesNotInheritLanguage(t *testing.T) {
	// Source: a FLAC carrying a no-language synced-lyrics set (the Vorbis LRC store holds none).
	flacBytes, err := os.ReadFile("../testdata/notags.flac")
	if err != nil {
		t.Fatal(err)
	}
	noLang := wl.SyncedLyrics{Lines: sampleSyncedLines} // no Language
	srcPlan, err := mustParseBytes(t, flacBytes).Edit().SetSyncedLyrics(noLang).Prepare()
	if err != nil {
		t.Fatalf("source Prepare: %v", err)
	}
	srcFLAC := applyToBytes(t, flacBytes, srcPlan)

	// Destination: an MP3 that already has an eng SYLT set; carry FLAC -> MP3.
	dstMP3 := lyricedMP3(t)
	plan, _, err := mustParseBytes(t, srcFLAC).PrepareTransfer(mustParseBytes(t, dstMP3))
	if err != nil {
		t.Fatalf("PrepareTransfer: %v", err)
	}
	out := applyToBytes(t, dstMP3, plan)
	got := mustParseBytes(t, out).SyncedLyrics()
	assertSyncedLines(t, "carried onto MP3", got, sampleSyncedLines)
	if got[0].Language != "" {
		t.Errorf("carried MP3 SYLT language = %q, want empty (must not inherit the destination's eng)", got[0].Language)
	}
}

// TestSyncedLyricsPreservation checks an unrelated tag edit preserves a file's synced
// lyrics: the SYLT (MP3) and SYNCEDLYRICS (FLAC) stores are owned by the structured model,
// so they survive a title edit untouched.
func TestSyncedLyricsPreservation(t *testing.T) {
	t.Run("MP3-SYLT", func(t *testing.T) {
		lyriced := lyricedMP3(t)
		plan, err := mustParseBytes(t, lyriced).Edit().Set(tag.Title, "New Title").Prepare()
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		out := applyToBytes(t, lyriced, plan)
		assertSyncedLines(t, "after unrelated edit", mustParseBytes(t, out).SyncedLyrics(), sampleSyncedLines)
	})
	t.Run("FLAC-SYNCEDLYRICS", func(t *testing.T) {
		src, err := os.ReadFile("../testdata/notags.flac")
		if err != nil {
			t.Fatal(err)
		}
		set := wl.SyncedLyrics{Lines: sampleSyncedLines}
		plan, err := mustParseBytes(t, src).Edit().SetSyncedLyrics(set).Prepare()
		if err != nil {
			t.Fatalf("set Prepare: %v", err)
		}
		lyriced := applyToBytes(t, src, plan)
		edit, err := mustParseBytes(t, lyriced).Edit().Set(tag.Title, "New Title").Prepare()
		if err != nil {
			t.Fatalf("edit Prepare: %v", err)
		}
		out := applyToBytes(t, lyriced, edit)
		assertSyncedLines(t, "after unrelated edit", mustParseBytes(t, out).SyncedLyrics(), sampleSyncedLines)
	})
}
