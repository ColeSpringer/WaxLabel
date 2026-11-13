package tail

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/ape"
	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// scanTrailer scans the audio stub followed by tail as a stream whose APE tag is its store.
func scanTrailer(t *testing.T, tail ...[]byte) (Trailer, []byte) {
	t.Helper()
	data := slices.Concat(append([][]byte{make([]byte, stubLen)}, tail...)...)
	l, err := Scan(core.BytesSource(data), int64(len(data)), stubFloor, testLimit, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	return Trailer{l}, data
}

// The native view says what each item's flags make of it, that a tag is marked read-only, and how
// many item bytes went unread.
func TestTrailerDescribeItemTypes(t *testing.T) {
	const readOnly = 1
	raw := apeTag(t, true,
		ape.Item{Key: "Title", Value: "T"},
		ape.Item{Key: "Locked", Value: "kept", Flags: readOnly},
		ape.Item{Key: "Blob", Data: []byte{1, 2}, Flags: 1 << 1},
		ape.Item{Key: "Link", Data: []byte("http://example.com/a"), Flags: 2 << 1},
		ape.Item{Key: "Odd", Data: []byte{3}, Flags: 3<<1 | readOnly},
	)
	tr, _ := scanTrailer(t, raw)
	notes := map[string]string{}
	for _, e := range tr.Describe(nil, 0, 0, "test audio", "codec") {
		notes[strings.TrimSpace(e.Kind)] = e.Note
	}
	for kind, want := range map[string]string{
		"APEv2":  "5 items",
		"Title":  "",
		"Locked": "read-only",
		"Blob":   "binary",
		"Link":   "external locator",
		"Odd":    "reserved type 3, read-only",
	} {
		if got, ok := notes[kind]; !ok || got != want {
			t.Errorf("%s: note = %q (listed %v), want %q", kind, got, ok, want)
		}
	}

	// A tag marked read-only, its footer counting two of the five items.
	marked := put32(put32(put32(raw, 20, 0xA0000001), -12, 0x80000001), -16, 2)
	tr, _ = scanTrailer(t, marked)
	entries := tr.Describe(nil, 0, 0, "test audio", "codec")
	if got, want := entries[1].Note, "2 items, read-only, 61 unparsed byte(s)"; got != want {
		t.Errorf("marked tag: note = %q, want %q", got, want)
	}
}

// A store whose item bytes run past the items its footer counts is reported, and a rewrite that
// drops them says so; the flags the tag carried go into the rewritten tag.
func TestTrailerUnreadTailAndFlags(t *testing.T) {
	raw := apeTag(t, true, ape.Item{Key: "Title", Value: "T"}, ape.Item{Key: "Artist", Value: "A"}, ape.Item{Key: "Album", Value: "B"})
	marked := put32(put32(put32(put32(raw, 20, 0xA0000001), -12, 0x80000001), 16, 1), -16, 1)
	tr, _ := scanTrailer(t, marked)
	store := tr.Tag()
	if store == nil || len(store.Items) != 1 || store.UnreadTail == 0 || !store.ReadOnly() {
		t.Fatalf("store = %+v, want one item, an unread tail and the read-only flag", store)
	}
	ws := tr.Warnings(subject)
	if len(ws) != 1 || ws[0].Code != core.WarnMalformedTagEntry || !strings.Contains(ws[0].Message, "31 byte(s) past the 1 item(s) read") {
		t.Fatalf("warnings = %+v, want one malformed-tag-entry counting the bytes and the items read", ws)
	}
	// A whole tag raises none.
	if whole, _ := scanTrailer(t, raw); len(whole.Warnings(subject)) != 0 {
		t.Errorf("whole tag: warnings = %+v, want none", whole.Warnings(subject))
	}

	base := tag.NewTagSet()
	base.Add(tag.Title, "T")
	edited := base.Clone()
	edited.Set(tag.Genre, "Rock")
	tp, err := RebuildTrailer(tr, base, edited, nil, Rewrite{Tags: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tp.Items) != 2 || tp.UnreadDropped != store.UnreadTail {
		t.Errorf("items = %d, unread dropped = %d, want 2 and %d", len(tp.Items), tp.UnreadDropped, store.UnreadTail)
	}
	if !slices.Contains(tp.Operations, "APEv2 unread item bytes drop (31 bytes)") {
		t.Errorf("operations = %q, want the unread bytes named", tp.Operations)
	}
	got, ok, err := ape.ParseAt(core.BytesSource(tp.Bytes), int64(len(tp.Bytes)), testLimit, 0)
	if err != nil || !ok {
		t.Fatalf("rewritten tag: ok=%v err=%v", ok, err)
	}
	if got.Flags != 0x80000001 || got.UnreadTail != 0 || len(got.Items) != 2 {
		t.Errorf("rewritten tag: flags %#x, unread %d, %d items, want 0x80000001, 0 and 2", got.Flags, got.UnreadTail, len(got.Items))
	}
	if head := tp.Bytes[20:24]; head[0] != 1 || head[3] != 0xA0 {
		t.Errorf("rewritten header flags = % x, want the read-only bit beside has-header and is-header", head)
	}
}

// A duplicate tag's unread item bytes go with it on a rewrite, so they count as content the file
// holds nowhere else.
func TestDuplicateTagUnreadBytesAreItsContent(t *testing.T) {
	dup := apeTag(t, true, ape.Item{Key: "Title", Value: "T"}, ape.Item{Key: "Artist", Value: "A"})
	tr, _ := scanTrailer(t, put32(put32(dup, 16, 1), -16, 1), titled(t, true, "T"))
	if n := len(tr.Regions); n != 2 {
		t.Fatalf("regions = %d, want the duplicate and the store", n)
	}
	m := &core.Media{}
	got := tr.duplicates(tr.Items(), m, m)
	if len(got) != 1 || len(got[0].Other) != 1 || got[0].Other[0].N != 16 || !strings.Contains(got[0].Other[0].Kind, "unread") {
		t.Errorf("duplicates = %+v, want the duplicate's 16 unread bytes counted", got)
	}
	// A whole duplicate that repeats the store holds nothing of its own.
	tr, _ = scanTrailer(t, titled(t, true, "T"), titled(t, true, "T"))
	if got := tr.duplicates(tr.Items(), m, m); len(got) != 1 || len(got[0].Other) != 0 {
		t.Errorf("duplicates = %+v, want nothing counted for a whole duplicate", got)
	}
}

// A legacy strip that changes neither tags nor pictures copies the store as the file holds it: its
// item bytes past the footer's count stay, and nothing is reported dropped. An edit beside the strip
// renders the store anew.
func TestStripOnlyCopiesTheStore(t *testing.T) {
	raw := apeTag(t, true, ape.Item{Key: "Title", Value: "T"}, ape.Item{Key: "Artist", Value: "A"})
	store := put32(put32(raw, 16, 1), -16, 1)
	tr, _ := scanTrailer(t, store, v1("Legacy"))
	base := tag.NewTagSet()
	base.Add(tag.Title, "T")

	tp, err := RebuildTrailer(tr, base, base, nil, Rewrite{StripLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if tp.UnreadDropped != 0 || slices.ContainsFunc(tp.Operations, func(op string) bool { return strings.Contains(op, "unread") }) {
		t.Errorf("unread dropped = %d, operations %q, want the unread bytes left alone", tp.UnreadDropped, tp.Operations)
	}
	if got, want := tp.Segments(), []bits.Segment{bits.Copy(stubLen, int64(len(store)))}; !reflect.DeepEqual(got, want) {
		t.Errorf("segments = %+v, want the store copied as it is: %+v", got, want)
	}
	if len(tp.Items) != 1 {
		t.Errorf("items = %d, want the one the store reads", len(tp.Items))
	}

	edited := base.Clone()
	edited.Set(tag.Genre, "Rock")
	tp, err = RebuildTrailer(tr, base, edited, nil, Rewrite{Tags: true, StripLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if tp.UnreadDropped == 0 || tp.Bytes == nil {
		t.Errorf("an edit beside the strip: unread dropped = %d, rendered = %v, want the store rendered anew", tp.UnreadDropped, tp.Bytes != nil)
	}
}

// A strip that edits nothing still moves the junk behind the store ahead of it, and says so; a
// store with no items and nothing unread is dropped, not copied.
func TestStripOnlyMovesJunkAndDropsAnEmptyStore(t *testing.T) {
	store := titled(t, true, "T")
	tr, _ := scanTrailer(t, store, junk(40), v1("Legacy"))
	base := tag.NewTagSet()
	base.Add(tag.Title, "T")
	tp, err := RebuildTrailer(tr, base, base, nil, Rewrite{StripLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tp.Operations, "trailing bytes moved ahead of the APE tag (40 bytes)") {
		t.Errorf("operations = %q, want the moved junk named", tp.Operations)
	}
	n := int64(len(store))
	if got, want := tp.Segments(), []bits.Segment{bits.Copy(stubLen+n, 40), bits.Copy(stubLen, n)}; !reflect.DeepEqual(got, want) {
		t.Errorf("segments = %+v, want the junk, then the store as it is: %+v", got, want)
	}

	tr, _ = scanTrailer(t, apeTag(t, true), v1("Legacy"))
	if tr.Tag() == nil || len(tr.Items()) != 0 {
		t.Fatalf("store = %+v, want an empty tag read", tr.Tag())
	}
	tp, err = RebuildTrailer(tr, tag.NewTagSet(), tag.NewTagSet(), nil, Rewrite{StripLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tp.Segments()) != 0 || !slices.Contains(tp.Operations, "APEv2 drop (no items remain)") {
		t.Errorf("segments = %+v, operations %q, want the empty store dropped", tp.Segments(), tp.Operations)
	}
}

// A clear of the pictures removes the cover items that do not decode with the ones that do, and
// says how many; without it they stay.
func TestClearCoversRemovesUndecodableCoverItems(t *testing.T) {
	cover := ape.Item{Key: "Cover Art (Front)", Data: append([]byte("f.png\x00"), testfile.PNG()...), Flags: 2}
	bad := ape.Item{Key: "Cover Art (Booklet)", Data: []byte("no file name"), Flags: 2}
	tr, _ := scanTrailer(t, apeTag(t, true, ape.Item{Key: "Title", Value: "T"}, cover, bad, ape.Item{Key: "Blob", Data: []byte{1}, Flags: 2}))
	base := tag.NewTagSet()
	base.Add(tag.Title, "T")
	names := func(tp TrailerPlan) []string {
		var out []string
		for _, it := range tp.Items {
			out = append(out, it.Key)
		}
		return out
	}

	tp, err := RebuildTrailer(tr, base, base, nil, Rewrite{Pictures: true, ClearCovers: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(tp); !slices.Equal(got, []string{"Title", "Blob"}) || !slices.Contains(tp.Operations, "undecodable Cover Art items removed: 1") {
		t.Errorf("items = %q, operations %q, want the title and the plain binary item, the removal reported", got, tp.Operations)
	}
	tp, err = RebuildTrailer(tr, base, base, nil, Rewrite{Pictures: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(tp); !slices.Equal(got, []string{"Title", "Cover Art (Booklet)", "Blob"}) {
		t.Errorf("without the clear: items = %q, want the undecodable cover kept", got)
	}
}

// The store is copied as the file holds it whenever the rebuild leaves its items as they are: an
// edit the writer refuses, made beside a legacy strip, drops none of the item bytes it did not
// read and reports no rewrite of the tag.
func TestRefusedEditBesideAStripCopiesTheStore(t *testing.T) {
	raw := apeTag(t, true, ape.Item{Key: "Title", Value: "T"}, ape.Item{Key: "Artist", Value: "A"})
	store := put32(put32(raw, 16, 1), -16, 1)
	tr, _ := scanTrailer(t, store, v1("Legacy"))
	base := tag.NewTagSet()
	base.Add(tag.Title, "T")
	edited := base.Clone()
	edited.Set("X", "1") // a name no item can have

	tp, err := RebuildTrailer(tr, base, edited, nil, Rewrite{Tags: true, StripLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tp.Rebuild.InvalidNames) != 1 {
		t.Fatalf("rebuild = %+v, want the set of X refused", tp.Rebuild)
	}
	if tp.UnreadDropped != 0 || !slices.Equal(tp.Operations, []string{"trailing ID3v1 strip"}) {
		t.Errorf("unread dropped = %d, operations %q, want the strip alone", tp.UnreadDropped, tp.Operations)
	}
	if got, want := tp.Segments(), []bits.Segment{bits.Copy(stubLen, int64(len(store)))}; !reflect.DeepEqual(got, want) {
		t.Errorf("segments = %+v, want the store copied as it is: %+v", got, want)
	}
}

// A store the element limit cut short is copied by a write that leaves its items alone, the items
// that were not read with it; an edit of it is refused.
func TestTruncatedStoreIsCopiedNotEdited(t *testing.T) {
	store := apeTag(t, true, ape.Item{Key: "Title", Value: "T"}, ape.Item{Key: "Artist", Value: "A"}, ape.Item{Key: "Album", Value: "B"})
	data := slices.Concat(make([]byte, stubLen), store, v1("Legacy"))
	l, err := Scan(core.BytesSource(data), int64(len(data)), stubFloor, testLimit, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := Trailer{l}
	if s := tr.Tag(); s == nil || !s.Truncated || len(s.Items) != 2 {
		t.Fatalf("store = %+v, want two of its three items read", s)
	}
	base := tag.NewTagSet()
	base.Add(tag.Title, "T")
	base.Add(tag.Artist, "A")

	tp, err := RebuildTrailer(tr, base, base, nil, Rewrite{StripLegacy: true})
	if err != nil {
		t.Fatalf("a strip that leaves the store alone: %v", err)
	}
	if got, want := tp.Segments(), []bits.Segment{bits.Copy(stubLen, int64(len(store)))}; !reflect.DeepEqual(got, want) {
		t.Errorf("segments = %+v, want the store copied as it is: %+v", got, want)
	}
	edited := base.Clone()
	edited.Set(tag.Genre, "Rock")
	if _, err := RebuildTrailer(tr, base, edited, nil, Rewrite{Tags: true}); !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Errorf("an edit of the store: err = %v, want ErrSizeTooLarge", err)
	}
}
