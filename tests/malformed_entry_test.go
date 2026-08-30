package waxlabel_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// unseparatedEntry is the malformed entry every case below carries: well framed by the
// list's length prefix, but with no separator, so no reader can split it into a key and a
// value.
const unseparatedEntry = "noequalshere"

// TestUnseparatedVorbisEntryPreservedAndReported: the entry used to be dropped at parse and
// then erased by the next rewrite, which re-renders the list from the parsed comments. The
// comment codec is shared, so one fix covers FLAC and every Ogg mapping; both the native
// FLAC block and the Ogg FLAC packet form are driven here.
func TestUnseparatedVorbisEntryPreservedAndReported(t *testing.T) {
	body := renderVC("TITLE=Song", unseparatedEntry)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"flac", slices.Concat([]byte("fLaC"),
			flacBlock(0, false, validStreamInfo()), flacBlock(4, false, body),
			flacBlock(1, true, make([]byte, 4)), []byte{0xFF, 0xF8})},
		{"ogg flac", synthOggFLAC(append([]byte{4}, body...))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustParseBytes(t, tc.data)
			if doc.Fields().Title != "Song" {
				t.Errorf("title = %q, want Song (the readable entry still projects)", doc.Fields().Title)
			}
			if !hasWarning(doc, wl.WarnMalformedTagEntry) {
				t.Fatalf("the unseparated entry went unreported: %v", doc.Warnings())
			}
			if !lintHasCode(doc, "malformed-tag-entry") {
				t.Errorf("lint did not promote the warning: %v", doc.Lint())
			}
			// The bytes survive an unrelated edit, and a re-parse reports the condition
			// exactly once - preserved, and not double-counted.
			plan, err := doc.Edit().Set(tag.Album, "New").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, tc.data, plan)
			if !bytes.Contains(out, []byte(unseparatedEntry)) {
				t.Errorf("the edit destroyed the unseparated entry")
			}
			re := mustParseBytes(t, out)
			if got := countWarning(re.Warnings(), wl.WarnMalformedTagEntry); got != 1 {
				t.Errorf("re-parse malformed-tag-entry count = %d, want exactly 1", got)
			}
			if re.Fields().Album != "New" {
				t.Errorf("the edit did not apply: album = %q", re.Fields().Album)
			}
		})
	}
}

// TestSeparatedVorbisEntriesStayQuiet is the control: an ordinary comment list must not
// draw the warning.
func TestSeparatedVorbisEntriesStayQuiet(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("TITLE=Song", "ARTIST=Band"))
	if hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("a well-formed comment list must stay quiet: %v", doc.Warnings())
	}
}

// overrunFrame builds an ID3 frame header whose declared size runs past the end of the tag,
// followed by a short body. The walk cannot read past it, so everything from its header on
// is unreadable rather than free padding.
func overrunFrame(id string) []byte {
	return slices.Concat([]byte(id), syncsafe(1<<20), []byte{0, 0}, []byte("short"))
}

// TestID3OverrunningFrameReportedAndDropped: the walk stopped on the overrunning frame with
// no error and no signal, and ParseTag then recorded the unread remainder as free padding,
// so dump reported room the file does not have. The write side lost the region too: a
// rewrite renders frames plus padding, replacing it with zeros and never mentioning it.
func TestID3OverrunningFrameReportedAndDropped(t *testing.T) {
	data := mp3WithFrames(t, textFrame(4, "TIT2", "Song"), overrunFrame("TALB"))
	doc := mustParseBytes(t, data)
	if doc.Fields().Title != "Song" {
		t.Errorf("title = %q, want Song (frames before the bad one still read)", doc.Fields().Title)
	}
	if !hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Fatalf("the overrunning frame went unreported: %v", doc.Warnings())
	}
	if !lintHasCode(doc, "malformed-tag-entry") {
		t.Errorf("lint did not promote the warning: %v", doc.Lint())
	}
	if p := doc.Padding(); p != 0 {
		t.Errorf("padding = %d, want 0 (an unreadable tail is not free space)", p)
	}
	named := false
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnMalformedTagEntry && strings.Contains(w.Message, "TALB") {
			named = true
		}
	}
	if !named {
		t.Errorf("the warning does not name the frame: %v", doc.Warnings())
	}
	plan, err := doc.Edit().Set(tag.Artist, "Band").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarn(plan.Report().Warnings, wl.WarnMalformedTagEntryDropped) {
		t.Errorf("the write did not report the dropped region: %v", plan.Report().Warnings)
	}
	// The rewritten tag has no unreadable tail, so the condition clears.
	re := mustParseBytes(t, applyToBytes(t, data, plan))
	if hasWarning(re, wl.WarnMalformedTagEntry) {
		t.Errorf("the rewritten file still reports the condition it fixed: %v", re.Warnings())
	}
}

// TestID3CleanTagKeepsItsPadding is the control: a tag that stops on real padding still
// reports it, so the malformed-tail rule did not swallow the ordinary case.
func TestID3CleanTagKeepsItsPadding(t *testing.T) {
	tagBytes := id3v2(4, textFrame(4, "TIT2", "Song"))
	tagBytes = slices.Concat(tagBytes, make([]byte, 64))
	copy(tagBytes[6:10], syncsafe(len(tagBytes)-10))
	doc := mustParseBytes(t, append(tagBytes, mp3Audio(t)...))
	if hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Fatalf("a clean tag must stay quiet: %v", doc.Warnings())
	}
	if p := doc.Padding(); p != 64 {
		t.Errorf("padding = %d, want 64", p)
	}
}

