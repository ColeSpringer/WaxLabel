package wav

import (
	"encoding/binary"
	"slices"
	"testing"
)

// infoItemRaw renders one INFO item exactly as a writer would, with pad controlling
// whether the word-alignment byte after an odd-size value is emitted. Omitting it is the
// malformation these tests are about, so it is a parameter rather than a fixed rule.
func infoItemRaw(id, value string, pad bool) []byte {
	val := append([]byte(value), 0) // ZSTR terminator
	sz := make([]byte, 4)
	binary.LittleEndian.PutUint32(sz, uint32(len(val)))
	out := slices.Concat([]byte(id), sz, val)
	if pad && len(val)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

// infoBody assembles a LIST body from pre-rendered items.
func infoBody(items ...[]byte) []byte { return append([]byte("INFO"), slices.Concat(items...)...) }

// TestParseInfoResynchronizesOverMissingPad: a writer that omits the pad byte after an
// odd-size item desynchronizes every item after it. The walk used to stop on the garbage
// with no error and no signal, and the next rewrite then destroyed everything past that
// point; it now steps back to the unpadded position and reads the rest.
func TestParseInfoResynchronizesOverMissingPad(t *testing.T) {
	body := infoBody(
		infoItemRaw("INAM", "Song", false), // odd-size value with no pad: the desync
		infoItemRaw("IPRD", "Album", true),
		infoItemRaw("ICMT", "Note", true),
	)
	items, unread, padRescued, err := parseInfo(body, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3: %v", len(items), items)
	}
	for i, want := range []string{"INAM", "IPRD", "ICMT"} {
		if items[i].id4() != want {
			t.Errorf("item %d = %q, want %q", i, items[i].id4(), want)
		}
	}
	if !padRescued {
		t.Error("padRescued = false, want true: the fallback is what recovered the later items")
	}
	if unread != 0 {
		t.Errorf("unread = %d, want 0 (the whole body was read)", unread)
	}
}

// TestParseInfoKeepsPaddedBranch: a well-formed list must take the spec-correct padded
// position, so nothing about ordinary files moves and no file gains a warning.
func TestParseInfoKeepsPaddedBranch(t *testing.T) {
	body := infoBody(
		infoItemRaw("INAM", "Song", true), // odd-size value, padded
		infoItemRaw("IART", "Artist", true),
	)
	items, unread, padRescued, err := parseInfo(body, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].text() != "Song" || items[1].text() != "Artist" {
		t.Fatalf("items = %v, want the two padded items", items)
	}
	if padRescued {
		t.Error("a well-formed list must not report a rescue")
	}
	if unread != 0 {
		t.Errorf("unread = %d, want 0", unread)
	}
}

// TestParseInfoFinalUnpaddedItemStaysQuiet: a list whose sole odd item is the last one,
// unpadded, parses completely and loses nothing, so it must not gain a warning for a byte
// the next rewrite adds anyway.
func TestParseInfoFinalUnpaddedItemStaysQuiet(t *testing.T) {
	body := infoBody(infoItemRaw("IART", "Artist", true), infoItemRaw("INAM", "Song", false))
	items, unread, padRescued, err := parseInfo(body, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if padRescued {
		t.Error("nothing was rescued: the unpadded item is the last one")
	}
	if unread != 0 {
		t.Errorf("unread = %d, want 0 (no unread tail)", unread)
	}
}

// TestParseInfoReportsUnreadableTail: a region neither candidate position can read as an
// item is reported through consumed, so the caller can warn rather than let the rewrite
// destroy it in silence. The tail here is too short to hold another item header.
func TestParseInfoReportsUnreadableTail(t *testing.T) {
	junk := []byte{0x01, 0x02, 0x03}
	body := append(infoBody(infoItemRaw("INAM", "Title", true)), junk...)
	items, unread, _, err := parseInfo(body, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if unread != len(junk) {
		t.Errorf("unread = %d, want %d (the junk is unread)", unread, len(junk))
	}
}

// TestParseInfoBothCandidatesImplausible pins the tie-break: when nothing readable follows
// the odd item, the walk stays at the unpadded position rather than stepping over a pad
// byte it cannot see, so the count of destroyed bytes is exact instead of one short.
func TestParseInfoBothCandidatesImplausible(t *testing.T) {
	junk := []byte{0x01, 0x02, 0x03}
	body := append(infoBody(infoItemRaw("INAM", "Song", false)), junk...)
	items, unread, padRescued, err := parseInfo(body, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if padRescued {
		t.Error("no later item was recovered, so nothing was rescued")
	}
	if unread != len(junk) {
		t.Errorf("unread = %d, want %d (every destroyed byte counted)", unread, len(junk))
	}
}

// TestParseInfoAlignmentZerosAreNotALoss: a writer that rounds the LIST size up leaves a
// run of zeros the item model does not carry but a rewrite re-creates, so counting it as
// destroyed would fail --strict on an ordinary file.
func TestParseInfoAlignmentZerosAreNotALoss(t *testing.T) {
	body := append(infoBody(infoItemRaw("INAM", "Title", true)), make([]byte, 8)...)
	items, unread, _, err := parseInfo(body, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1: a run of zeros is not an item", len(items))
	}
	if unread != 0 {
		t.Errorf("unread = %d, want 0 (alignment zeros cost nothing)", unread)
	}
}

// TestParseInfoCountsBytesPastTheTerminator: renderInfo writes the value up to the first
// NUL plus one terminator, so anything an item declared past it dies on the next rewrite.
func TestParseInfoCountsBytesPastTheTerminator(t *testing.T) {
	val := []byte("Song\x00HIDDENDATA\x00")
	body := slices.Concat([]byte("INFO"), []byte("INAM"),
		[]byte{byte(len(val)), 0, 0, 0}, val)
	items, unread, _, err := parseInfo(body, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].text() != "Song" {
		t.Fatalf("items = %v, want the value cut at its terminator", items)
	}
	if want := len(val) - len("Song\x00"); unread != want {
		t.Errorf("unread = %d, want %d (the bytes past the terminator)", unread, want)
	}
}

// TestPlausibleInfoItemToleratesPastEnd: the padded candidate after a final unpadded odd
// item is len(body)+1, which must read as implausible rather than index out of range.
func TestPlausibleInfoItemToleratesPastEnd(t *testing.T) {
	body := infoBody(infoItemRaw("INAM", "Song", false))
	if plausibleInfoItem(body, len(body)+1) {
		t.Error("a position past the body must not be plausible")
	}
	if !plausibleInfoItem(body, len(body)) {
		t.Error("the exact end of the body is a clean end of the list")
	}
}

// TestParseInfoPadByteIsNotALoss: an odd item that DOES carry its pad byte, followed by a
// region no walk can read, must not count the pad byte among the destroyed bytes - a
// rewrite writes its own. The sibling case, where the pad byte is missing and that first
// byte is real data, is TestParseInfoBothCandidatesImplausible.
func TestParseInfoPadByteIsNotALoss(t *testing.T) {
	junk := []byte{0x01, 0x02, 0x03}
	body := append(infoBody(infoItemRaw("INAM", "Song", true)), junk...)
	items, unread, _, err := parseInfo(body, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if unread != len(junk) {
		t.Errorf("unread = %d, want %d (the pad byte is not destroyed data)", unread, len(junk))
	}
}
