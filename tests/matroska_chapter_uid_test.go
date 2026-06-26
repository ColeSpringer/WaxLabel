package waxlabel_test

import (
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
)

// chapterUIDsByStart independently decodes the output's Chapters tree and returns
// each top-level ChapterAtom's start time and ChapterUID. The tests need the pairing,
// not just proof that some UID exists, because surviving chapters must keep their own
// UID by start time.
func chapterUIDsByStart(t *testing.T, data []byte) map[time.Duration]uint64 {
	t.Helper()
	_, segData, segEnd, ok := elemRange(data, 0, len(data), idSegment, nil)
	if !ok {
		t.Fatal("no Segment in output")
	}
	out := map[time.Duration]uint64{}
	var walk func(start, end int)
	walk = func(start, end int) {
		off := start
		for off < end {
			id, idn, ok := readVint(data, off, true)
			if !ok {
				return
			}
			size, szn, ok := readVint(data, off+idn, false)
			if !ok {
				return
			}
			ds := off + idn + szn
			de := ds + int(size)
			if de > end || de < ds {
				de = end
			}
			switch id {
			case idChapters, idEditionEntry:
				walk(ds, de)
			case idChapterAtom:
				start, uid := chapterAtomFields(data, ds, de)
				out[start] = uid
			}
			if de <= off {
				return
			}
			off = de
		}
	}
	walk(segData, segEnd)
	return out
}

// chapterAtomFields reads a ChapterAtom's direct ChapterTimeStart and ChapterUID.
func chapterAtomFields(data []byte, start, end int) (time.Duration, uint64) {
	var st time.Duration
	var uid uint64
	eachChildField(data, start, end, func(id uint64, ds, de int) {
		switch id {
		case idChapTimeStart:
			st = time.Duration(beUint(data, ds, de))
		case idChapterUID:
			uid = beUint(data, ds, de)
		}
	})
	return st, uid
}

func beUint(data []byte, start, end int) uint64 {
	var v uint64
	for i := start; i < end; i++ {
		v = v<<8 | uint64(data[i])
	}
	return v
}

// chapterTitleUID pairs a decoded atom's first display title with its ChapterUID.
type chapterTitleUID struct {
	title string
	uid   uint64
}

// chapterTitleUIDs decodes each top-level ChapterAtom's (title, ChapterUID) in document
// order, so a test can confirm which chapter a UID landed on when several share a start.
func chapterTitleUIDs(t *testing.T, data []byte) []chapterTitleUID {
	t.Helper()
	_, segData, segEnd, ok := elemRange(data, 0, len(data), idSegment, nil)
	if !ok {
		t.Fatal("no Segment in output")
	}
	var out []chapterTitleUID
	var walk func(start, end int)
	walk = func(start, end int) {
		off := start
		for off < end {
			id, idn, ok := readVint(data, off, true)
			if !ok {
				return
			}
			size, szn, ok := readVint(data, off+idn, false)
			if !ok {
				return
			}
			ds := off + idn + szn
			de := ds + int(size)
			if de > end || de < ds {
				de = end
			}
			switch id {
			case idChapters, idEditionEntry:
				walk(ds, de)
			case idChapterAtom:
				out = append(out, chapterTitleUID{chapterTitle(data, ds, de), chapterUID(data, ds, de)})
			}
			if de <= off {
				return
			}
			off = de
		}
	}
	walk(segData, segEnd)
	return out
}

func chapterUID(data []byte, start, end int) uint64 {
	var uid uint64
	eachChildField(data, start, end, func(id uint64, ds, de int) {
		if id == idChapterUID {
			uid = beUint(data, ds, de)
		}
	})
	return uid
}

func chapterTitle(data []byte, start, end int) string {
	var title string
	eachChildField(data, start, end, func(id uint64, ds, de int) {
		if id == idChapDisplay && title == "" {
			eachChildField(data, ds, de, func(cid uint64, cds, cde int) {
				if cid == idChapString && title == "" {
					title = string(data[cds:cde])
				}
			})
		}
	})
	return title
}

// eachChildField invokes fn(id, dataStart, dataEnd) for each EBML child in [start,end).
func eachChildField(data []byte, start, end int, fn func(id uint64, ds, de int)) {
	off := start
	for off < end {
		id, idn, ok := readVint(data, off, true)
		if !ok {
			return
		}
		size, szn, ok := readVint(data, off+idn, false)
		if !ok {
			return
		}
		ds := off + idn + szn
		de := ds + int(size)
		if de > end || de < ds {
			return
		}
		fn(id, ds, de)
		if de <= off {
			return
		}
		off = de
	}
}