// TestID3FrontTagOverrunningFileReported: a tag header declaring more bytes than the whole
// file returned "no front tag" with a nil error, so the tag vanished from every view with
// no diagnostic at all.
func TestID3FrontTagOverrunningFileReported(t *testing.T) {
	audio := mp3Audio(t)
	hdr := slices.Concat([]byte{'I', 'D', '3', 4, 0, 0}, syncsafe(1<<20))
	doc := mustParseBytes(t, append(hdr, audio...))
	if !hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("an unreadable front tag went unreported: %v", doc.Warnings())
	}
}

// TestManyUnseparatedEntriesReportOnce: the condition is a property of the list, and a
// crafted comment packet can hold entries by the tens of thousands. One warning per entry
// turned a 400 KiB file into megabytes of dump and lint output.
func TestManyUnseparatedEntriesReportOnce(t *testing.T) {
	entries := make([]string, 0, 5001)
	entries = append(entries, "TITLE=Song")
	for range 5000 {
		entries = append(entries, unseparatedEntry)
	}
	doc := mustParseBytes(t, flacWithComments(entries...))
	if got := countWarning(doc.Warnings(), wl.WarnMalformedTagEntry); got != 1 {
		t.Fatalf("malformed-tag-entry count = %d, want 1 for the whole list", got)
	}
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnMalformedTagEntry && !strings.Contains(w.Message, "4999 more") {
			t.Errorf("the warning does not count the rest: %q", w.Message)
		}
	}
}

// TestWarningSnippetsAreBounded: a warning splices bytes the file chose, so an oversized
// value must be elided rather than printed whole - a 1 MiB ENCODER containing "lavf" would
// otherwise be a 1 MiB line in dump and lint.
func TestWarningSnippetsAreBounded(t *testing.T) {
	const big = 4096
	for _, tc := range []struct{ name, entry string }{
		{"encoder stamp", "ENCODER=Lavf" + strings.Repeat("x", big)},
		{"unseparated entry", strings.Repeat("y", big)},
		{"invalid key", strings.Repeat("K~", big/2) + "=v"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustParseBytes(t, flacWithComments("TITLE=Song", tc.entry))
			if len(doc.Warnings()) == 0 {
				t.Fatal("expected a warning to elide")
			}
			for _, w := range doc.Warnings() {
				if len(w.Message) > 512 {
					t.Errorf("%s message is %d bytes; file-derived text must be elided", w.Code, len(w.Message))
				}
			}
		})
	}
}

// TestID3MalformedTailClearsOnThePostWriteDocument: the Document a write returns must match
// a fresh parse of the bytes it wrote. The rewritten tag has no unreadable tail, so carrying
// the read warning forward would have the result claim the region both still exists and was
// dropped.
func TestID3MalformedTailClearsOnThePostWriteDocument(t *testing.T) {
	data := mp3WithFrames(t, textFrame(4, "TIT2", "Song"), overrunFrame("TALB"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "Band").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	after, _, err := plan.Execute(context.Background(), wl.WriteTo(&buf, wl.BytesSource(data)))
	if err != nil {
		t.Fatal(err)
	}
	if hasWarning(after, wl.WarnMalformedTagEntry) {
		t.Errorf("the post-write document still reports a region the write removed: %v", after.Warnings())
	}
	if got := len(mustParseBytes(t, buf.Bytes()).Warnings()); got != len(after.Warnings()) {
		t.Errorf("post-write warnings (%d) disagree with a fresh parse (%d)", len(after.Warnings()), got)
	}
}

// TestID3v22MalformedFrameUsesTheUpgradedID: a v2.2 identifier is upgraded to its v2.3/v2.4
// spelling everywhere else in the output, so the diagnostic must not name a frame the
// listing beside it calls something different.
func TestID3v22MalformedFrameUsesTheUpgradedID(t *testing.T) {
	// A v2.2 TAL (album) frame whose 3-byte size overruns the tag.
	bad := slices.Concat([]byte("TAL"), []byte{0x0F, 0xFF, 0xFF}, []byte("short"))
	doc := mustParseBytes(t, append(id3v2(2, bad), mp3Audio(t)...))
	var msg string
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnMalformedTagEntry {
			msg = w.Message
		}
	}
	if !strings.Contains(msg, "TALB") {
		t.Errorf("warning = %q, want it to name the frame as TALB", msg)
	}
}

// TestID3MalformedTailShownInTheNativeView: zeroing the phantom padding removed the only
// line that accounted for the region, so the block's size disagreed with its listed frames
// in silence - the very condition the warning is about.
func TestID3MalformedTailShownInTheNativeView(t *testing.T) {
	data := mp3WithFrames(t, textFrame(4, "TIT2", "Song"), overrunFrame("TALB"))
	var note string
	for _, e := range mustParseBytes(t, data).Native().Describe() {
		if strings.HasPrefix(e.Kind, "ID3v2") {
			note = e.Note
		}
	}
	if !strings.Contains(note, "unparsed byte(s)") {
		t.Errorf("native note = %q, want it to account for the unreadable region", note)
	}
}

// TestFLACStrayID3MalformedTailBlocksTheStrip: lint --fix strips a legacy container only
// when it can prove it redundant. A region nothing read cannot be proven redundant, so the
// container must grade opaque or the fix would destroy it.
func TestFLACStrayID3MalformedTailBlocksTheStrip(t *testing.T) {
	lead := id3v2(4, overrunFrame("TIT2"))
	doc := mustParseBytes(t, append(lead, flacWithComments("TITLE=Song")...))
	if !doc.HasOpaqueLegacyContent() {
		t.Error("a stray ID3v2 with an unreadable region must grade opaque, or --fix would strip it")
	}
}
