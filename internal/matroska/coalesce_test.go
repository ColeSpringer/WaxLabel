package matroska

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/bits"
	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
)

// countChildID counts the top-level child descriptors carrying the given EBML id.
func countChildID(children []l1elem, id uint64) int {
	n := 0
	for _, c := range children {
		if c.id == id {
			n++
		}
	}
	return n
}

func emptyCluster() []byte { return encElement(idCluster, nil) } // 4-byte ID + 1-byte zero size
func mkInfo(title string) []byte {
	return encElement(idInfo, stringElement(idSegTitle, title))
}

// segBytes wraps a Segment body in the EBML header + Segment framing the parser expects.
func segBytes(body []byte) []byte {
	out := encElement(idEBML, stringElement(idDocType, "matroska"))
	return append(out, encElement(idSegment, body)...)
}

func parseMKA(t *testing.T, data []byte) *core.Media {
	t.Helper()
	m, err := parse(context.Background(), core.BytesSource(data), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

// TestCoalesceClusterDescriptors checks that a contiguous run of minimum-size empty
// Clusters retains one coalesced descriptor instead of one descriptor per cluster. A
// non-cluster element between two runs breaks them into two descriptors.
func TestCoalesceClusterDescriptors(t *testing.T) {
	const n = 200000
	body := append(mkInfo("x"), bytes.Repeat(emptyCluster(), n)...)
	m := parseMKA(t, segBytes(body))
	if got := countChildID(m.Native.(*doc).wb.children, idCluster); got != 1 {
		t.Errorf("%d empty clusters: %d cluster descriptors, want 1 (coalesced)", n, got)
	}

	// Cluster run / Void / cluster run: the Void breaks the run, so exactly two
	// cluster descriptors plus the one Void descriptor.
	void := encElement(idVoid, []byte{0x00, 0x00})
	body2 := mkInfo("x")
	body2 = append(body2, bytes.Repeat(emptyCluster(), 3)...)
	body2 = append(body2, void...)
	body2 = append(body2, bytes.Repeat(emptyCluster(), 3)...)
	wb2 := parseMKA(t, segBytes(body2)).Native.(*doc).wb
	if got := countChildID(wb2.children, idCluster); got != 2 {
		t.Errorf("clusters/Void/clusters: %d cluster descriptors, want 2", got)
	}
	if got := countChildID(wb2.children, idVoid); got != 1 {
		t.Errorf("clusters/Void/clusters: %d Void descriptors, want 1", got)
	}
}

// TestCoalesceAlternationRejectedPastCap checks the separator case: a
// Cluster/Void/Cluster/Void sequence defeats coalescing because each Void breaks the
// run. Charging those separators against the element budget keeps descriptor growth
// bounded and rejects an over-cap flood cleanly.
func TestCoalesceAlternationRejectedPastCap(t *testing.T) {
	n := bits.DefaultLimits.MaxElements + 100
	void := encElement(idVoid, nil)
	cluster := emptyCluster()
	body := make([]byte, 0, len(mkInfo("x"))+n*(len(void)+len(cluster)))
	body = append(body, mkInfo("x")...)
	for i := 0; i < n; i++ {
		body = append(body, cluster...)
		body = append(body, void...)
	}
	_, err := parse(context.Background(), core.BytesSource(segBytes(body)), core.DefaultParseOptions())
	if !errors.Is(err, waxerr.ErrSizeTooLarge) {
		t.Fatalf("Cluster/Void alternation past the element cap: err = %v, want ErrSizeTooLarge", err)
	}
}

// TestCoalesceWriteRoundTripsDescriptorCount checks that the writer and parser
// coalesce the same way. After a shift-path edit on a 3-cluster file, both the
// returned result document and a fresh parse of the written bytes should hold one
// cluster descriptor.
func TestCoalesceWriteRoundTripsDescriptorCount(t *testing.T) {
	body := append(mkInfo("x"), bytes.Repeat(emptyCluster(), 3)...)
	src := segBytes(body)
	base := parseMKA(t, src)
	if got := countChildID(base.Native.(*doc).wb.children, idCluster); got != 1 {
		t.Fatalf("source: %d cluster descriptors, want 1", got)
	}

	edited := base.Clone()
	bigTitle := strings.Repeat("T", 300) // grows Info, shifting the clusters (shift path)
	edited.Tags.Set(tag.Title, bigTitle)
	d := edited.Native.(*doc)
	ch := detectChanges(base, edited)
	if !ch.title {
		t.Fatal("setup: title change not detected")
	}
	plan, err := planShift(d, base, edited, ch, editedKeySet(base.Tags, edited.Tags), core.WriteReport{})
	if err != nil {
		t.Fatalf("planShift: %v", err)
	}
	if got := countChildID(plan.Result.Native.(*doc).wb.children, idCluster); got != 1 {
		t.Errorf("result document: %d cluster descriptors, want 1", got)
	}

	var buf bytes.Buffer
	if _, err := bits.Write(context.Background(), &buf, core.BytesSource(src), plan.Segments, nil); err != nil {
		t.Fatalf("bits.Write: %v", err)
	}
	re := parseMKA(t, buf.Bytes())
	if got := countChildID(re.Native.(*doc).wb.children, idCluster); got != 1 {
		t.Errorf("reparsed output: %d cluster descriptors, want 1", got)
	}
	if got, _ := re.Tags.First(tag.Title); got != bigTitle {
		t.Errorf("reparsed title = %q, want the edited title", got)
	}
}

// TestOffsetMapLookup covers the shift-path offset resolver's three cases: a direct
// per-element hit, an interior cluster-run offset mapped by the run's uniform shift,
// and the half-open [start, end) boundary. An offset at runEnd must resolve through
// the next element's direct mapping, never through the run.
func TestOffsetMapLookup(t *testing.T) {
	// run [100,130) shifts +1000; the element right after it (origStart 130) maps to a
	// deliberately distinct 9999 so a boundary hit can be told apart from run + shift.
	om := offsetMap{
		direct: map[int64]int64{100: 1100, 130: 9999},
		runs:   []clusterRun{{start: 100, end: 130, shift: 1000}},
	}
	cases := []struct {
		abs    int64
		want   int64
		wantOK bool
		note   string
	}{
		{100, 1100, true, "run start: direct hit"},
		{110, 1110, true, "interior: via run shift"},
		{129, 1129, true, "last interior byte: via run shift"},
		{130, 9999, true, "boundary (first byte after run): next element's direct mapping wins, not run+shift (1130)"},
		{90, 0, false, "before the run, no direct key"},
		{200, 0, false, "past everything, no key"},
	}
	for _, c := range cases {
		got, ok := om.lookup(c.abs)
		if ok != c.wantOK || (ok && got != c.want) {
			t.Errorf("lookup(%d) = (%d,%v), want (%d,%v) [%s]", c.abs, got, ok, c.want, c.wantOK, c.note)
		}
	}

	// With no direct key covering runEnd, the half-open run must not claim it.
	bare := offsetMap{direct: map[int64]int64{100: 1100}, runs: []clusterRun{{start: 100, end: 130, shift: 1000}}}
	if _, ok := bare.lookup(130); ok {
		t.Error("lookup at runEnd must miss the half-open [start,end) run when no direct key covers it")
	}

	// Several ascending, non-overlapping runs with gaps between them: the binary search
	// must select the one containing the offset, and miss in a gap or past the end.
	multi := offsetMap{runs: []clusterRun{
		{start: 100, end: 200, shift: 10},
		{start: 300, end: 400, shift: 20},
		{start: 500, end: 600, shift: 30},
	}}
	for _, c := range []struct {
		abs    int64
		want   int64
		wantOK bool
	}{
		{100, 110, true}, // first run, at start
		{150, 160, true}, // first run, interior
		{250, 0, false},  // gap between run 1 and 2
		{300, 320, true}, // second run, at start
		{399, 419, true}, // second run, last interior byte
		{400, 0, false},  // boundary just past run 2 (half-open), no run here
		{550, 580, true}, // third run
		{600, 0, false},  // past the last run's end
		{50, 0, false},   // before the first run
	} {
		got, ok := multi.lookup(c.abs)
		if ok != c.wantOK || (ok && got != c.want) {
			t.Errorf("multi.lookup(%d) = (%d,%v), want (%d,%v)", c.abs, got, ok, c.want, c.wantOK)
		}
	}
}
