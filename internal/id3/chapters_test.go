package id3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// tagWith builds a tag at the given version carrying frames, for the chapter tests that
// project CHAP/CTOC back out.
func tagWith(version byte, frames []Frame) *Tag {
	return &Tag{srcVersion: version, writeVersion: version, frames: frames}
}

// TestChapterRoundTrip checks decode(encode(x)) == x for the start, end, and title CHAP
// stores, at both write versions. chapterFrames materializes open ends before encoding, so
// the expected round-trip carries the filled ends: an interior open chapter takes the next
// chapter's start, and the trailing open chapter takes the media duration passed in.
func TestChapterRoundTrip(t *testing.T) {
	const duration = 10 * time.Second
	in := []core.Chapter{
		{Start: 0, Title: "Intro"},
		{Start: 1500 * time.Millisecond, End: 3 * time.Second, Title: "Verse"},
		{Start: 3 * time.Second, Title: "Outro café"}, // non-Latin-1 forces UTF encoding
	}
	want := []core.Chapter{
		{Start: 0, End: 1500 * time.Millisecond, Title: "Intro"},               // interior open -> next start
		{Start: 1500 * time.Millisecond, End: 3 * time.Second, Title: "Verse"}, // explicit end kept
		{Start: 3 * time.Second, End: duration, Title: "Outro café"},           // trailing open -> duration
	}
	for _, version := range []byte{3, 4} {
		frames, overflow := chapterFrames(in, duration, version)
		if overflow {
			t.Fatalf("v%d: unexpected overflow", version)
		}
		got, ws := ProjectChapters(tagWith(version, frames))
		if len(ws) != 0 {
			t.Errorf("v%d: unexpected warnings %v", version, ws)
		}
		if len(got) != len(want) {
			t.Fatalf("v%d: got %d chapters, want %d", version, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("v%d chapter %d = %+v, want %+v", version, i, got[i], want[i])
			}
		}
	}
}

// TestChapterOpenEndRoundTrips checks the unknown-duration fallback: with duration 0, a
// trailing open-ended chapter (End == 0) has no bound to fill, so it keeps the 0xFFFFFFFF
// sentinel and reads back open rather than picking it up as a ~49.7-day time.
func TestChapterOpenEndRoundTrips(t *testing.T) {
	frames, _ := chapterFrames([]core.Chapter{{Start: 2 * time.Second, Title: "A"}}, 0, 4)
	got, _ := ProjectChapters(tagWith(4, frames))
	if len(got) != 1 || got[0].End != 0 {
		t.Fatalf("open-ended chapter with unknown duration = %+v, want End 0", got)
	}
}

// TestChapterTrailingEndFilledFromDuration checks the known-duration case: a trailing
// open-ended chapter takes the media duration as its concrete end, so a spec-conforming
// reader sees a bounded final chapter instead of the sentinel. The assertion is on the
// concrete End value (== duration), which the sentinel cannot spoof: WaxLabel's own decoder
// reads the sentinel back as End == 0, so a "reads back open" check could not tell a correctly
// bounded chapter from one regressed to the sentinel.
func TestChapterTrailingEndFilledFromDuration(t *testing.T) {
	const duration = 12 * time.Second
	frames, _ := chapterFrames([]core.Chapter{{Start: 2 * time.Second, Title: "A"}}, duration, 4)
	got, _ := ProjectChapters(tagWith(4, frames))
	if len(got) != 1 || got[0].End != duration {
		t.Fatalf("trailing open chapter with known duration = %+v, want End %v", got, duration)
	}
}

// TestChapterInteriorEndFilledFromNextStart checks an interior open-ended chapter takes the
// next chapter's start as its concrete end (a gapless interval), never the sentinel.
func TestChapterInteriorEndFilledFromNextStart(t *testing.T) {
	frames, _ := chapterFrames([]core.Chapter{
		{Start: 0, Title: "A"},
		{Start: 5 * time.Second, Title: "B"},
	}, 10*time.Second, 4)
	got, _ := ProjectChapters(tagWith(4, frames))
	if len(got) != 2 {
		t.Fatalf("got %d chapters, want 2", len(got))
	}
	if got[0].End != 5*time.Second {
		t.Errorf("interior chapter End = %v, want 5s (next start)", got[0].End)
	}
}

