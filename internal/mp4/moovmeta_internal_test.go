package mp4

import (
	"context"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
)

func parseTestDoc(t *testing.T, data []byte) *doc {
	t.Helper()
	m, err := parse(context.Background(), core.BytesSource(data), core.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return m.Native.(*doc)
}

// twoStores is a file with an iTunes store under moov.udta.meta and a keys store under moov.meta.
func twoStores() []byte {
	udta := testfile.MP4Atom("udta", testfile.MP4Meta(testfile.MP4HdlrMdir(), testfile.MP4Ilst(testfile.MP4Text("\xa9nam", "U"))))
	meta := testfile.MP4Meta(testfile.MP4HdlrMdta(), testfile.MP4Keys("title"), testfile.MP4Ilst(testfile.MP4KeyItem(1, "M")))
	return testfile.MP4TracksFile(nil, func(audio, _, _ uint32) []byte {
		return slices.Concat(testfile.MP4SoundTrak(1, audio), udta, meta)
	})
}

func TestStoreUnderMoovIsTheOneReadWithoutAnother(t *testing.T) {
	meta := testfile.MP4Meta(testfile.MP4HdlrMdta(), testfile.MP4Keys("title"), testfile.MP4Ilst(testfile.MP4KeyItem(1, "M")))
	bare := testfile.MP4Atom("udta", testfile.MP4Meta(testfile.MP4HdlrMdir()))
	for name, kids := range map[string][]byte{"alone": meta, "beside a udta meta with no ilst": slices.Concat(bare, meta)} {
		d := parseTestDoc(t, testfile.MP4TracksFile(nil, func(audio, _, _ uint32) []byte {
			return slices.Concat(testfile.MP4SoundTrak(1, audio), kids)
		}))
		if !d.metaAtMoov || d.second != nil || d.metaHandler != mdtaHandler || len(d.items) != 1 {
			t.Errorf("%s: metaAtMoov %v, second %v, handler %q, %d items", name, d.metaAtMoov, d.second != nil, d.metaHandler, len(d.items))
		}
		if got := d.tagAncestors(); len(got) != 2 || got[1].id() != "meta" {
			t.Errorf("%s: ancestors %v", name, got)
		}
		kinds := describeKinds(d)
		if !slices.Contains(kinds, "moov.meta.ilst") || slices.Contains(kinds, "moov.udta.meta.ilst") {
			t.Errorf("%s: native view %v", name, kinds)
		}
	}
}

func TestSecondStoreIsDescribedAndCloned(t *testing.T) {
	d := parseTestDoc(t, twoStores())
	if d.metaAtMoov || d.second == nil || d.metaHandler != "mdir" || d.second.handler != mdtaHandler {
		t.Fatalf("metaAtMoov %v, second %v, handler %q", d.metaAtMoov, d.second != nil, d.metaHandler)
	}
	if got := d.tagAncestors(); len(got) != 3 || got[1].id() != "udta" {
		t.Errorf("ancestors %v", got)
	}
	kinds := describeKinds(d)
	first, second := slices.Index(kinds, "moov.udta.meta.ilst"), slices.Index(kinds, "moov.meta.ilst")
	if first < 0 || second < first || kinds[second+1] != "  title" {
		t.Errorf("native view %v", kinds)
	}
	c := d.Clone().(*doc)
	c.second.items[0].payload[0] ^= 0xFF
	c.second.keyNames[0] = "changed"
	c.second.ilst.offset++
	if d.second.items[0].payload[0] == c.second.items[0].payload[0] || d.second.keyNames[0] != "title" || d.second.ilst.offset == c.second.ilst.offset {
		t.Error("a clone shares the second store with its source")
	}
}

func describeKinds(d *doc) []string {
	var kinds []string
	for _, e := range d.Describe() {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

func TestShiftsAtAndPast(t *testing.T) {
	// Ten bytes go in at 100, and the twenty at 200 become five.
	moved := newShifts([]edit{{off: 200, oldLen: 20, lit: make([]byte, 5)}, {off: 100, lit: make([]byte, 10)}})
	for _, c := range []struct{ pos, at, past int64 }{
		{99, 0, 0}, {100, 0, 10}, {101, 10, 10}, {200, 10, 10}, {201, -5, 10}, {219, -5, 10}, {220, -5, -5}, {1000, -5, -5},
	} {
		if at, past := moved.at(c.pos), moved.past(c.pos); at != c.at || past != c.past {
			t.Errorf("pos %d: at %d past %d, want %d and %d", c.pos, at, past, c.at, c.past)
		}
	}
	// An insertion and a replacement at one offset: the atom there follows the insertion only.
	same := newShifts([]edit{{off: 50, oldLen: 8, lit: make([]byte, 12)}, {off: 50, lit: make([]byte, 3)}})
	if got := same.past(50); got != 3 {
		t.Errorf("past(50) = %d, want 3", got)
	}
	if got := same.past(58); got != 7 {
		t.Errorf("past(58) = %d, want 7", got)
	}
}

func TestMergeRanges(t *testing.T) {
	got := mergeRanges([][2]int64{{300, 400}, {0, 100}, {100, 150}, {10, 20}, {140, 200}, {250, 260}, {400, 400}})
	if want := [][2]int64{{0, 200}, {250, 260}, {300, 400}}; !slices.Equal(got, want) {
		t.Errorf("merged %v, want %v", got, want)
	}
	if got := mergeRanges(nil); len(got) != 0 {
		t.Errorf("merged nothing into %v", got)
	}
}

// A chunk range moves as the writer moves its chunk offset: by the edits that start ahead of it,
// so bytes put in exactly where it starts leave it.
func TestShiftSoundChunksFollowsTheChunkOffsets(t *testing.T) {
	base := &doc{sampleExtent: true, soundChunks: [][2]int64{{100, 200}, {300, 350}}}
	edits := []edit{{off: 100, lit: make([]byte, 10)}, {off: 250, oldLen: 20, lit: make([]byte, 5)}}
	nd := &doc{}
	shiftSoundChunks(nd, base, newShifts(edits))
	if want := [][2]int64{{100, 200}, {295, 345}}; !nd.sampleExtent || !slices.Equal(nd.soundChunks, want) {
		t.Errorf("shifted to %v (sampleExtent %v), want %v", nd.soundChunks, nd.sampleExtent, want)
	}
	for _, e := range []uint64{100, 300} {
		if got, _ := shiftOffset(e, 100, 10); (e == 100) != (got == 100) {
			t.Errorf("the writer moves the chunk offset %d to %d", e, got)
		}
	}
}
