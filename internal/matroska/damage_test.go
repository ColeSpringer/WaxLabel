package matroska

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/waxerr"
)

const testLimit = int64(1 << 20)

// firstElement reads the element at the start of raw.
func firstElement(t *testing.T, raw []byte) (core.ReaderAtSized, element) {
	t.Helper()
	src := core.BytesSource(raw)
	el, ok, err := readElement(src, 0, src.Size(), testLimit)
	if !ok || err != nil {
		t.Fatalf("readElement: ok=%v err=%v", ok, err)
	}
	return src, el
}

// A leaf whose size runs past its parent holds no value: the bytes it reaches are the parent's
// other children, or fewer than it declares.
func TestLeafThatOverrunsItsParentIsNotRead(t *testing.T) {
	// Eight bytes declared, four held: read by its held length, a float32.
	src, el := firstElement(t, []byte{byte(idSampFreq), 0x88, 0x45, 0x3B, 0x80, 0x00})
	if !el.clamped || el.declared != 8 || el.dataLen() != 4 || !el.overruns() {
		t.Fatalf("element = %+v, want one clamped from 8 bytes to 4", el)
	}
	if f, ok, err := readFloat(src, el, testLimit); ok || f != 0 || err != nil {
		t.Errorf("readFloat = %v, %v, %v; want no value", f, ok, err)
	}
	if v, err := readUint(src, el, testLimit); v != 0 || err != nil {
		t.Errorf("readUint = %d, %v; want 0", v, err)
	}
	if s, cut, err := readString(src, el, testLimit); s != "" || cut || err != nil {
		t.Errorf("readString = %q, cut %v, %v; want none", s, cut, err)
	}
	if b, err := readBytes(src, el, testLimit); b != nil || err != nil {
		t.Errorf("readBytes = %x, %v; want none", b, err)
	}
	if b, err := readBytesPrefix(src, el, 2, testLimit); b != nil || err != nil {
		t.Errorf("readBytesPrefix = %x, %v; want none", b, err)
	}
	if b, err := captureRaw(src, el, testLimit); b != nil || err != nil {
		t.Errorf("captureRaw = %x, %v; want none", b, err)
	}
	if b, err := captureHeld(src, el, testLimit); len(b) != 6 || err != nil {
		t.Errorf("captureHeld = %x, %v; want the six bytes the source holds", b, err)
	}

	src, el = firstElement(t, []byte{byte(idChapTimeStart), 0xFF, 0x01, 0x02})
	if !el.unknown || el.clamped || !el.overruns() {
		t.Fatalf("element = %+v, want one of unknown size", el)
	}
	if v, err := readUint(src, el, testLimit); v != 0 || err != nil {
		t.Errorf("readUint of an unknown-size element = %d, %v; want 0", v, err)
	}
}