// TestChapterFilledEndClampsBelowSentinel checks that a filled trailing end past the 32-bit
// millisecond field clamps to chapTimeMax (0xFFFFFFFE), never colliding with the 0xFFFFFFFF
// sentinel that would decode as "open". The raw end bytes are inspected, since the projection
// reads chapTimeMax as a concrete time but 0xFFFFFFFF as open.
func TestChapterFilledEndClampsBelowSentinel(t *testing.T) {
	huge := time.Duration(0x100000000) * time.Millisecond // one past the uint32 ms field
	frames, overflow := chapterFrames([]core.Chapter{{Start: time.Second, Title: "A"}}, huge, 4)
	if !overflow {
		t.Error("a filled end past the 32-bit ms field should report overflow")
	}
	_, rest, ok := cutLatin1(frames[0].Body) // element ID, then start(4)+end(4)+offsets(8)
	if !ok || len(rest) < 8 {
		t.Fatalf("could not read CHAP end field from %x", frames[0].Body)
	}
	if end := binary.BigEndian.Uint32(rest[4:8]); end != chapTimeMax {
		t.Errorf("clamped filled end = %#08x, want %#08x (chapTimeMax, below the sentinel)", end, chapTimeMax)
	}
}

// TestChapterSubMillisecondTrailingEndFloorsSafely covers the ms-resolution edge of the CHAP
// fill: when the media duration is under 1 ms past the last chapter's start, flooring the
// filled end to milliseconds (durationToMs) collapses it onto the start. The result must be a
// benign zero-length chapter (End == Start) - never End < Start (an invalid interval) and never
// the 0xFFFFFFFF sentinel (the 49.7-day bug Finding 1 fixed). A zero-length final chapter is far
// closer to the truth than either alternative, and ms is the container's hard resolution, so this
// is the accepted output rather than a case to reroute back to the sentinel.
func TestChapterSubMillisecondTrailingEndFloorsSafely(t *testing.T) {
	start := 1000 * time.Millisecond
	duration := start + 500*time.Microsecond // 0.5 ms past the start: floors to the same ms
	frames, _ := chapterFrames([]core.Chapter{{Start: start, Title: "A"}}, duration, 4)
	_, rest, ok := cutLatin1(frames[0].Body) // element ID, then start(4)+end(4)+offsets(8)
	if !ok || len(rest) < 8 {
		t.Fatalf("could not read CHAP fields from %x", frames[0].Body)
	}
	startMs := binary.BigEndian.Uint32(rest[0:4])
	endMs := binary.BigEndian.Uint32(rest[4:8])
	if endMs == chapFieldUnused {
		t.Error("trailing end regressed to the 0xFFFFFFFF sentinel instead of a concrete end")
	}
	if endMs < startMs {
		t.Errorf("trailing end %d < start %d: an invalid interval", endMs, startMs)
	}
	if endMs != startMs {
		t.Errorf("sub-ms trailing end = %d, want == start %d (floored to a zero-length chapter)", endMs, startMs)
	}
}

// TestChapterTrailingEndNormalizesAgainstWriteDuration ties the ID3 writer's millisecond
// flooring (durationToMs) to the diff comparator's millisecond truncation
// (core.EqualChaptersModuloEnds): a trailing open chapter written from a non-whole-millisecond
// duration must read back as an end the comparator, given that same duration, normalizes to
// "open" (run-to-EOF). If the two ever floored differently, a file WaxLabel just wrote would
// diff as having a chapter difference against an equivalent open-ended list. This is the
// cross-package coupling the Truncate in normalizeReconstructableEnds exists to hold, exercised
// through both real code paths rather than a hardcoded floor.
func TestChapterTrailingEndNormalizesAgainstWriteDuration(t *testing.T) {
	start := time.Second
	duration := 2037*time.Millisecond + 551*time.Microsecond // deliberately not a whole ms
	frames, _ := chapterFrames([]core.Chapter{{Start: start, Title: "A"}}, duration, 4)
	got, _ := ProjectChapters(tagWith(4, frames))
	if len(got) != 1 {
		t.Fatalf("got %d chapters, want 1", len(got))
	}
	written := []core.Chapter{{Start: start, End: got[0].End, Title: "A"}}
	open := []core.Chapter{{Start: start, Title: "A"}}
	if !core.EqualChaptersModuloEnds(written, open, duration, duration) {
		t.Errorf("written trailing end %v does not normalize to open against its own write duration %v: the writer floor and comparator truncation disagree",
			got[0].End, duration)
	}
}

