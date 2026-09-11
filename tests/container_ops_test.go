package waxlabel_test

import (
	"slices"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// The IFF codecs re-emit a present tag container on every write, so the operation list has to
// distinguish three things Execute really does to it: replace its bytes, leave them where they
// were, and delete the container outright. These tests pin all three for the WAV and AIFF
// twins together, since the rule is one rule.

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

func addCover(e *wl.Editor) { e.AddPicture(wl.Picture{Type: wl.PicFrontCover, Data: tinyPNG()}) }

// TestNativeContainerDropIsReported: clearing every value the native container holds deletes
// the container. Execute removes a chunk the file had, so the report must name the removal
// instead of leaving the renderer to fall back to a bare "rewrite metadata".
func TestNativeContainerDropIsReported(t *testing.T) {
	for _, tc := range []struct {
		name, op string
		data     []byte
	}{
		{"wav", "LIST/INFO drop", wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "T"}), wavData(400))},
		{"aiff", "native text chunk drop", aiffFile("AIFF", stdCOMM(), aiffText("NAME", "T"), stdSSND())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := opsFor(t, tc.data, func(e *wl.Editor) { e.Clear(tag.Title) })
			if !slices.Contains(ops, tc.op) {
				t.Errorf("deleting the container reported %v, want it to contain %q", ops, tc.op)
			}
		})
	}
}

// TestUnchangedNativeContainerReportsNoRewrite: a picture-only edit forces an ID3 chunk but
// leaves the native container's bytes exactly where they were. Claiming a rewrite there
// describes work no reader could observe.
func TestUnchangedNativeContainerReportsNoRewrite(t *testing.T) {
	for _, tc := range []struct {
		name, op string
		data     []byte
	}{
		{"wav", "LIST/INFO rewrite", wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "T"}), wavData(400))},
		{"aiff", "native text chunk rewrite", aiffFile("AIFF", stdCOMM(), aiffText("NAME", "T"), stdSSND())},
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

// TestContainerRewriteReportedWhenOnlyTheBytesMove guards the half of the rule a value
// comparison alone gets wrong. In both files every canonical value survives the rewrite
// untouched, yet the container's bytes do move, so the rewrite is real and must be reported.
// The same files must still round-trip an edit that names nothing as a no-op: the report
// question ("do the bytes move?") is not the no-op question ("did the content change?").
func TestContainerRewriteReportedWhenOnlyTheBytesMove(t *testing.T) {
	// A LIST body whose tail the item walk could not read: the rewrite renders from the items
	// alone, so those bytes do not come back.
	infoTail := wavChunk("LIST", slices.Concat(wavInfo([2]string{"INAM", "T"})[8:], []byte{0xFF, 0xFE, 0xFD, 0xFC}))

	for _, tc := range []struct {
		name, op, why string
		data          []byte
	}{
		{"aiff-post-nul-bytes", "native text chunk rewrite", "the rewrite drops what follows the terminator",
			aiffFile("AIFF", stdCOMM(), aiffChunk("NAME", []byte("T\x00junk")), stdSSND())},
		{"aiff-split-group", "native text chunk rewrite", "regrouping moves the chunks between them",
			aiffFile("AIFF", stdCOMM(), aiffText("NAME", "T"), aiffChunk("APPL", []byte("xx")), aiffText("ANNO", "c"), stdSSND())},
		{"wav-unreadable-tail", "LIST/INFO rewrite", "the rewrite renders from the items alone",
			wavFile(wavFmtPCM(), infoTail, wavData(400))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ops := opsFor(t, tc.data, addCover); !slices.Contains(ops, tc.op) {
				t.Errorf("%s, so the rewrite must be reported: %v", tc.why, ops)
			}
			plan, err := mustParseBytes(t, tc.data).Edit().Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if !plan.IsNoOp() {
				t.Errorf("an edit naming nothing must still be a no-op; operations: %v", plan.Report().Operations)
			}
		})
	}
}
