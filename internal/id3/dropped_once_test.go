package id3

import (
	"slices"
	"strings"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestOneDropWarnsOnce holds the rebuild's value-dropped warnings to one per loss. A key whose
// value is dropped whole (no frame takes an empty one, a total has no number to join, a v2.3
// date has no year) is not also told of the trailing empty that went with it, an empty credit
// is the involved-people frame's alone to report, and a number frame, which holds one number
// and one total, reports every value past the first itself, an empty one included, whatever
// the multi-value policy.
func TestOneDropWarnsOnce(t *testing.T) {
	type set map[tag.Key][]string
	const (
		empty    = "an empty value cannot be represented"
		credit   = "an empty credit value"
		trailing = "a trailing empty value"
		joined   = "stored joined"
		first    = "stores only the first"
		total    = "cannot be represented in ID3 because the number"
		movement = "cannot join the movement number"
		year     = "no valid 4-digit year"
	)
	cases := []struct {
		name string
		edit set
		key  tag.Key
		// want is the one warning expected for key, "" for none, by version and policy: v2.4,
		// v2.3 NUL-separated, v2.3 repeat-frame, v2.3 slash.
		want [4]string
	}{
		{"number, all empty", set{tag.TrackNumber: {"", ""}}, tag.TrackNumber, [4]string{empty, empty, empty, empty}},
		{"total, all empty", set{tag.TrackTotal: {"", "", ""}}, tag.TrackTotal, [4]string{empty, empty, empty, empty}},
		{"disc, all empty", set{tag.DiscNumber: {"", ""}}, tag.DiscNumber, [4]string{empty, empty, empty, empty}},
		{"movement, all empty", set{tag.Movement: {"", ""}}, tag.Movement, [4]string{empty, empty, empty, empty}},
		{"genre, all empty", set{tag.Genre: {"", ""}}, tag.Genre, [4]string{empty, empty, empty, empty}},
		{"date, all empty", set{tag.RecordingDate: {"", ""}}, tag.RecordingDate, [4]string{empty, empty, empty, empty}},
		{"recording id, all empty", set{tag.MBRecordingID: {"", ""}}, tag.MBRecordingID, [4]string{empty, empty, empty, empty}},
		{"credit, one empty", set{tag.Producer: {""}}, tag.Producer, [4]string{credit, credit, credit, credit}},
		{"credit, all empty", set{tag.Producer: {"", ""}}, tag.Producer, [4]string{credit, credit, credit, credit}},
		{"total beside a number that is none", set{tag.TrackNumber: {"A1"}, tag.TrackTotal: {"12", ""}}, tag.TrackTotal,
			[4]string{total, total, total, total}},
		{"totals beside a number that is none", set{tag.TrackNumber: {"A1"}, tag.TrackTotal: {"12", "13"}}, tag.TrackTotal,
			[4]string{total, total, total, total}},
		{"movement total beside a movement that is none", set{tag.Movement: {"x"}, tag.MovementTotal: {"5", ""}}, tag.MovementTotal,
			[4]string{movement, movement, movement, movement}},
		{"date with no year", set{tag.RecordingDate: {"garbage", ""}}, tag.RecordingDate, [4]string{trailing, year, year, year}},
		{"number, then an empty", set{tag.TrackNumber: {"3", ""}}, tag.TrackNumber, [4]string{first, first, first, first}},
		{"numbers, then an empty", set{tag.TrackNumber: {"3", "4", ""}}, tag.TrackNumber, [4]string{first, first, first, first}},
		{"total, then an empty", set{tag.TrackNumber: {"3"}, tag.TrackTotal: {"12", ""}}, tag.TrackTotal, [4]string{first, first, first, first}},
		{"movement, then an empty", set{tag.Movement: {"2", ""}}, tag.Movement, [4]string{first, first, first, first}},
		{"one number", set{tag.TrackNumber: {"3"}, tag.TrackTotal: {"12"}}, tag.TrackNumber, [4]string{}},
		// A text frame holds its values as a list: only a NUL-separated one loses a trailing empty.
		{"text, then an empty", set{tag.Artist: {"A", ""}}, tag.Artist, [4]string{trailing, trailing, "", joined}},
		{"text, all empty", set{tag.Title: {"", ""}}, tag.Title, [4]string{trailing, trailing, "", joined}},
		{"text, two values", set{tag.Artist: {"A", "B"}}, tag.Artist, [4]string{"", "", "", joined}},
	}
	writes := []struct {
		name    string
		version byte
		pol     core.ID3MultiValuePolicy
	}{
		{"v2.4", 4, core.ID3MultiNullSep},
		{"v2.3 null", 3, core.ID3MultiNullSep},
		{"v2.3 repeat", 3, core.ID3MultiRepeatFrame},
		{"v2.3 slash", 3, core.ID3MultiSlash},
	}
	for _, c := range cases {
		for i, w := range writes {
			edited := tag.NewTagSet()
			for k, vals := range c.edit {
				edited.Set(k, vals...)
			}
			frames, info := RebuildFrames(nil, tag.NewTagSet(), edited, w.version, StructuredEdit{}, WriteOpts{Multi: w.pol})
			retained := frameValues(frames, w.version)
			var got []string
			for _, warn := range core.WarningsWithCode(AppendRebuildWarnings(nil, info, retained), core.WarnValueDropped) {
				if slices.Contains(warn.Keys, c.key) {
					got = append(got, warn.Message)
				}
			}
			switch want := c.want[i]; {
			case want == "" && len(got) != 0:
				t.Errorf("%s, %s: %s warned, want nothing: %q", c.name, w.name, c.key, got)
			case want != "" && (len(got) != 1 || !strings.Contains(got[0], want)):
				t.Errorf("%s, %s: %s warnings = %q, want one holding %q", c.name, w.name, c.key, got, want)
			}
		}
	}
}
