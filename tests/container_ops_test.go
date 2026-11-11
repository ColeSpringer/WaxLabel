package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// The operation list distinguishes what Execute does to a tag container the IFF codecs hold:
// replace its bytes, leave them in place, or delete the container.

func opsFor(t *testing.T, data []byte, edit func(*wl.Editor)) []string {
	t.Helper()
	e := mustParseBytes(t, data).Edit()
	edit(e)
	plan, err := e.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if plan.IsNoOp() {
		t.Fatal("expected a real write, got a no-op")
	}
	return plan.Report().Operations
}

func addCover(e *wl.Editor) { e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: testfile.PNG()}) }

// clearing every value the native container holds deletes the container.
func TestNativeContainerDropIsReported(t *testing.T) {
	for _, tc := range []struct {
		name, op string
		data     []byte
	}{
		{"wav", "LIST/INFO drop", testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "T"}), testfile.WAVData(400))},
		{"aiff", "native text chunk drop", testfile.AIFF("AIFF", stdCOMM(), aiffText("NAME", "T"), stdSSND())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := opsFor(t, tc.data, func(e *wl.Editor) { e.Clear(tag.Title) })
			if !slices.Contains(ops, tc.op) {
				t.Errorf("deleting the container reported %v, want it to contain %q", ops, tc.op)
			}
		})
	}
}

// A picture-only edit forces an ID3 chunk but leaves the native container's bytes in place, so no
// rewrite is reported.
func TestUnchangedNativeContainerReportsNoRewrite(t *testing.T) {
	for _, tc := range []struct {
		name, op string
		data     []byte
	}{
		{"wav", "LIST/INFO rewrite", testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "T"}), testfile.WAVData(400))},
		{"aiff", "native text chunk rewrite", testfile.AIFF("AIFF", stdCOMM(), aiffText("NAME", "T"), stdSSND())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ops := opsFor(t, tc.data, addCover); slices.Contains(ops, tc.op) {
				t.Errorf("an untouched container must not report %q: %v", tc.op, ops)
			}
			// The control: an edit that does move the bytes still reports it.
			ops := opsFor(t, tc.data, func(e *wl.Editor) { e.Set(tag.Title, "New") })
			if !slices.Contains(ops, tc.op) {
				t.Errorf("a real container change should report %q: %v", tc.op, ops)
			}
		})
	}
}

// A native container an edit leaves alone keeps bytes a re-render from the item model would lose
// or move: what follows a value's terminator, chunks sitting between its own, a tail the item walk
// could not read.
func TestUntouchedNativeContainerKeepsItsBytes(t *testing.T) {
	infoTail := testfile.WAVChunk("LIST", slices.Concat(testfile.WAVInfo([2]string{"INAM", "T"})[8:], []byte{0xFF, 0xFE, 0xFD, 0xFC}))
	noItems := testfile.WAVChunk("LIST", []byte("INFO\xFF\xFE\xFD\xFC\xFB\xFA"))
	postNUL := testfile.AIFFChunk("NAME", []byte("T\x00junk"))
	split := slices.Concat(aiffText("NAME", "T"), testfile.AIFFChunk("APPL", []byte("xx")), aiffText("ANNO", "c"))

	for _, tc := range []struct {
		name, op   string
		data, kept []byte
	}{
		{"aiff-post-nul-bytes", "native text chunk rewrite", testfile.AIFF("AIFF", stdCOMM(), postNUL, stdSSND()), postNUL},
		{"aiff-split-group", "native text chunk rewrite", testfile.AIFF("AIFF", stdCOMM(), split, stdSSND()), split},
		{"wav-unreadable-tail", "LIST/INFO rewrite", testfile.WAV(testfile.WAVFmtPCM(), infoTail, testfile.WAVData(400)), infoTail},
		{"wav-tail-and-no-items", "LIST/INFO drop", testfile.WAV(testfile.WAVFmtPCM(), noItems, testfile.WAVData(400)), noItems},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := mustParseBytes(t, tc.data).Edit()
			addCover(e)
			plan, err := e.Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if ops := plan.Report().Operations; slices.Contains(ops, tc.op) {
				t.Errorf("an untouched container must not report %q: %v", tc.op, ops)
			}
			if planWarns(t, plan, wl.WarnMalformedTagEntryDropped) || !bytes.Contains(applyToBytes(t, tc.data, plan), tc.kept) {
				t.Errorf("the container's bytes did not survive whole and silent: %v", plan.Report().Warnings)
			}
		})
	}
}