// TestMatroskaChapterUIDsDuplicateStart covers two chapters with the same start: the
// first originally UID-less and the second carrying UID 22. A rename forces a chapter
// rebuild; UID 22 must stay on the second chapter while the UID-less chapter gets a
// fresh UID.
func TestMatroskaChapterUIDsDuplicateStart(t *testing.T) {
	chapters := mkEl(idChapters, mkEdition(true, nil,
		mkAtom(0, uint64(ms(100)), uint64(ms(200)), "First"),   // file order: UID-less first
		mkAtom(22, uint64(ms(100)), uint64(ms(300)), "Second"), // same start, real UID 22
	))
	data := buildMatroskaCh("matroska", "T", chapters, nil)

	// Rename both (same starts) so the chapters are re-rendered, not copied verbatim.
	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: ms(100), End: ms(200), Title: "First-renamed"},
		wl.Chapter{Start: ms(100), End: ms(300), Title: "Second-renamed"},
	))

	got := chapterTitleUIDs(t, out)
	if len(got) != 2 {
		t.Fatalf("decoded %d atoms, want 2: %+v", len(got), got)
	}
	byTitle := map[string]uint64{got[0].title: got[0].uid, got[1].title: got[1].uid}
	if byTitle["Second-renamed"] != 22 {
		t.Errorf("the originally-UID-22 chapter has UID %d, want 22 (kept by FIFO order, not minted away)", byTitle["Second-renamed"])
	}
	if byTitle["First-renamed"] == 22 {
		t.Errorf("the originally-UID-less chapter stole UID 22 (got %d); it must mint a fresh distinct UID", byTitle["First-renamed"])
	}
	if byTitle["First-renamed"] == 0 {
		t.Errorf("the originally-UID-less chapter must be minted a fresh non-zero UID, got 0")
	}
}

// TestMatroskaChapterUIDsInsertMiddle verifies that inserting a chapter keeps each
// survivor matched to its own ChapterUID by start time. The inserted chapter gets a
// fresh UID instead of taking the following survivor's UID by position.
func TestMatroskaChapterUIDsInsertMiddle(t *testing.T) {
	chapters := mkEl(idChapters, mkEdition(true, nil,
		mkAtom(11, 0, uint64(ms(200)), "First"),
		mkAtom(22, uint64(ms(400)), uint64(ms(600)), "Third"),
	))
	data := buildMatroskaCh("matroska", "T", chapters, nil)

	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, End: ms(200), Title: "First"},
		wl.Chapter{Start: ms(200), End: ms(400), Title: "Inserted"},
		wl.Chapter{Start: ms(400), End: ms(600), Title: "Third"},
	))

	got := chapterUIDsByStart(t, out)
	if len(got) != 3 {
		t.Fatalf("output has %d chapters, want 3: %v", len(got), got)
	}
	if got[0] != 11 {
		t.Errorf("chapter at 0 has UID %d, want 11 (survivor kept)", got[0])
	}
	if got[ms(400)] != 22 {
		t.Errorf("chapter at 400ms has UID %d, want 22 (survivor kept, not reassigned to the insert)", got[ms(400)])
	}
	if ins := got[ms(200)]; ins == 0 || ins == 11 || ins == 22 {
		t.Errorf("inserted chapter UID = %d, want a fresh distinct value", ins)
	}
}

// TestMatroskaChapterUIDsDeleteMiddle verifies that deleting the middle chapter leaves
// the first and third chapters with their own UIDs. The third keeps 33 by start time,
// rather than taking the deleted middle chapter's UID by position.
func TestMatroskaChapterUIDsDeleteMiddle(t *testing.T) {
	chapters := mkEl(idChapters, mkEdition(true, nil,
		mkAtom(11, 0, uint64(ms(200)), "First"),
		mkAtom(22, uint64(ms(200)), uint64(ms(400)), "Second"),
		mkAtom(33, uint64(ms(400)), uint64(ms(600)), "Third"),
	))
	data := buildMatroskaCh("matroska", "T", chapters, nil)

	out, _ := saveMatroska(t, data, mustParseBytes(t, data).Edit().SetChapters(
		wl.Chapter{Start: 0, End: ms(200), Title: "First"},
		wl.Chapter{Start: ms(400), End: ms(600), Title: "Third"},
	))

	got := chapterUIDsByStart(t, out)
	if len(got) != 2 {
		t.Fatalf("output has %d chapters, want 2: %v", len(got), got)
	}
	if got[0] != 11 {
		t.Errorf("First UID = %d, want 11", got[0])
	}
	if got[ms(400)] != 33 {
		t.Errorf("Third UID = %d, want 33 (kept by start; the deleted middle's 22 must not slide up)", got[ms(400)])
	}
}