// TestChapterTimeClampsBelowSentinel checks that CHAP clamps past the 32-bit
// millisecond field to chapTimeMax (0xFFFFFFFE), one below the reserved
// "unused" sentinel. SYLT's 0xFFFFFFFF ceiling must not be used for CHAP,
// because that value decodes as time-not-used.
func TestChapterTimeClampsBelowSentinel(t *testing.T) {
	body, overflow := encodeCHAP("a", core.Chapter{Start: time.Duration(0x100000000) * time.Millisecond, Title: "X"}, 4)
	if !overflow {
		t.Error("a start past the 32-bit ms field should report overflow")
	}
	// The 4-byte start-time field follows the NUL-terminated element ID "a\0".
	if start := binary.BigEndian.Uint32(body[2:6]); start != chapTimeMax {
		t.Errorf("clamped CHAP start = %#08x, want %#08x (chapTimeMax, below the 0xFFFFFFFF sentinel)", start, chapTimeMax)
	}
}

// TestChapterCTOCOrdering checks the projection orders chapters by the CTOC child list,
// not by the on-disk CHAP order, and appends a CHAP the CTOC does not reference.
func TestChapterCTOCOrdering(t *testing.T) {
	mk := func(id string, start time.Duration, title string) Frame {
		body, _ := encodeCHAP(id, core.Chapter{Start: start, Title: title}, 4)
		return Frame{ID: "CHAP", Body: body}
	}
	// CHAP frames in scrambled file order; CTOC names the intended order b, a.
	frames := []Frame{
		mk("a", 5*time.Second, "second"),
		mk("b", 1*time.Second, "first"),
		mk("c", 9*time.Second, "unreferenced"),
		{ID: "CTOC", Body: encodeCTOC("toc", []string{"b", "a"})},
	}
	got, _ := ProjectChapters(tagWith(4, frames))
	titles := []string{}
	for _, c := range got {
		titles = append(titles, c.Title)
	}
	want := []string{"first", "second", "unreferenced"}
	if len(titles) != len(want) {
		t.Fatalf("titles = %v, want %v", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("title[%d] = %q, want %q", i, titles[i], want[i])
		}
	}
}

// TestCarryProjectionWarnings checks the front-tag warning carry: a stale projection note
// (chapters-flattened or invalid-picture) is dropped when the written tag no longer projects
// it, and kept in source order when it still does, while container warnings carry unchanged.
func TestCarryProjectionWarnings(t *testing.T) {
	flat := core.Warning{Code: core.WarnChaptersFlattened, Message: "flat"}
	enc := core.Warning{Code: core.WarnInheritedEncoder, Message: "enc"}
	source := []core.Warning{flat, enc}

	// Written tag still flattens (proj carries the note): keep source order [flatten, enc].
	got := CarryProjectionWarnings(source, []core.Warning{flat})
	if len(got) != 2 || got[0].Code != core.WarnChaptersFlattened || got[1].Code != core.WarnInheritedEncoder {
		t.Errorf("still-flattened: got %+v, want [flatten, encoder] in order", got)
	}
	// Written tag no longer flattens (proj empty): drop the stale flatten, keep the rest.
	got = CarryProjectionWarnings(source, nil)
	if len(got) != 1 || got[0].Code != core.WarnInheritedEncoder {
		t.Errorf("flattened-away: got %+v, want [encoder] only", got)
	}

	// A source invalid-picture (a parsed malformed APIC) is dropped when a picture edit's
	// rewritten tag no longer projects it (the malformed cover was dropped), but a container
	// warning like legacy-ape carries unchanged.
	badPic := core.Warning{Code: core.WarnInvalidPicture, Message: "APIC: invalid picture data"}
	ape := core.Warning{Code: core.WarnLegacyAPE, Message: "ape"}
	got = CarryProjectionWarnings([]core.Warning{badPic, ape}, nil)
	if len(got) != 1 || got[0].Code != core.WarnLegacyAPE {
		t.Errorf("dropped-malformed-apic: got %+v, want [legacy-ape] only", got)
	}
	// A tag-only edit preserves the malformed APIC, so proj still projects invalid-picture: keep it.
	got = CarryProjectionWarnings([]core.Warning{badPic}, []core.Warning{badPic})
	if len(got) != 1 || got[0].Code != core.WarnInvalidPicture {
		t.Errorf("preserved-malformed-apic: got %+v, want [invalid-picture] kept", got)
	}
}