// An id3 chunk holding no frames is kept by an edit that gives it none, as any chunk the edit
// leaves alone is; only an edit that empties a chunk drops it.
func TestFramelessId3ChunkSurvivesAnEditThatLeavesItAlone(t *testing.T) {
	empty := testfile.ID3v2(3, make([]byte, 20))
	for _, tc := range []struct {
		name, op    string
		data, chunk []byte
	}{
		{"wav", "id3 chunk drop", testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "T"}, [2]string{"ICMT", "c"}),
			wavID3(empty), testfile.WAVData(400)), wavID3(empty)},
		{"aiff", "ID3 chunk drop", testfile.AIFF("AIFF", stdCOMM(), aiffText("NAME", "T"), aiffText("ANNO", "c"), aiffID3(empty), stdSSND()), aiffID3(empty)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := mustParseBytes(t, tc.data).Edit().Clear(tag.Title).Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if ops := plan.Report().Operations; slices.Contains(ops, tc.op) || !bytes.Contains(applyToBytes(t, tc.data, plan), tc.chunk) {
				t.Errorf("the chunk did not survive whole: %v", ops)
			}
		})
	}
}

// Bytes that follow an ID3v2 tag inside its chunk are reported, survive an edit that leaves the
// chunk alone, and are warned when a rewrite of the chunk drops them. Zeros there are padding.
func TestBytesBehindTheTagInItsChunk(t *testing.T) {
	tagBytes := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "T"))
	junk, zeros := slices.Concat(tagBytes, []byte("JUNKJUNK")), slices.Concat(tagBytes, make([]byte, 8))
	for _, tc := range []struct {
		name string
		file func(tagged []byte) (data, chunk []byte)
	}{
		{"wav", func(tagged []byte) ([]byte, []byte) {
			return testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Other"}), wavID3(tagged), testfile.WAVData(400)), wavID3(tagged)
		}},
		{"aiff", func(tagged []byte) ([]byte, []byte) {
			return testfile.AIFF("AIFF", stdCOMM(), aiffText("NAME", "Other"), aiffID3(tagged), stdSSND()), aiffID3(tagged)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, chunk := tc.file(junk)
			doc := mustParseBytes(t, data)
			if !hasWarning(doc, wl.WarnMalformedTagEntry) {
				t.Errorf("warnings %v, want the bytes behind the tag reported", doc.Warnings())
			}
			// Setting the title the tag holds rewrites the native value alone.
			kept, err := doc.Edit().Set(tag.Title, "T").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if planWarns(t, kept, wl.WarnMalformedTagEntryDropped) || !bytes.Contains(applyToBytes(t, data, kept), chunk) {
				t.Errorf("an edit that leaves the chunk alone must keep it whole and silent: %v", kept.Report().Warnings)
			}
			rewritten, err := doc.Edit().Set(tag.Album, "A").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if !planWarns(t, rewritten, wl.WarnMalformedTagEntryDropped) || bytes.Contains(applyToBytes(t, data, rewritten), []byte("JUNKJUNK")) {
				t.Errorf("a rewrite of the chunk drops the bytes and must say so: %v", rewritten.Report().Warnings)
			}
			data, _ = tc.file(zeros)
			if doc := mustParseBytes(t, data); hasWarning(doc, wl.WarnMalformedTagEntry) {
				t.Errorf("zeros behind the tag reported: %v", doc.Warnings())
			}
		})
	}
}
