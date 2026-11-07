package waxlabel_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// unseparatedEntry is the malformed entry every case below carries: well framed by the list's
// length prefix, but with no separator, so no reader can split it into a key and a value.
const unseparatedEntry = "noequalshere"

// An unseparated entry is preserved across a rewrite, which re-renders the list from the parsed
// comments, and is reported. The comment codec is shared by FLAC and every Ogg mapping, so both
// the native FLAC block and the Ogg FLAC packet form are covered.
func TestUnseparatedVorbisEntryPreservedAndReported(t *testing.T) {
	comments := testfile.CommentBlock("test", "TITLE=Song", unseparatedEntry)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"flac", testfile.FLAC(comments, testfile.FLACBlock(1, make([]byte, 4)))},
		{"ogg flac", testfile.OggFLAC(comments)},
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
			// The bytes survive an unrelated edit, and a re-parse reports the condition exactly once.
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

// control: an ordinary comment list must not draw the warning.
func TestSeparatedVorbisEntriesStayQuiet(t *testing.T) {
	doc := mustParseBytes(t, flacWithComments("TITLE=Song", "ARTIST=Band"))
	if hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("a well-formed comment list must stay quiet: %v", doc.Warnings())
	}
}

// An overrunning frame is reported and dropped, and its unread remainder is not counted as
// padding, since dump would otherwise report room the file does not have.
func TestID3OverrunningFrameReportedAndDropped(t *testing.T) {
	data := mp3WithFrames(t, testfile.TextFrame(4, "TIT2", "Song"), testfile.OverrunFrame("TALB"))
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

// control: a tag that stops on real padding still reports it, so the malformed-tail rule did not
// swallow the ordinary case.
func TestID3CleanTagKeepsItsPadding(t *testing.T) {
	tagBytes := testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "Song"))
	tagBytes = slices.Concat(tagBytes, make([]byte, 64))
	copy(tagBytes[6:10], testfile.Syncsafe(len(tagBytes)-10))
	doc := mustParseBytes(t, append(tagBytes, mp3Audio(t)...))
	if hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Fatalf("a clean tag must stay quiet: %v", doc.Warnings())
	}
	if p := doc.Padding(); p != 64 {
		t.Errorf("padding = %d, want 64", p)
	}
}

// A front tag header declaring more bytes than the whole file must draw a diagnostic, not read
// as "no front tag" with a nil error.
func TestID3FrontTagOverrunningFileReported(t *testing.T) {
	audio := mp3Audio(t)
	hdr := slices.Concat([]byte{'I', 'D', '3', 4, 0, 0}, testfile.Syncsafe(1<<20))
	doc := mustParseBytes(t, append(hdr, audio...))
	if !hasWarning(doc, wl.WarnMalformedTagEntry) {
		t.Errorf("an unreadable front tag went unreported: %v", doc.Warnings())
	}
}

// The condition is a property of the list, so it is reported once. A crafted comment packet can
// hold tens of thousands of entries, and one warning per entry would turn a 400 KiB file into
// megabytes of dump and lint output.
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

// A warning splices bytes the file chose, so an oversized value must be elided; a 1 MiB ENCODER
// containing "lavf" would otherwise be a 1 MiB line in dump and lint.
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

// The rewritten tag has no unreadable tail, so the written file no longer reports one.
func TestID3RewriteDropsMalformedTail(t *testing.T) {
	data := mp3WithFrames(t, testfile.TextFrame(4, "TIT2", "Song"), testfile.OverrunFrame("TALB"))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "Band").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if re := mustParseBytes(t, applyToBytes(t, data, plan)); hasWarning(re, wl.WarnMalformedTagEntry) {
		t.Errorf("the written file still reports a region the write removed: %v", re.Warnings())
	}
}

// A v2.2 identifier is upgraded to its v2.3/v2.4 spelling everywhere else in the output, so the
// diagnostic must use the same name.
func TestID3v22MalformedFrameUsesTheUpgradedID(t *testing.T) {
	// A v2.2 TAL (album) frame whose 3-byte size overruns the tag.
	bad := slices.Concat([]byte("TAL"), []byte{0x0F, 0xFF, 0xFF}, []byte("short"))
	doc := mustParseBytes(t, append(testfile.ID3v2(2, bad), mp3Audio(t)...))
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

// The native view must account for the unreadable region; with the phantom padding zeroed, nothing
// else explains why the block's size disagrees with its listed frames.
func TestID3MalformedTailShownInTheNativeView(t *testing.T) {
	data := mp3WithFrames(t, testfile.TextFrame(4, "TIT2", "Song"), testfile.OverrunFrame("TALB"))
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

// lint --fix strips a legacy container only when it can prove it redundant. A region nothing read
// cannot be proven redundant, so the container must grade opaque or the fix would destroy it.
func TestFLACStrayID3MalformedTailBlocksTheStrip(t *testing.T) {
	lead := testfile.ID3v2(4, testfile.OverrunFrame("TIT2"))
	doc := mustParseBytes(t, append(lead, flacWithComments("TITLE=Song")...))
	if !doc.HasOpaqueLegacyContent() {
		t.Error("a stray ID3v2 with an unreadable region must grade opaque, or --fix would strip it")
	}
}

// An APE tag footer at the end of a WavPack, Monkey's Audio, Musepack, MP3, ADTS or FLAC file whose
// tag would start inside the first block or frame, the file header or the stream header is not read
// as a tag, and is reported rather than ignored.
func TestAPETagInsideTheFirstUnitReported(t *testing.T) {
	inHeader := descriptorAPEFile(52, 0) // header at 52..76 past the marker
	copy(inHeader[44:], apeTagText(nil)) // a footer-only tag over the last 32 bytes
	// reaching declares the tag that ends stream to start at offset at.
	reaching := func(stream []byte, at int) []byte {
		b := slices.Concat(stream, apeTagText(map[string]string{"Title": "Inside"}))
		put32le(b[len(b)-20:], len(b)-at)
		return b
	}
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"wavpack block", wvBlock(1|wvRate44k|wvFinal, 4410, slices.Concat(make([]byte, 64), apeTagText(map[string]string{"Title": "Inside"})))},
		{"monkey's audio header", inHeader},
		{"musepack stream header", reaching(mpcSV7(100, 0), 10)}, // inside the 24-byte SV7 header
		{"mp3 frame", reaching(mp3Frames(4), 100)},
		{"adts frame", reaching(adtsStream(2, 4, 200), 100)},
		{"flac metadata", reaching(readFixture(t, notagsFLAC), 100)},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseBytes(t, c.data)
			if doc.Tags().Len() != 0 || hasWarning(doc, wl.WarnLegacyAPE) {
				t.Errorf("tags %v, warnings %v: a tag was read from inside the first unit", doc.Tags().Keys(), doc.Warnings())
			}
			if !hasWarning(doc, wl.WarnMalformedTagEntry) || !lintHasCode(doc, "malformed-tag-entry") {
				t.Errorf("the misplaced footer went unreported; warnings %v", doc.Warnings())
			}
		})
	}
}