// TestCheckChapterCount checks the codec-level backstop for the single-byte CTOC count:
// exactly 255 is accepted, 256 is rejected with ErrUnsupportedTag.
func TestCheckChapterCount(t *testing.T) {
	if err := CheckChapterCount(make([]core.Chapter, MaxChapters)); err != nil {
		t.Errorf("%d chapters: %v, want nil", MaxChapters, err)
	}
	if err := CheckChapterCount(make([]core.Chapter, MaxChapters+1)); !errors.Is(err, waxerr.ErrUnsupportedTag) {
		t.Errorf("%d chapters: err = %v, want ErrUnsupportedTag", MaxChapters+1, err)
	}
}

// TestChapterCTOCSubsetKeepsUnreferenced checks that when a CTOC references only a subset of
// the CHAP frames, the unreferenced chapters are still appended in file order. This is the
// regression case: file order [a,b,c] with CTOC [b] must yield [b,a,c], not drop a.
func TestChapterCTOCSubsetKeepsUnreferenced(t *testing.T) {
	mk := func(id string, start time.Duration, title string) Frame {
		body, _ := encodeCHAP(id, core.Chapter{Start: start, Title: title}, 4)
		return Frame{ID: "CHAP", Body: body}
	}
	frames := []Frame{
		mk("a", 1*time.Second, "a"),
		mk("b", 2*time.Second, "b"),
		mk("c", 3*time.Second, "c"),
		{ID: "CTOC", Body: encodeCTOC("toc", []string{"b"})}, // references only b
	}
	got, _ := ProjectChapters(tagWith(4, frames))
	var titles []string
	for _, c := range got {
		titles = append(titles, c.Title)
	}
	want := []string{"b", "a", "c"} // b first (CTOC), then a and c unreferenced in file order
	if len(titles) != len(want) {
		t.Fatalf("titles = %v, want %v (unreferenced chapters must not be dropped)", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("title[%d] = %q, want %q", i, titles[i], want[i])
		}
	}
}

// TestChapterDuplicateElementIDsAllSurvive checks that several CHAP frames sharing an
// element ID (a non-conformant tag) must each project to a distinct chapter rather than
// collapsing to one via the old map[elementID]Chapter keying. Without a CTOC they keep file
// order; a CTOC that names the shared ID more than once consumes one distinct CHAP per
// reference.
func TestChapterDuplicateElementIDsAllSurvive(t *testing.T) {
	mk := func(id string, start time.Duration, title string) Frame {
		body, _ := encodeCHAP(id, core.Chapter{Start: start, Title: title}, 4)
		return Frame{ID: "CHAP", Body: body}
	}
	titlesOf := func(chs []core.Chapter) []string {
		var ts []string
		for _, c := range chs {
			ts = append(ts, c.Title)
		}
		return ts
	}

	// No CTOC: three CHAP frames all keyed "chp0" must all survive, in file order.
	noTOC := []Frame{
		mk("chp0", 1*time.Second, "one"),
		mk("chp0", 2*time.Second, "two"),
		mk("chp0", 3*time.Second, "three"),
	}
	gotNoTOC, _ := ProjectChapters(tagWith(4, noTOC))
	if got := titlesOf(gotNoTOC); !slices.Equal(got, []string{"one", "two", "three"}) {
		t.Errorf("duplicate-ID chapters without a CTOC = %v, want [one two three] (none collapsed)", got)
	}

	// CTOC naming the shared ID three times: each reference consumes the next un-emitted
	// "chp0" in file order, so all three survive and follow the CTOC order.
	withTOC := []Frame{
		mk("chp0", 1*time.Second, "one"),
		mk("chp0", 2*time.Second, "two"),
		mk("chp0", 3*time.Second, "three"),
		{ID: "CTOC", Body: encodeCTOC("toc", []string{"chp0", "chp0", "chp0"})},
	}
	gotTOC, _ := ProjectChapters(tagWith(4, withTOC))
	if got := titlesOf(gotTOC); !slices.Equal(got, []string{"one", "two", "three"}) {
		t.Errorf("duplicate-ID chapters with a CTOC = %v, want [one two three] (each reference consumes one)", got)
	}
}

