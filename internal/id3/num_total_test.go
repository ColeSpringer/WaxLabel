package id3

import (
	"slices"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// frameValue returns the decoded first value of the first frame with the given ID.
func frameValue(frames []Frame, id string) (string, bool) {
	for _, f := range frames {
		if f.ID == id {
			if vs := DecodeText(f); len(vs) > 0 {
				return vs[0], true
			}
			return "", true
		}
	}
	return "", false
}

// TestNumTotalNonNumericDropsTotal: composing "n/total" into a TRCK/TPOS frame is

func TestNumTotalNonNumericDropsTotal(t *testing.T) {
	cases := []struct {
		name        string
		number      string
		total       string // canonical TRACKTOTAL; "" means none set
		wantTRCK    string
		wantDropped bool
	}{
		{"non-numeric number + canonical total drops total", "A1", "12", "A1", true},
		{"embedded total in a non-numeric number preserved verbatim", "A1/12", "", "A1/12", false},
		{"numeric pair composes", "3", "12", "3/12", false},
		{"non-numeric total also dropped", "3", "A", "3", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := tag.NewTagSet()
			edited := tag.NewTagSet()
			edited.Set(tag.TrackNumber, c.number)
			if c.total != "" {
				edited.Set(tag.TrackTotal, c.total)
			}
			out, info := RebuildFrames(nil, base, edited, 4, StructuredEdit{}, WriteOpts{})
			got, ok := frameValue(out, "TRCK")
			if !ok {
				t.Fatal("no TRCK frame emitted")
			}
			if got != c.wantTRCK {
				t.Errorf("TRCK = %q, want %q", got, c.wantTRCK)
			}
			if dropped := slices.Contains(info.DroppedTotals, tag.TrackTotal); dropped != c.wantDropped {
				t.Errorf("DroppedTotals has TRACKTOTAL = %v, want %v (DroppedTotals=%v)", dropped, c.wantDropped, info.DroppedTotals)
			}
			// The recorded drop must surface as a value-dropped warning keyed to the total (no other
			// container retains it here).
			ws := AppendRebuildWarnings(nil, info, tag.NewTagSet())
			warned := slices.ContainsFunc(ws, func(w core.Warning) bool {
				return w.Code == core.WarnValueDropped && slices.Contains(w.Keys, tag.TrackTotal)
			})
			if warned != c.wantDropped {
				t.Errorf("value-dropped warning keyed to TRACKTOTAL = %v, want %v (warnings=%v)", warned, c.wantDropped, ws)
			}
		})
	}
}

// TestNumTotalDropIdempotent covers the idempotency half: after the total is dropped,
// re-rendering the retained number (now with no total) writes the same value and flags nothing.
func TestNumTotalDropIdempotent(t *testing.T) {
	base := tag.NewTagSet()
	edited := tag.NewTagSet()
	edited.Set(tag.TrackNumber, "A1") // the retained state after the first write
	out, info := RebuildFrames(nil, base, edited, 4, StructuredEdit{}, WriteOpts{})
	if got, _ := frameValue(out, "TRCK"); got != "A1" {
		t.Errorf("TRCK = %q, want \"A1\"", got)
	}
	if len(info.DroppedTotals) != 0 {
		t.Errorf("the second pass (no total) flagged a drop: %v", info.DroppedTotals)
	}
}

// TestNumTotalUnchangedPairNotFlagged: an unchanged track pair (base == edited) is never flagged

func TestNumTotalUnchangedPairNotFlagged(t *testing.T) {
	base := tag.NewTagSet()
	base.Set(tag.TrackNumber, "A1")
	base.Set(tag.TrackTotal, "12")
	edited := tag.NewTagSet()
	edited.Set(tag.TrackNumber, "A1")
	edited.Set(tag.TrackTotal, "12")
	if _, info := RebuildFrames(nil, base, edited, 4, StructuredEdit{}, WriteOpts{}); len(info.DroppedTotals) != 0 {
		t.Errorf("an unchanged track pair must not be flagged dropped, got %v", info.DroppedTotals)
	}
}

// TestExtractDatePartYearBounded: extractDatePart's year component is exactly

func TestExtractDatePartYearBounded(t *testing.T) {
	for _, c := range []struct {
		iso  string
		want string
	}{
		{"2021", "2021"},
		{"2021-05", "2021"},
		{"2021-05-03", "2021"},
		{"2021-05-03T10:30", "2021"},
		{"10000", ""},      // 5-digit year: not truncated to "1000"
		{"20210503", ""},   // compact form: not truncated to "2021"
		{"2021.05.03", ""}, // dotted form
		{"2021/05", ""},    // slash separator
		{"not-a-date", ""},
	} {
		if got := extractDatePart(c.iso, partYear); got != c.want {
			t.Errorf("extractDatePart(%q, partYear) = %q, want %q", c.iso, got, c.want)
		}
	}
}
