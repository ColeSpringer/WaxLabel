package matroska

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/tag"
)

// The keys of the SimpleTags cut at a NUL are listed once each, in the order met, in one pass
// whatever their number. The bound is far above what the pass takes on a slow runner under the race
// detector; a search of the listed keys for each SimpleTag takes over a minute at this size.
func TestCutWarningsListKeysInOnePass(t *testing.T) {
	d := &doc{groups: []tagGroup{{tags: []simpleTag{
		{name: "GENRE", value: "a", hasValue: true, cut: 1},
		{name: "MOOD", value: "b", hasValue: true},
		{name: "GENRE", value: "c", hasValue: true, cut: 2},
		{name: "COMPOSER", value: "d", hasValue: true, cut: 1},
	}}}}
	ws := cutWarnings(d)
	if len(ws) != 1 || !slices.Equal(ws[0].Keys, []tag.Key{tag.Genre, tag.Composer}) {
		t.Fatalf("warnings = %+v, want one keyed GENRE and COMPOSER", ws)
	}

	const n = 500000
	tags := make([]simpleTag, n)
	for i := range tags {
		tags[i] = simpleTag{name: "K" + strconv.Itoa(i), value: "v", hasValue: true, cut: 1}
	}
	d = &doc{groups: []tagGroup{{tags: tags}}}
	start := time.Now()
	ws = cutWarnings(d)
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("the warning took %v", took)
	}
	if len(ws) != 1 || len(ws[0].Keys) != n {
		t.Fatalf("warnings = %d, want one keyed by all %d names", len(ws), n)
	}
}