// TestChapterEmptyElementIDsAllSurvive checks the empty-ID case: several CHAP
// frames that all carry an empty element ID must each project to a distinct chapter rather than
// collapsing under the shared "" key.
func TestChapterEmptyElementIDsAllSurvive(t *testing.T) {
	mk := func(start time.Duration, title string) Frame {
		body, _ := encodeCHAP("", core.Chapter{Start: start, Title: title}, 4)
		return Frame{ID: "CHAP", Body: body}
	}
	frames := []Frame{
		mk(1*time.Second, "a"),
		mk(2*time.Second, "b"),
		mk(3*time.Second, "c"),
	}
	got, _ := ProjectChapters(tagWith(4, frames))
	if len(got) != 3 {
		t.Fatalf("empty-ID chapters projected %d, want 3 (none collapsed)", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].Title != want {
			t.Errorf("chapter %d title = %q, want %q", i, got[i].Title, want)
		}
	}
}

// TestChapterOpaqueFrameNotProjected checks that an opaque (compressed/encrypted) CHAP frame
// is not decoded into a bogus chapter, and is preserved verbatim across a chapter edit.
func TestChapterOpaqueFrameNotProjected(t *testing.T) {
	body, _ := encodeCHAP("a", core.Chapter{Start: time.Second, Title: "X"}, 4)
	// The same well-formed body, but marked opaque: skip it on read.
	if chs, _ := ProjectChapters(tagWith(4, []Frame{{ID: "CHAP", Body: body, Opaque: true}})); len(chs) != 0 {
		t.Errorf("opaque CHAP projected %d chapters, want 0", len(chs))
	}
	// The identical non-opaque body decodes, proving the skip is due to Opaque.
	if chs, _ := ProjectChapters(tagWith(4, []Frame{{ID: "CHAP", Body: body}})); len(chs) != 1 {
		t.Errorf("non-opaque CHAP projected %d chapters, want 1", len(chs))
	}
	// A chapter edit must preserve the opaque CHAP verbatim (we never decoded it).
	out, _ := RebuildFrames([]Frame{{ID: "CHAP", Body: body, Opaque: true}}, tag.NewTagSet(), tag.NewTagSet(), 4,
		StructuredEdit{Chapters: []core.Chapter{{Start: 0, Title: "New"}}, ChaptersChanged: true}, WriteOpts{})
	preserved := false
	for _, f := range out {
		if f.ID == "CHAP" && f.Opaque {
			preserved = true
		}
	}
	if !preserved {
		t.Error("a chapter edit dropped the opaque CHAP instead of preserving it verbatim")
	}
}

// TestChapterUnusedStartTimeSkipped checks that a CHAP whose start time is the 0xFFFFFFFF
// "not used" sentinel (a byte-offset-only chapter) is skipped, not projected at ~49.7 days.
func TestChapterUnusedStartTimeSkipped(t *testing.T) {
	body := append(encodeLatin1("a"), 0)
	body = append(body, 0xFF, 0xFF, 0xFF, 0xFF)                         // start time: not used
	body = append(body, 0x00, 0x00, 0x00, 0x00)                         // end time
	body = append(body, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF) // byte offsets
	if chs, _ := ProjectChapters(tagWith(4, []Frame{{ID: "CHAP", Body: body}})); len(chs) != 0 {
		t.Errorf("byte-offset-only CHAP projected %d chapters, want 0", len(chs))
	}
}

