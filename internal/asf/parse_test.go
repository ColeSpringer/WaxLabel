package asf

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/colespringer/waxlabel/tag"
)

// The texts cut at a NUL are counted and their keys listed once each, in the order met, in one pass
// whatever their number. The bound is far above what the pass takes on a slow runner under the race
// detector; a search of the listed keys for each text takes minutes at this size.
func TestCutCounts(t *testing.T) {
	var c cutCounts
	c.note(tag.Title, true)
	c.note(tag.Genre, false, true)
	c.note("", true, true)      // a descriptor no key reads
	c.note(tag.Title, true)     // a key met before
	c.note(tag.Composer, false) // nothing cut
	if c.descriptors != 5 || !slices.Equal(c.keys, []tag.Key{tag.Title, tag.Genre}) {
		t.Errorf("counted %d keyed %v, want 5 keyed [TITLE GENRE]", c.descriptors, c.keys)
	}
	c.markers = 2
	ws := c.warnings(nil)
	if len(ws) != 2 || !strings.Contains(ws[0].Message, "5 descriptor name(s) or value(s)") || !strings.Contains(ws[1].Message, "2 marker description(s)") {
		t.Errorf("warnings = %+v, want the descriptors, then the markers", ws)
	}
	if ws := (cutCounts{}).warnings(nil); len(ws) != 0 {
		t.Errorf("no cuts: warnings = %+v, want none", ws)
	}

	const n = 600000
	var many cutCounts
	start := time.Now()
	for i := range n {
		many.note(tag.Key("K"+strconv.Itoa(i)), true)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("counting %d cut texts took %v", n, d)
	}
	if len(many.keys) != n {
		t.Errorf("keys = %d, want %d", len(many.keys), n)
	}
}
