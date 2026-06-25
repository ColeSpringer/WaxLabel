package waxlabel_test

import (
	"bytes"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// chaptersFromTitles builds chapters at one-second spacing from titles (none of which
// contain "mdat", so bytes.Count(out, "mdat") reliably counts mdat atoms).
func chaptersFromTitles(titles ...string) []wl.Chapter {
	chs := make([]wl.Chapter, len(titles))
	for i, title := range titles {
		chs[i] = wl.Chapter{Start: time.Duration(i) * time.Second, Title: title}
	}
	return chs
}

// TestMP4ChapterMdatFlatAndEssenceStable verifies that a QuickTime chapter write
// appends a chapter-sample mdat at end-of-file. Each subsequent rewrite must reclaim the
// prior one rather than leak it, so the mdat count stays flat from the second edit
// onward and a clear returns it to baseline. The audio essence (parse-side audio-only
// rule) stays byte-stable across the whole sequence. The synthetic audio payload (0xA7)
// and the titles never contain "mdat", so the byte count is an exact atom count.
func TestMP4ChapterMdatFlatAndEssenceStable(t *testing.T) {
	base := mp4AssembleUdta() // ftyp + audio moov + audio mdat, no chapters
	baseline := bytes.Count(base, []byte("mdat"))
	if baseline != 1 {
		t.Fatalf("baseline mdat count = %d, want 1 (the audio mdat)", baseline)
	}
	baseEssence := essenceOf(t, base)

	setChapters := func(src []byte, titles ...string) []byte {
		plan, err := mustParseBytes(t, src).Edit().SetChapters(chaptersFromTitles(titles...)...).Prepare()
		if err != nil {
			t.Fatalf("set chapters %v: %v", titles, err)
		}
		return applyToBytes(t, src, plan)
	}

	// First edit creates the QuickTime chapter track and appends its mdat: the count may
	// legitimately rise here (there was no chapter mdat before).
	v := setChapters(base, "A", "B")
	firstCount := bytes.Count(v, []byte("mdat"))
	if firstCount <= baseline {
		t.Fatalf("first chapter write should add a chapter mdat; count %d, baseline %d", firstCount, baseline)
	}
	if e := essenceOf(t, v); !e.Equal(baseEssence) {
		t.Errorf("essence drifted on the first chapter write")
	}

	// Successive rewrites (varying chapter counts) must reclaim the prior chapter mdat:
	// the total stays flat and the essence stays stable.
	rewrites := [][]string{{"C", "D", "E"}, {"F"}, {"G", "H", "I", "J"}, {"K", "L"}}
	for i, titles := range rewrites {
		v = setChapters(v, titles...)
		if c := bytes.Count(v, []byte("mdat")); c != firstCount {
			t.Errorf("rewrite %d leaked an mdat: count %d, want flat at %d", i+2, c, firstCount)
		}
		if e := essenceOf(t, v); !e.Equal(baseEssence) {
			t.Errorf("rewrite %d: essence drifted from baseline", i+2)
		}
	}

	// Clearing removes the chapter track and reclaims the chapter mdat: back to baseline.
	clearPlan, err := mustParseBytes(t, v).Edit().ClearChapters().Prepare()
	if err != nil {
		t.Fatalf("clear chapters: %v", err)
	}
	cleared := applyToBytes(t, v, clearPlan)
	if c := bytes.Count(cleared, []byte("mdat")); c != baseline {
		t.Errorf("clear did not restore the baseline mdat count: %d, want %d", c, baseline)
	}
	if e := essenceOf(t, cleared); !e.Equal(baseEssence) {
		t.Errorf("essence not restored to baseline after clear")
	}
}