// TestChapterNestedCTOCFlattensWithWarning checks two CTOC frames (a nested hierarchy) are
// flattened to one ordered list with a chapters-flattened warning.
func TestChapterNestedCTOCFlattensWithWarning(t *testing.T) {
	chapA, _ := encodeCHAP("a", core.Chapter{Start: 0, Title: "A"}, 4)
	frames := []Frame{
		{ID: "CHAP", Body: chapA},
		{ID: "CTOC", Body: encodeCTOC("root", []string{"a"})},
		{ID: "CTOC", Body: encodeCTOC("sub", []string{"a"})},
	}
	got, ws := ProjectChapters(tagWith(4, frames))
	if len(got) != 1 {
		t.Fatalf("got %d chapters, want 1", len(got))
	}
	if len(ws) != 1 || ws[0].Code != core.WarnChaptersFlattened {
		t.Errorf("warnings = %v, want one chapters-flattened", ws)
	}
}

// TestChapterStartOverflowClamps checks a start past the 32-bit millisecond field clamps
// and reports the overflow.
func TestChapterStartOverflowClamps(t *testing.T) {
	huge := 60 * 24 * time.Hour // ~60 days, past the ~49.7-day uint32 ms limit
	_, overflow := chapterFrames([]core.Chapter{{Start: huge, Title: "X"}}, 0, 4)
	if !overflow {
		t.Error("a chapter past the 32-bit ms field should report overflow")
	}
}

// TestDecodeCHAPTruncated checks a CHAP body too short for the five fixed fields is
// rejected rather than over-read.
func TestDecodeCHAPTruncated(t *testing.T) {
	// element id "a\x00" then only 8 bytes (need 16 for start+end+two offsets).
	body := append([]byte("a\x00"), make([]byte, 8)...)
	if _, _, ok := decodeCHAP(body, 4); ok {
		t.Error("a truncated CHAP body should not decode")
	}
}

// TestChapterTitleInvalidUTF8Sanitized checks a decoded UTF-8 title with an invalid byte
// is sanitized through the read path (no raw invalid sequence reaches the model).
func TestChapterTitleInvalidUTF8Sanitized(t *testing.T) {
	// Build a CHAP whose TIT2 subframe holds a raw invalid UTF-8 byte under the UTF-8
	// encoding, mimicking a non-conformant file.
	tit2 := Frame{ID: "TIT2", Body: append([]byte{encUTF8}, 0xFF)}
	body := append(encodeLatin1("a"), 0)
	body = append(body, 0, 0, 0, 0)                                     // start ms
	body = append(body, 0xFF, 0xFF, 0xFF, 0xFF)                         // end: unused
	body = append(body, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF) // both offsets unused
	body = append(body, renderFrame(4, tit2)...)
	got, _ := ProjectChapters(tagWith(4, []Frame{{ID: "CHAP", Body: body}}))
	if len(got) != 1 {
		t.Fatalf("got %d chapters, want 1", len(got))
	}
	for _, r := range got[0].Title {
		if r == 0xFFFD {
			return // replacement char present: sanitized as expected
		}
	}
	t.Errorf("title %q was not sanitized to the replacement character", got[0].Title)
}

// TestRebuildPreservesChaptersOnTagEdit checks that a tag-only edit (chapters unchanged)
// preserves the source CHAP/CTOC frames byte-for-byte, while the tag change still applies.
func TestRebuildPreservesChaptersOnTagEdit(t *testing.T) {
	chapFrames, _ := chapterFrames([]core.Chapter{{Start: time.Second, End: 2 * time.Second, Title: "Ch1"}}, 0, 4)
	orig := append([]Frame{{ID: "TIT2", Body: encodeTextFrame(encLatin1, []string{"Old"})}}, chapFrames...)

	base := tag.NewTagSet()
	base.Set(tag.Title, "Old")
	edited := tag.NewTagSet()
	edited.Set(tag.Title, "New")

	out, _ := RebuildFrames(orig, base, edited, 4, StructuredEdit{}, WriteOpts{})

	var chap, ctoc []Frame
	for _, f := range out {
		switch f.ID {
		case "CHAP":
			chap = append(chap, f)
		case "CTOC":
			ctoc = append(ctoc, f)
		}
	}
	if len(chap) != 1 || len(ctoc) != 1 {
		t.Fatalf("chapters not preserved on a tag edit: %d CHAP, %d CTOC", len(chap), len(ctoc))
	}
	if !bytes.Equal(chap[0].Body, chapFrames[0].Body) {
		t.Error("CHAP body changed on an unrelated tag edit")
	}
	if got, _ := Project(tagWith(4, out)).Tags.First(tag.Title); got != "New" {
		t.Errorf("title after edit = %q, want New", got)
	}
}

