package wav

import (
	"context"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// infoItemBytes renders one LIST/INFO item as parseInfo reads it: a 4-byte id, a
// little-endian length, the value, and a word-alignment pad byte for an odd length.
func infoItemBytes(id, val string) []byte {
	b := append([]byte(id), le32(uint32(len(val)))...)
	b = append(b, val...)
	if len(val)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func wavWithInfo(items ...[2]string) []byte {
	body := []byte("INFO")
	for _, it := range items {
		body = append(body, infoItemBytes(it[0], it[1])...)
	}
	chunks := slices.Concat(wavFmtChunk(), wavChunk("data", []byte{0, 0, 0, 0}), wavChunk("LIST", body))
	return riffWrap(chunks, nil, nil)
}

// TestDuplicateNumberInfoFirstWins covers the number-pair cardinality guard: two INFO items
// mapping to one number key (two IPRT, both TrackNumber) project a single first-wins value,
// not a phantom multi-value TRACKNUMBER that no writer can store - which would diff as a
// spurious change and trip a false native-value-reduced warning. The family view still exposes
// the conflict (the second item reads back unselected).
func TestDuplicateNumberInfoFirstWins(t *testing.T) {
	m, err := parse(context.Background(), core.BytesSource(wavWithInfo([2]string{"IPRT", "1"}, [2]string{"IPRT", "2"})), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	vals, ok := m.Tags.Get(tag.TrackNumber)
	if !ok || len(vals) != 1 || vals[0] != "1" {
		t.Fatalf("TrackNumber = %v (ok=%v), want first-wins [\"1\"]", vals, ok)
	}
	if ws := nativeReducedWarnings(m.Tags); len(ws) != 0 {
		t.Errorf("first-wins TrackNumber must not warn native-value-reduced, got %v", ws)
	}
	unselected := 0
	for _, fv := range m.Families {
		if fv.Key == tag.TrackNumber && !fv.Selected {
			unselected++
		}
	}
	if unselected != 1 {
		t.Errorf("family view should mark the duplicate IPRT unselected, got %d unselected", unselected)
	}
}

// TestDuplicateTextInfoPreserved is the regression guard for the finding that the blanket
// first-wins silently dropped preservable text values: two INAM items (both Title, a
// single-valued text key) must project BOTH values, because the write then forces an ID3
// chunk whose v2.4 TIT2 frame stores both NUL-separated. Dropping the second at read would
// lose data that round-trips. nativeReducedWarnings fires accurately (both kept in ID3).
func TestDuplicateTextInfoPreserved(t *testing.T) {
	m, err := parse(context.Background(), core.BytesSource(wavWithInfo([2]string{"INAM", "A"}, [2]string{"INAM", "B"})), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	vals, ok := m.Tags.Get(tag.Title)
	if !ok || len(vals) != 2 || vals[0] != "A" || vals[1] != "B" {
		t.Fatalf("Title = %v (ok=%v), want both values [\"A\" \"B\"] preserved", vals, ok)
	}
	// The reduction to the single-valued INFO container is real and preserved in ID3, so the
	// warning here is accurate, not the false one the number-pair case produced.
	if ws := nativeReducedWarnings(m.Tags); len(ws) != 1 {
		t.Errorf("duplicate Title should warn native-value-reduced exactly once, got %v", ws)
	}
}
