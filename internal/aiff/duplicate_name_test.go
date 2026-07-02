package aiff

import (
	"context"
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestDuplicateNamePreserved is the regression guard for the finding that a blanket first-wins
// silently dropped preservable text values: two NAME chunks (both Title, a single-valued text
// key) must project BOTH values. AIFF maps no number/total key, so the number-pair guard never
// fires here; dropping the second value would be silent data loss, since the write then forces
// an ID3 chunk whose v2.4 TIT2 frame stores both NUL-separated.
func TestDuplicateNamePreserved(t *testing.T) {
	chunks := slices.Concat(aiffComm(), aiffSsnd(),
		aiffChunk("NAME", []byte("First")), aiffChunk("NAME", []byte("Second")))
	m, err := parse(context.Background(), core.BytesSource(formWrap(chunks, nil, nil)), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	vals, ok := m.Tags.Get(tag.Title)
	if !ok || len(vals) != 2 || vals[0] != "First" || vals[1] != "Second" {
		t.Fatalf("Title = %v (ok=%v), want both values [\"First\" \"Second\"] preserved", vals, ok)
	}
}

// TestDuplicateAnnoStillAccumulates guards the genuinely multi-valued path: Comment (ANNO) is
// multi-valued, so two ANNO chunks both project - unchanged by the number-pair guard.
func TestDuplicateAnnoStillAccumulates(t *testing.T) {
	chunks := slices.Concat(aiffComm(), aiffSsnd(),
		aiffChunk("ANNO", []byte("one")), aiffChunk("ANNO", []byte("two")))
	m, err := parse(context.Background(), core.BytesSource(formWrap(chunks, nil, nil)), core.DefaultParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	vals, ok := m.Tags.Get(tag.Comment)
	if !ok || len(vals) != 2 {
		t.Fatalf("Comment = %v (ok=%v), want two accumulated values", vals, ok)
	}
}