// TestChapterEditFlattensNestedCTOCWarning checks that a chapter edit on a tag whose source
// had a nested CTOC writes a single flat CTOC whose projection carries no flatten warning.
// The post-write result uses the written tag's warnings, not stale source warnings.
func TestChapterEditFlattensNestedCTOCWarning(t *testing.T) {
	chapA, _ := encodeCHAP("a", core.Chapter{Start: 0, Title: "A"}, 4)
	nested := []Frame{
		{ID: "CHAP", Body: chapA},
		{ID: "CTOC", Body: encodeCTOC("root", []string{"a"})},
		{ID: "CTOC", Body: encodeCTOC("sub", []string{"a"})},
	}
	if _, ws := ProjectChapters(tagWith(4, nested)); len(ws) != 1 {
		t.Fatalf("nested source should warn once on read, got %v", ws)
	}

	base := Project(tagWith(4, nested)).Tags
	out, _ := RebuildFrames(nested, base, base, 4, StructuredEdit{
		Chapters:        []core.Chapter{{Start: 0, Title: "A"}, {Start: time.Second, Title: "B"}},
		ChaptersChanged: true,
	}, WriteOpts{})

	chs, ws := ProjectChapters(tagWith(4, out))
	if len(ws) != 0 {
		t.Errorf("flattened output should carry no flatten warning, got %v", ws)
	}
	if len(chs) != 2 {
		t.Errorf("got %d chapters after edit, want 2", len(chs))
	}
	ctocs := 0
	for _, f := range out {
		if f.ID == "CTOC" {
			ctocs++
		}
	}
	if ctocs != 1 {
		t.Errorf("got %d CTOC frames after edit, want 1 (flattened)", ctocs)
	}
}

// FuzzDecodeCHAP asserts the CHAP body parser never panics and never yields an
// invalid-UTF-8 title, at either subframe version.
func FuzzDecodeCHAP(f *testing.F) {
	f.Add([]byte(nil))
	f.Add([]byte("a\x00"))
	f.Add(append([]byte("a\x00"), make([]byte, 16)...))
	f.Add(append([]byte("a\x00"), append(make([]byte, 16), 'T', 'I', 'T', '2')...))
	f.Add(append([]byte("\x00"), make([]byte, 16)...)) // empty element ID (dup/empty-ID regression)
	good, _ := encodeCHAP("chp0", core.Chapter{Start: time.Second, End: 2 * time.Second, Title: "Tî"}, 4)
	f.Add(good)
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, major := range []byte{3, 4} {
			if _, ch, ok := decodeCHAP(body, major); ok && !utf8.ValidString(ch.Title) {
				t.Errorf("decodeCHAP title not valid UTF-8: %q", ch.Title)
			}
		}
	})
}

// FuzzDecodeCTOC asserts the CTOC body parser never panics on arbitrary input. The child
// count is a single byte, so the child list is inherently bounded.
func FuzzDecodeCTOC(f *testing.F) {
	f.Add([]byte(nil))
	f.Add([]byte("toc\x00"))
	f.Add([]byte("toc\x00\x03\x02a\x00b\x00"))
	f.Add(encodeCTOC("toc", []string{"a", "b", "c"}))
	f.Fuzz(func(t *testing.T, body []byte) {
		c, ok := decodeCTOC(body)
		if ok && len(c.children) > 255 {
			t.Errorf("CTOC decoded %d children past the single-byte count", len(c.children))
		}
	})
}
