package matroska

import (
	"bytes"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/core"
)

// TestChapterUIDQueue covers the start-to-UID matching helpers. Zero slots stay in that
// order so a UID-less atom does not steal a sibling's real UID.
func TestChapterUIDQueue(t *testing.T) {
	q := chapterUIDQueue([]chapterStartUID{
		{0, 11},
		{200 * time.Millisecond, 0},  // a UID-less atom stored before...
		{200 * time.Millisecond, 22}, // ...one that carries UID 22, at the same start
	})
	cases := []struct {
		start time.Duration
		want  uint64
		note  string
	}{
		{0, 11, "match"},
		{999 * time.Millisecond, 0, "no match -> fresh"},
		{200 * time.Millisecond, 0, "same-start first pop: the UID-less atom pops 0 (mint fresh)"},
		{200 * time.Millisecond, 22, "same-start second pop: UID 22 stays with its own atom"},
		{200 * time.Millisecond, 0, "same-start exhausted -> fresh"},
	}
	for _, c := range cases {
		if got := popChapterUID(q, c.start); got != c.want {
			t.Errorf("popChapterUID(%v) = %d, want %d [%s]", c.start, got, c.want, c.note)
		}
	}
}

// TestChapterStartUIDsKeepZero verifies that a ChapterAtom with no real ChapterUID
// keeps its slot in startUIDs.
func TestChapterStartUIDsKeepZero(t *testing.T) {
	atoms := chapAtom(5, 0)
	atoms = append(atoms, chapAtom(0, uint64(100*time.Millisecond))...) // no real ChapterUID
	chapters := encElement(idChapters, encElement(idEditionEntry, atoms))
	m := parseMKA(t, segBytes(chapters))

	ed := m.Native.(*doc).chapters.editions[0]
	want := []chapterStartUID{{0, 5}, {100 * time.Millisecond, 0}}
	if len(ed.startUIDs) != 2 || ed.startUIDs[0] != want[0] || ed.startUIDs[1] != want[1] {
		t.Errorf("startUIDs = %+v, want %+v (every atom kept in file order, including the zero-UID one)", ed.startUIDs, want)
	}
}

// chapAtom builds a ChapterAtom with a ChapterUID and a ChapterTimeStart.
func chapAtom(uid, startNs uint64) []byte {
	body := uintElement(idChapterUID, uid)
	body = append(body, uintElement(idChapTimeStart, startNs)...)
	return encElement(idChapterAtom, body)
}

// A chapter that keeps its UID is written with it in as few bytes as it takes, like the rest of
// the atom; one that is new gets eight bytes, so the size of the rendered edition does not turn
// on the UID drawn.
func TestRenderedChapterUIDWidth(t *testing.T) {
	ed := chapterEdition{startUIDs: []chapterStartUID{{0, 5}}}
	chs := []core.Chapter{{Start: 0}, {Start: time.Second}, {Start: 2 * time.Second}}
	first := renderDefaultEdition(ed, chs, false)
	for range 2000 {
		if b := renderDefaultEdition(ed, chs, false); len(b) != len(first) {
			t.Fatalf("an edition of %d bytes, then one of %d", len(first), len(b))
		}
	}
	kept, minted := uintElement(idChapterUID, 5), append(idBytes(idChapterUID), 0x88)
	if !bytes.Contains(first, kept) || bytes.Count(first, minted) != 2 {
		t.Errorf("edition % x: want UID 5 in one byte and two new UIDs in eight", first)
	}
}
