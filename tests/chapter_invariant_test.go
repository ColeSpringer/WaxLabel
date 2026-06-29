package waxlabel_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// TestChapterCapabilityConsistency keeps the grading-critical chapter capability fields
// (MaxItems and ChapterLoss) equal across codecs that share the same physical store. The
// store logic is centralized in internal/id3 and internal/vorbis, but the capability
// metadata is declared per codec.
func TestChapterCapabilityConsistency(t *testing.T) {
	chapterCaps := func(f wl.Format) wl.Capability { return wl.CapabilitiesFor(f).Chapters }
	sameGrading := func(group []wl.Format) wl.Capability {
		ref := chapterCaps(group[0])
		for _, f := range group[1:] {
			c := chapterCaps(f)
			if c.MaxItems != ref.MaxItems || c.ChapterLoss != ref.ChapterLoss {
				t.Errorf("%s chapters {MaxItems %d, loss %d} != %s {MaxItems %d, loss %d}",
					f, c.MaxItems, c.ChapterLoss, group[0], ref.MaxItems, ref.ChapterLoss)
			}
		}
		return ref
	}
	// The ID3 CHAP/CTOC store (MP3/AAC/AIFF/WAV) and the VorbisComment CHAPTERxxx store
	// (FLAC/Ogg) must each grade uniformly within the group.
	id3 := sameGrading([]wl.Format{wl.FormatMP3, wl.FormatAAC, wl.FormatAIFF, wl.FormatWAV})
	vorbis := sameGrading([]wl.Format{wl.FormatFLAC, wl.FormatOggVorbis, wl.FormatOggOpus})
	// And the two stores genuinely differ: ID3 keeps end times (LangFlags), CHAPTERxxx does
	// not (StartTitleOnly), so a uniform ChapterLoss across both would be a real bug.
	if id3.ChapterLoss == vorbis.ChapterLoss {
		t.Errorf("ID3 and Vorbis chapter loss should differ, both = %d", id3.ChapterLoss)
	}
}

// assertChapters checks a chapter slice by start and title, the common subset every
// chaptered format stores. path names the source being checked: in-memory result or
// reparsed bytes.
func assertChapters(t *testing.T, path string, got, want []wl.Chapter) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: chapter count = %d, want %d: %+v", path, len(got), len(want), got)
	}
	for i := range want {
		if got[i].Start != want[i].Start || got[i].Title != want[i].Title {
			t.Errorf("%s: chapter %d = {%v %q}, want {%v %q}",
				path, i, got[i].Start, got[i].Title, want[i].Start, want[i].Title)
		}
	}
}

// executeChapters applies the plan and returns chapters from both the in-memory Document
// that Execute builds from plan.Result and a fresh re-parse of the written bytes. Checking
// both catches a buildResult that writes correct bytes but forgets to set Media.Chapters.
func executeChapters(t *testing.T, src []byte, plan *wl.Plan) (inMemory, reparsed []wl.Chapter) {
	t.Helper()
	var buf bytes.Buffer
	doc, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(src)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return doc.Chapters(), mustParseBytes(t, buf.Bytes()).Chapters()
}

// chapterFixtures maps each format to a clean fixture for the chapter invariant test.
// Every format whose Chapters capability is writable must appear here.
var chapterFixtures = map[wl.Format]string{
	wl.FormatMP3:       "../testdata/notags.mp3",
	wl.FormatAAC:       "../testdata/notags.aac",
	wl.FormatAIFF:      "../testdata/notags.aiff",
	wl.FormatWAV:       "../testdata/notags.wav",
	wl.FormatFLAC:      "../testdata/notags.flac",
	wl.FormatOggVorbis: "../testdata/notags.ogg",
	wl.FormatOggOpus:   "../testdata/notags.opus",
	wl.FormatMP4:       "../testdata/notags.m4a",
	wl.FormatMatroska:  "../testdata/notags.mka",
}

// TestChapterWriteInvariant checks every writable chapter format with the same structured
// edit. It catches missing change detection, missing write support, and post-write result
// plumbing without requiring per-codec copies of the same test.
//
//  1. produce a non-no-op plan, proving the codec's change-detection gate includes chapters
//     (a missing term silently no-ops a chapters-only SetChapters); and
//  2. round-trip the chapters' start and title through a re-parse, proving the writer
//     persists them and the projected result equals a fresh parse.
//
// Start and title are the lossy-common subset every chaptered format stores (ID3 CHAP also
// keeps ends; CHAPTERxxx and MP4 do not), so the assertion holds uniformly. A new chaptered
// codec is covered automatically; one with a missing gate or projection arm fails here.
func TestChapterWriteInvariant(t *testing.T) {
	want := []wl.Chapter{
		{Start: 0, Title: "Opening"},
		{Start: 3 * time.Second, Title: "Middle"},
		{Start: 7500 * time.Millisecond, Title: "Closing"},
	}
	for _, f := range wl.Formats() {
		if wl.CapabilitiesFor(f).Chapters.Write < wl.AccessPartial {
			continue
		}
		fixture, ok := chapterFixtures[f]
		if !ok {
			t.Errorf("%s has writable chapters but no fixture in chapterFixtures; add one", f)
			continue
		}
		t.Run(f.String(), func(t *testing.T) {
			src, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			plan, err := mustParseBytes(t, src).Edit().SetChapters(want...).Prepare()
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if plan.IsNoOp() {
				t.Fatal("structured-only chapter edit produced a no-op plan; the codec's change-detection gate omits chapters")
			}
			inMem, reparsed := executeChapters(t, src, plan)
			assertChapters(t, "in-memory result", inMem, want)
			assertChapters(t, "reparsed bytes", reparsed, want)

			// Clearing the chapters must also round-trip to none on both paths, exercising the
			// clear branch the set-only path does not. Re-edit the now-chaptered output.
			chaptered := applyToBytes(t, src, plan)
			clearPlan, err := mustParseBytes(t, chaptered).Edit().ClearChapters().Prepare()
			if err != nil {
				t.Fatalf("clear Prepare: %v", err)
			}
			if clearPlan.IsNoOp() {
				t.Fatal("ClearChapters on a chaptered file produced a no-op plan")
			}
			clearedMem, clearedReparsed := executeChapters(t, chaptered, clearPlan)
			assertChapters(t, "in-memory after clear", clearedMem, nil)
			assertChapters(t, "reparsed after clear", clearedReparsed, nil)
		})
	}
}