// The reporting walk hands over every element whose header parses, an overrunning one too, and
// says where they end.
func TestEachChildReport(t *testing.T) {
	uid := uintElement(idChapterUID, 7)
	walk := func(raw []byte) (ids []uint64, overrun []bool, stop int64) {
		t.Helper()
		src := core.BytesSource(raw)
		stop, err := eachChildReport(src, 0, src.Size(), bits.NewDepth(8), testLimit, func(el element) error {
			ids, overrun = append(ids, el.id), append(overrun, el.overruns())
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return ids, overrun, stop
	}
	for _, tc := range []struct {
		name    string
		tail    []byte
		ids     []uint64
		overrun []bool
		stop    int
	}{
		{"whole", nil, []uint64{idChapterUID}, []bool{false}, len(uid)},
		{"zero bytes are no header", []byte{0, 0, 0}, []uint64{idChapterUID}, []bool{false}, len(uid)},
		{"an ID with no size", []byte{byte(idChapTimeStart)}, []uint64{idChapterUID}, []bool{false}, len(uid)},
		{"an ID of five bytes", []byte{0x08, 0x81, 0x82, 0x83, 0x84, 0x81, 0x00}, []uint64{idChapterUID}, []bool{false}, len(uid)},
		{"a child past the end", []byte{byte(idChapTimeStart), 0x84, 1}, []uint64{idChapterUID, idChapTimeStart}, []bool{false, true}, len(uid) + 3},
		{"a child of unknown size", []byte{byte(idChapTimeStart), 0xFF, 1}, []uint64{idChapterUID, idChapTimeStart}, []bool{false, true}, len(uid) + 3},
	} {
		ids, overrun, stop := walk(append(slices.Clone(uid), tc.tail...))
		if !slices.Equal(ids, tc.ids) || !slices.Equal(overrun, tc.overrun) || stop != int64(tc.stop) {
			t.Errorf("%s: ids %x overrun %v stop %d, want %x %v %d", tc.name, ids, overrun, stop, tc.ids, tc.overrun, tc.stop)
		}
	}
}

// overrun is el with its one-byte size turned into a four-byte one, which reaches far past
// anything that holds it. Three bytes must follow the size byte where it stands, or no header
// parses there at all.
func overrun(t *testing.T, el []byte, idLen int) []byte {
	t.Helper()
	if el[idLen]&0x80 == 0 {
		t.Fatalf("element % x: size is not one byte", el[:idLen+1])
	}
	out := slices.Clone(el)
	out[idLen] = 0x17
	return out
}

func tagNames(g tagGroup) []string {
	var names []string
	for _, st := range g.tags {
		names = append(names, st.name)
	}
	return names
}

// A Tag is read up to the first of its children that is no whole element: a SimpleTag that
// overruns it, bytes no header parses from, a child of unknown size. A SimpleTag holding such
// bytes at any depth is not read, and neither is a Tag whose Targets holds them.
func TestParseTagReadsOnlyWholeElements(t *testing.T) {
	targets := encElement(idTargets, uintElement(idTgtTypeVal, 30))
	artist, genre := simpleTagBytes("ARTIST", "A", ""), simpleTagBytes("GENRE", "G", "")
	nested := encElement(idSimpleTag, slices.Concat(stringElement(idTagName, "PART"), stringElement(idTagString, "p"),
		overrun(t, simpleTagBytes("SUB", "s", ""), 2)))
	deep := encElement(idSimpleTag, slices.Concat(stringElement(idTagName, "PART"), stringElement(idTagString, "p"),
		encElement(idSimpleTag, slices.Concat(stringElement(idTagName, "SUB"), overrun(t, simpleTagBytes("LEAF", "l", ""), 2)))))
	padded := encElement(idSimpleTag, slices.Concat(stringElement(idTagName, "PART"), stringElement(idTagString, "p"), []byte{0, 0}))
	junkTargets := encElement(idTargets, slices.Concat(uintElement(idTgtTypeVal, 30), []byte{0, 0}))
	longTargets := encElement(idTargets, slices.Concat(overrun(t, uintElement(idTgtTypeVal, 30), 2), uintElement(idTagTrackUID, 5)))
	for _, tc := range []struct {
		name    string
		content []byte
		ok      bool
		names   []string
		unread  int
	}{
		{"whole", slices.Concat(targets, artist, genre), true, []string{"ARTIST", "GENRE"}, 0},
		{"a SimpleTag past the Tag", slices.Concat(targets, artist, overrun(t, genre, 2)), true, []string{"ARTIST"}, len(genre)},
		{"the first SimpleTag past the Tag", slices.Concat(targets, overrun(t, artist, 2), genre), true, nil, len(artist) + len(genre)},
		{"bytes that are no element", slices.Concat(targets, artist, []byte{0, 0, 0}), true, []string{"ARTIST"}, 3},
		{"a child of unknown size", slices.Concat(targets, artist, []byte{byte(idVoid), 0xFF, 1, 2}), true, []string{"ARTIST"}, 4},
		{"a nested SimpleTag past its parent", slices.Concat(targets, nested, genre), true, []string{"GENRE"}, len(nested)},
		{"a SimpleTag past its parent two levels down", slices.Concat(targets, deep, genre), true, []string{"GENRE"}, len(deep)},
		{"a SimpleTag holding bytes that are no element", slices.Concat(targets, padded, genre), true, []string{"GENRE"}, len(padded)},
		{"Targets holding bytes that are no element", slices.Concat(junkTargets, artist), false, nil, 3 + len(junkTargets) + len(artist)},
		{"Targets past the Tag", slices.Concat(artist, overrun(t, targets, 2)), false, nil, 3 + len(artist) + len(targets)},
		{"damage and no Targets seen", slices.Concat(artist, overrun(t, genre, 2)), false, nil, 3 + len(artist) + len(genre)},
		{"Targets holding a child that runs past it", slices.Concat(longTargets, artist), false, nil, 3 + len(longTargets) + len(artist)},
		{"a whole Targets behind a damaged one", slices.Concat(junkTargets, targets, artist), false, nil, 3 + len(junkTargets) + len(targets) + len(artist)},
	} {
		raw := encElement(idTag, tc.content)
		src, el := firstElement(t, raw)
		g, unread, ok, err := parseTag(src, el, bits.NewDepth(8), testLimit)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if ok != tc.ok || !slices.Equal(tagNames(g), tc.names) || unread != int64(tc.unread) {
			t.Errorf("%s: ok %v, tags %v, %d unread; want %v, %v, %d", tc.name, ok, tagNames(g), unread, tc.ok, tc.names, tc.unread)
		}
		if wantRaw := tc.unread == 0; ok && (g.malformed == wantRaw || (g.raw != nil) != wantRaw) {
			t.Errorf("%s: malformed %v with raw %v, want a group kept whole only when all of it was read", tc.name, g.malformed, g.raw != nil)
		}
		if ok && g.level() != 30 {
			t.Errorf("%s: level %d, want the 30 its Targets gives", tc.name, g.level())
		}
	}

	// A whole Tag with no Targets has the default scope.
	src, el := firstElement(t, encElement(idTag, artist))
	if g, unread, ok, err := parseTag(src, el, bits.NewDepth(8), testLimit); err != nil || !ok || unread != 0 || g.level() != 50 || !slices.Equal(tagNames(g), []string{"ARTIST"}) {
		t.Errorf("a whole Tag with no Targets: ok %v, tags %v, %d unread, level %d, err %v", ok, tagNames(g), unread, g.level(), err)
	}

	// A Tag that runs past what holds it, every child whole: read, and never kept as it is.
	raw := encElement(idTag, slices.Concat(targets, artist))
	raw[2]++
	src, el = firstElement(t, raw)
	g, unread, ok, err := parseTag(src, el, bits.NewDepth(8), testLimit)
	if err != nil || !ok || unread != 0 || !g.malformed || g.raw != nil || !slices.Equal(tagNames(g), []string{"ARTIST"}) {
		t.Errorf("a Tag past its parent: ok %v, tags %v, %d unread, malformed %v, raw %v, err %v", ok, tagNames(g), unread, g.malformed, g.raw != nil, err)
	}
}

// Every Tag of a Tags element counts toward its damage, and so do the bytes behind the last.
func TestParseTagsCountsDamage(t *testing.T) {
	artist, genre := simpleTagBytes("ARTIST", "A", ""), simpleTagBytes("GENRE", "G", "")
	whole := encElement(idTag, slices.Concat(encElement(idTargets, nil), artist))
	cut := encElement(idTag, slices.Concat(encElement(idTargets, nil), overrun(t, genre, 2)))
	long := slices.Clone(whole)
	long[2]++
	for _, tc := range []struct {
		name    string
		content []byte
		groups  int
		want    damage
	}{
		{"whole", slices.Concat(whole, whole), 2, damage{}},
		{"a SimpleTag past its Tag", slices.Concat(cut, whole), 2, damage{unread: int64(len(genre))}},
		{"bytes behind the last Tag", slices.Concat(whole, []byte{0, 0}), 1, damage{unread: 2}},
		{"a Tag past the Tags", slices.Concat(whole, long), 2, damage{resized: true}},
		{"a child of unknown size", slices.Concat(whole, []byte{byte(idVoid), 0xFF, 9}), 1, damage{unread: 3}},
	} {
		src, el := firstElement(t, encElement(idTags, tc.content))
		d := &doc{}
		if err := parseTags(src, el, bits.NewDepth(8), testLimit, d); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(d.groups) != tc.groups || d.tagsDamage != tc.want {
			t.Errorf("%s: %d groups, damage %+v; want %d, %+v", tc.name, len(d.groups), d.tagsDamage, tc.groups, tc.want)
		}
	}
}

func chapterStarts(chs []core.Chapter) []time.Duration {
	var out []time.Duration
	for _, c := range chs {
		out = append(out, c.Start)
	}
	return out
}

// An EditionEntry is read up to the first of its children that is no whole element, and a
// ChapterAtom is not read when a child of it or of one of its displays is none. What is left of
// a damaged edition is kept as an EditionEntry of its whole children. A nested atom is an
// element like any other: what it holds is not looked into.
func TestParseEditionReadsOnlyWholeElements(t *testing.T) {
	uid, flag := uintElement(idEditionUID, 77), uintElement(idEditionFlagDf, 1)
	one := renderChapterAtom([]byte{1}, core.Chapter{Start: time.Second, Title: "One"})
	two := renderChapterAtom([]byte{2}, core.Chapter{Start: 2 * time.Second, Title: "Two"})
	display := encElement(idChapDisplay, stringElement(idChapString, "x"))
	longDisplay := encElement(idChapDisplay, overrun(t, stringElement(idChapString, "long"), 1))
	atom := func(children ...[]byte) []byte {
		return encElement(idChapterAtom, slices.Concat(append([][]byte{uintElement(idChapTimeStart, uint64(3*time.Second))}, children...)...))
	}
	s := func(d ...time.Duration) []time.Duration { return d }
	for _, tc := range []struct {
		name    string
		content []byte
		starts  []time.Duration
		unread  int
		kept    []byte // the content of the EditionEntry kept, nil for none
	}{
		{"whole", slices.Concat(uid, flag, one, two), s(time.Second, 2*time.Second), 0, nil},
		{"an atom past the edition", slices.Concat(uid, flag, one, overrun(t, two, 1)), s(time.Second), len(two), slices.Concat(uid, flag, one)},
		{"bytes that are no element", slices.Concat(uid, one, []byte{0, 0, 0, 0}), s(time.Second), 4, slices.Concat(uid, one)},
		{"a child of unknown size ahead of everything", slices.Concat(bytes.Repeat([]byte{0xFF}, 8), flag, one), nil, 8 + len(flag) + len(one), nil},
		{"the default flag past the edition", slices.Concat(uid, overrun(t, flag, 2), one), nil, len(flag) + len(one), uid},
		{"an atom holding bytes that are no element", slices.Concat(atom([]byte{0, 0}), two), s(2 * time.Second), len(atom([]byte{0, 0})), two},
		{"such an atom between two whole ones", slices.Concat(one, atom([]byte{0, 0}), two), s(time.Second, 2*time.Second), len(atom([]byte{0, 0})), slices.Concat(one, two)},
		{"a display holding a child that runs past it", slices.Concat(one, atom(longDisplay)), s(time.Second), len(atom(longDisplay)), one},
		{"a second display holding a child that runs past it", slices.Concat(one, atom(display, longDisplay)), s(time.Second), len(atom(display, longDisplay)), one},
		{"a nested atom holding a display with such a child", slices.Concat(one, atom(atom(longDisplay))), s(time.Second, 3*time.Second), 0, nil},
		{"a nested atom past its parent", slices.Concat(atom(overrun(t, one, 1)), two), s(2 * time.Second), len(atom(one)), two},
		{"a display past its atom", slices.Concat(one, atom(overrun(t, display, 1))), s(time.Second), len(atom(display)), one},
		{"a second display holding bytes that are no element", slices.Concat(one, atom(display, encElement(idChapDisplay, []byte{0}))), s(time.Second), len(atom(display)) + 3, one},
	} {
		src, el := firstElement(t, encElement(idEditionEntry, tc.content))
		r, err := parseEdition(src, el, bits.NewDepth(8), testLimit)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !slices.Equal(chapterStarts(r.chapters), tc.starts) || r.unread != int64(tc.unread) || len(r.ed.startUIDs) != len(tc.starts) {
			t.Errorf("%s: starts %v (%d UIDs), %d unread; want %v, %d", tc.name, chapterStarts(r.chapters), len(r.ed.startUIDs), r.unread, tc.starts, tc.unread)
		}
		want := encElement(idEditionEntry, tc.kept)
		if tc.unread == 0 {
			want = encElement(idEditionEntry, tc.content)
		} else if tc.kept == nil {
			want = []byte{}
		}
		if r.ed.raw == nil || !bytes.Equal(r.ed.raw, want) {
			t.Errorf("%s: raw % x; want % x", tc.name, r.ed.raw, want)
		}
	}

	// An edition that runs past what holds it, every child whole and behind a CRC-32: kept at
	// its own size, the CRC-32 computed again.
	raw := masterElement(idEditionEntry, slices.Concat(uid, one), true)
	raw[2]++
	src, el := firstElement(t, raw)
	r, err := parseEdition(src, el, bits.NewDepth(8), testLimit)
	if want := masterElement(idEditionEntry, slices.Concat(uid, one), true); err != nil || len(r.chapters) != 1 || r.unread != 0 || !bytes.Equal(r.ed.raw, want) {
		t.Errorf("an edition past its parent: %d chapters, %d unread, raw % x, err %v; want raw % x", len(r.chapters), r.unread, r.ed.raw, err, want)
	}
}

// Every edition of a Chapters element counts toward its damage, and so do the bytes behind the
// last. A damaged edition that is not the default one is written back as its whole children.
func TestParseChaptersCountsDamage(t *testing.T) {
	one := renderChapterAtom([]byte{1}, core.Chapter{Start: time.Second, Title: "One"})
	two := renderChapterAtom([]byte{2}, core.Chapter{Start: 2 * time.Second, Title: "Two"})
	def := encElement(idEditionEntry, slices.Concat(uintElement(idEditionFlagDf, 1), one))
	other := encElement(idEditionEntry, slices.Concat(uintElement(idEditionUID, 5), two, overrun(t, one, 1)))
	long := slices.Clone(def)
	long[2]++
	for _, tc := range []struct {
		name     string
		content  []byte
		editions int
		want     damage
	}{
		{"whole", def, 1, damage{}},
		{"an atom past its edition", slices.Concat(other, def), 2, damage{unread: int64(len(one))}},
		{"bytes behind the last edition", slices.Concat(def, []byte{0, 0}), 1, damage{unread: 2}},
		{"an edition past the Chapters", long, 1, damage{resized: true}},
		{"a child of unknown size", slices.Concat(def, []byte{byte(idVoid), 0xFF, 9}), 1, damage{unread: 3}},
	} {
		src, el := firstElement(t, encElement(idChapters, tc.content))
		d := &doc{}
		chs, err := parseChapters(src, el, bits.NewDepth(8), testLimit, d)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(d.chapters.editions) != tc.editions || d.chapDamage != tc.want || !slices.Equal(chapterStarts(chs), []time.Duration{time.Second}) {
			t.Errorf("%s: %d editions, damage %+v, starts %v; want %d, %+v, the default edition's", tc.name, len(d.chapters.editions), d.chapDamage, chapterStarts(chs), tc.editions, tc.want)
		}
	}

	src, el := firstElement(t, encElement(idChapters, slices.Concat(other, def)))
	d := &doc{}
	if _, err := parseChapters(src, el, bits.NewDepth(8), testLimit, d); err != nil {
		t.Fatal(err)
	}
	if err := checkPreservable(d, changes{chapters: true}, nil); err != nil {
		t.Fatalf("checkPreservable: %v", err)
	}
	out := renderChapters(d, []core.Chapter{{Start: 9 * time.Second}})
	kept := encElement(idEditionEntry, slices.Concat(uintElement(idEditionUID, 5), two))
	if !bytes.Contains(out, kept) || bytes.Contains(out, overrun(t, one, 1)) {
		t.Errorf("rendered Chapters % x: want the other edition as % x", out, kept)
	}
	if got := chaptersFromRaw(out, bits.NewDepth(8), testLimit); len(got) != 1 || got[0].Start != 9*time.Second {
		t.Errorf("rendered Chapters read back as %+v, want the one new chapter", got)
	}
}

// A default edition whose kept children ran past it rendered chapters behind them, where no
// reader finds them, and the write went through. The plan check refuses such a result.
func TestSwallowedChaptersAreRefusedByThePlanCheck(t *testing.T) {
	d := &doc{chapters: &chapterDoc{editions: []chapterEdition{{prefix: bytes.Repeat([]byte{0xFF}, 8)}}}}
	want := []core.Chapter{{Start: 0}, {Start: time.Second}}
	got := chaptersFromRaw(renderChapters(d, want), bits.NewDepth(8), testLimit)
	if len(got) != 0 {
		t.Fatalf("%d chapters read behind a child of unknown size, want none", len(got))
	}
	err := core.CheckPlanResult(&core.Media{Chapters: want}, &core.Media{Chapters: got}, core.WriteReport{})
	if !errors.Is(err, waxerr.ErrInvalidData) {
		t.Errorf("CheckPlanResult = %v, want ErrInvalidData for 0 chapters planned of 2", err)
	}
}

// An Attachments element lists the AttachedFiles that are whole and counts the rest as damage.
func TestParseAttachmentsCountsDamage(t *testing.T) {
	file := func(name string, data []byte) []byte {
		return encElement(idAttached, slices.Concat(stringElement(idFileName, name), stringElement(idFileMime, "font/ttf"), data))
	}
	a, b := file("a.ttf", encElement(idFileData, []byte("aa"))), file("b.ttf", encElement(idFileData, []byte("bb")))
	cut := file("c.ttf", overrun(t, encElement(idFileData, []byte("cc")), 2))
	for _, tc := range []struct {
		name    string
		content []byte
		names   []string
		unread  int
	}{
		{"whole", slices.Concat(a, b), []string{"a.ttf", "b.ttf"}, 0},
		{"a FileData past its AttachedFile", slices.Concat(a, cut, b), []string{"a.ttf", "b.ttf"}, len(cut)},
		{"an AttachedFile past the Attachments", slices.Concat(a, overrun(t, b, 2)), []string{"a.ttf"}, len(b)},
		{"bytes that are no element", slices.Concat(a, []byte{0, 0}), []string{"a.ttf"}, 2},
		{"a child of unknown size", slices.Concat(a, []byte{byte(idVoid), 0xFF, 9}), []string{"a.ttf"}, 3},
	} {
		src, el := firstElement(t, encElement(idAttachments, tc.content))
		d := &doc{}
		if _, err := parseAttachments(src, el, bits.NewDepth(8), testLimit, d); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var names []string
		for _, at := range d.attachments {
			names = append(names, at.name)
			if at.raw == nil {
				t.Errorf("%s: %s was not captured", tc.name, at.name)
			}
		}
		if !slices.Equal(names, tc.names) || d.attachDamage != (damage{unread: int64(tc.unread)}) {
			t.Errorf("%s: files %v, damage %+v; want %v, %d unread", tc.name, names, d.attachDamage, tc.names, tc.unread)
		}
	}
}

// An EBML string ends at its first NUL. NULs behind it are padding; anything else is reported.
func TestReadStringStopsAtTheFirstNUL(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		cut       bool
	}{
		{"", "", false},
		{"Alpha", "Alpha", false},
		{"Alpha\x00", "Alpha", false},
		{"Alpha\x00\x00\x00", "Alpha", false},
		{"Alpha\x00Beta", "Alpha", true},
		{"Alpha\x00\x00Beta\x00", "Alpha", true},
		{"\x00Beta", "", true},
		{"\x00", "", false},
	} {
		src, el := firstElement(t, stringElement(idTagString, tc.raw))
		got, cut, err := readString(src, el, testLimit)
		if got != tc.want || cut != tc.cut || err != nil {
			t.Errorf("readString(%q) = %q, cut %v, %v; want %q, cut %v", tc.raw, got, cut, err, tc.want, tc.cut)
		}
	}
}

// Cues are not rebuilt from a child that runs past its parent, at any of the three levels: its
// bytes rendered again would reach over what follows them.
func TestCuesWithAnOverrunningChildAreNotRebuilt(t *testing.T) {
	whole := cueTrackPosBytes(1, 100, 2)
	for name, cues := range map[string][]byte{
		"a CuePoint past the Cues":                   encElement(idCues, slices.Concat(cuePointBytes(0, whole), overrun(t, cuePointBytes(1, whole), 1))),
		"a CueTrackPositions past its CuePoint":      encElement(idCues, cuePointBytes(0, whole, overrun(t, whole, 1))),
		"a CueRelativePosition past its positions":   encElement(idCues, cuePointBytes(0, encElement(idCueTrackPos, slices.Concat(encElement(idCueClusterPos, uintDataWidth(100, 2)), overrun(t, uintElement(idCueRelPos, 0x010203), 1))))),
		"a CueTime past its CuePoint, tracks behind": encElement(idCues, encElement(idCuePoint, slices.Concat(whole, overrun(t, uintElement(idCueTime, 0x010203), 1)))),
	} {
		ci := cuesFromRaw(cues, 0, bits.NewDepth(64), maxElement)
		if _, ok := buildCuePoints(ci); ok {
			t.Errorf("%s: the Cues are taken for a rebuild", name)
		}
	}
	ci := cuesFromRaw(encElement(idCues, cuePointBytes(0, whole)), 0, bits.NewDepth(64), maxElement)
	if pts, ok := buildCuePoints(ci); !ok || len(pts) != 1 {
		t.Errorf("whole Cues: %d points, ok %v", len(pts), ok)
	}
}

// A FLAC CodecPrivate fills only what the track lacks, and only when it starts with the marker
// and a STREAMINFO block.
func TestApplyFLACCodecPrivate(t *testing.T) {
	// 44100 Hz, 2 channels, 16 bits.
	info := make([]byte, 34)
	info[10], info[11], info[12], info[13] = 0x0A, 0xC4, 0x42, 0xF0
	private := slices.Concat([]byte("fLaC"), []byte{0x80, 0, 0, 34}, info)
	notInfo := slices.Clone(private)
	notInfo[4] = 0x84
	for _, tc := range []struct {
		name       string
		private    []byte
		have, want core.AudioTrack
	}{
		{"nothing stated", private, core.AudioTrack{}, core.AudioTrack{SampleRate: 44100, Channels: 2, BitsPerSample: 16}},
		{"a rate and a channel count stated", private, core.AudioTrack{SampleRate: 48000, Channels: 6}, core.AudioTrack{SampleRate: 48000, Channels: 6, BitsPerSample: 16}},
		{"a depth stated", private, core.AudioTrack{BitsPerSample: 24}, core.AudioTrack{SampleRate: 44100, Channels: 2, BitsPerSample: 24}},
		{"no marker", slices.Concat([]byte("FLAC"), private[4:]), core.AudioTrack{}, core.AudioTrack{}},
		{"another block first", notInfo, core.AudioTrack{}, core.AudioTrack{}},
		{"cut short", private[:41], core.AudioTrack{}, core.AudioTrack{}},
	} {
		got := tc.have
		applyFLACCodecPrivate(&got, tc.private)
		if got != tc.want {
			t.Errorf("%s: %d Hz, %d ch, %d-bit; want %d Hz, %d ch, %d-bit", tc.name, got.SampleRate, got.Channels, got.BitsPerSample, tc.want.SampleRate, tc.want.Channels, tc.want.BitsPerSample)
		}
	}
}

// The output Segment declares what the source's declared past the file, less the share of a last
// child the output does not end with as it was.
func TestSegShort(t *testing.T) {
	// A Segment of 100 declared bytes of which the file holds 70; its last child, at 60, declares
	// 25 of the 30 missing.
	cut := &writeBase{segDataStart: 10, segDataEnd: 80, segClamped: true, segDeclared: 100, lastShort: 25,
		children: []l1elem{{id: idCluster, start: 10}, {id: idTags, start: 60}}}
	boundary := &writeBase{segDataStart: 10, segDataEnd: 80, segClamped: true, segDeclared: 100}
	whole := &writeBase{segDataStart: 10, segDataEnd: 110, segDeclared: 100, children: []l1elem{{id: idCluster, start: 10}}}
	for _, tc := range []struct {
		name  string
		wb    *writeBase
		items []outItem
		want  int64
	}{
		{"a whole Segment", whole, []outItem{{origStart: 10}}, 0},
		{"cut between children", boundary, nil, 30},
		{"the cut child copied", cut, []outItem{{origStart: 10}, {origStart: 60}}, 30},
		{"the cut child patched where it stands", cut, []outItem{{origStart: 10}, {origStart: 60, lit: []byte{1}}}, 30},
		{"the cut child rendered anew", cut, []outItem{{origStart: 10}, litItem(idTags, []byte{1}, 60)}, 5},
		{"the cut child left out", cut, []outItem{{origStart: 10}}, 5},
		{"nothing written", cut, nil, 5},
	} {
		if got := segShort(tc.wb, tc.items); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A chapter title read up to a NUL is counted where the chapter is listed: not for an atom that
// is no whole element, a nested atom, or an edition that is not the default one.
func TestChapterTitleCutsCountListedChaptersOnly(t *testing.T) {
	titled := func(start time.Duration, title string, more ...[]byte) []byte {
		return encElement(idChapterAtom, slices.Concat(append([][]byte{uintElement(idChapTimeStart, uint64(start)),
			encElement(idChapDisplay, stringElement(idChapString, title))}, more...)...))
	}
	edition := func(def bool, atoms ...[]byte) []byte {
		flag := uintElement(idEditionFlagDf, 0)
		if def {
			flag = uintElement(idEditionFlagDf, 1)
		}
		return encElement(idEditionEntry, slices.Concat(append([][]byte{flag}, atoms...)...))
	}
	for _, tc := range []struct {
		name    string
		content []byte
		want    int
	}{
		{"a listed chapter", edition(true, titled(0, "A\x00B")), 1},
		{"an atom that is no whole element", edition(true, titled(0, "A\x00B", overrun(t, uintElement(idChapTimeEnd, 0x010203), 1))), 0},
		{"a nested atom", edition(true, titled(0, "A", titled(time.Second, "N\x00x"))), 0},
		{"another edition", slices.Concat(edition(true, titled(0, "A")), edition(false, titled(0, "O\x00x"))), 0},
		{"an edition ahead of the default one", slices.Concat(edition(false, titled(0, "O\x00x")), edition(true, titled(0, "A"))), 0},
	} {
		src, el := firstElement(t, encElement(idChapters, tc.content))
		d := &doc{}
		if _, err := parseChapters(src, el, bits.NewDepth(8), testLimit, d); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if d.cuts.chapters != tc.want {
			t.Errorf("%s: %d titles counted, want %d", tc.name, d.cuts.chapters, tc.want)
		}
	}
}

// An edition that is not the default one keeps what was read of it through a chapter edit: one
// that runs past the Chapters element with no ChapterAtom in it too. One of which nothing was
// read goes, without a refusal.
func TestDamagedEditionKeepsItsWholeChildren(t *testing.T) {
	def := encElement(idEditionEntry, slices.Concat(uintElement(idEditionFlagDf, 1), renderChapterAtom([]byte{1}, core.Chapter{Start: time.Second})))
	bare := encElement(idEditionEntry, uintElement(idEditionUID, 6))
	long := slices.Clone(bare)
	long[2]++
	cutAtom := encElement(idEditionEntry, slices.Concat(uintElement(idEditionUID, 6), overrun(t, renderChapterAtom([]byte{2}, core.Chapter{Start: time.Second, Title: "Two"}), 1)))
	unread := encElement(idEditionEntry, overrun(t, uintElement(idEditionUID, 0x010203), 2))
	for _, tc := range []struct {
		name    string
		content []byte
		kept    bool
	}{
		{"an edition past the Chapters with no atom", slices.Concat(def, long), true},
		{"an edition whose only atom runs past it", slices.Concat(def, cutAtom), true},
		{"an edition of which nothing is whole", slices.Concat(def, unread), false},
	} {
		src, el := firstElement(t, encElement(idChapters, tc.content))
		d := &doc{}
		if _, err := parseChapters(src, el, bits.NewDepth(8), testLimit, d); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if err := checkPreservable(d, changes{chapters: true}, nil); err != nil {
			t.Errorf("%s: checkPreservable: %v", tc.name, err)
		}
		out := renderChapters(d, []core.Chapter{{Start: 9 * time.Second}})
		if got := bytes.Contains(out, bare); got != tc.kept {
			t.Errorf("%s: the edition is in the rendered Chapters: %v, want %v (% x)", tc.name, got, tc.kept, out)
		}
		if got := chaptersFromRaw(out, bits.NewDepth(8), testLimit); len(got) != 1 {
			t.Errorf("%s: %d chapters read back, want 1", tc.name, len(got))
		}
	}
}

// A Targets whose children the depth limit keeps out of reach leaves the scope unknown.
func TestParseTagUnderADepthLimit(t *testing.T) {
	raw := encElement(idTag, encElement(idTargets, slices.Concat(uintElement(idTgtTypeVal, 30), uintElement(idTagTrackUID, 5))))
	src, el := firstElement(t, raw)
	g, unread, ok, err := parseTag(src, el, bits.NewDepth(1), testLimit)
	if err != nil || ok || unread != int64(len(raw)) {
		t.Errorf("ok %v, scope %v, %d unread, err %v; want the Tag unread", ok, g.scope, unread, err)
	}
}

// An index rebuilt at the size it had takes one layout pass where the Segment is whole: only a
// Segment that declares bytes it lacks is sized by whether its last child was rendered anew.
func TestRebuiltIndexOfUnchangedSizeTakesOnePass(t *testing.T) {
	const segData = 12
	cluster := make([]byte, 40)
	// A nested CRC-32 forces a rebuild, which comes out byte for byte the same.
	cues := cuesBytes(false, encElement(idCuePoint, cat(uintElement(idCueTime, 0),
		masterElement(idCueTrackPos, cat(uintElement(idCueTrack, 1), uintElement(idCueClusterPos, 0)), true))))
	at := int64(segData + len(cluster))
	wb := &writeBase{
		size: at + int64(len(cues)), segSizeOff: 4, segSizeLen: 8, segDataStart: segData, segDataEnd: at + int64(len(cues)),
		segDeclared: uint64(len(cluster) + len(cues)), clusterStart: segData,
		children: []l1elem{{id: idCluster, start: segData, dataEnd: at}, {id: idCues, start: at, dataEnd: at + int64(len(cues))}},
		cues:     cuesFromRaw(cues, at, bits.NewDepth(64), maxElement),
	}
	if !wb.cues.hasNestedCRC {
		t.Fatal("setup: the Cues hold no nested CRC-32")
	}
	items := []outItem{copyItem(wb.children[0]), copyItem(wb.children[1])}
	lay, iters, ok := resolveShiftLayout(wb, items, -1, 1)
	if !ok || iters != 1 || lay.size != wb.size || !bytes.Equal(items[1].lit, cues) {
		t.Errorf("ok %v after %d passes, %d bytes for %d, Cues % x; want the same Cues after one pass", ok, iters, lay.size, wb.size, items[1].lit)
	}
}

// lyingSource holds fewer bytes than it says, as a file that shrinks under a parse does.
type lyingSource struct {
	core.ReaderAtSized
	size int64
}

func (s lyingSource) Size() int64 { return s.size }

// An edition whose bytes could not be read to their end is not taken for one of which nothing
// is whole: a chapter edit is refused, as it has nothing to copy.
func TestEditionCutByAShortReadIsNotDropped(t *testing.T) {
	def := encElement(idEditionEntry, slices.Concat(uintElement(idEditionFlagDf, 1), renderChapterAtom([]byte{1}, core.Chapter{Start: time.Second})))
	other := encElement(idEditionEntry, slices.Concat(uintElement(idEditionUID, 6), overrun(t, renderChapterAtom([]byte{2}, core.Chapter{Start: time.Second, Title: "Two"}), 1)))
	raw := encElement(idChapters, slices.Concat(def, other))
	src := lyingSource{core.BytesSource(raw[:len(raw)-1]), int64(len(raw))}
	el, ok, err := readElement(src, 0, src.Size(), testLimit)
	if !ok || err != nil {
		t.Fatalf("readElement: ok=%v err=%v", ok, err)
	}
	d := &doc{}
	if _, err := parseChapters(src, el, bits.NewDepth(8), testLimit, d); err != nil {
		t.Fatal(err)
	}
	if err := checkPreservable(d, changes{chapters: true}, nil); err == nil {
		t.Error("a chapter edit is planned over an edition whose bytes were not read")
	}
}
