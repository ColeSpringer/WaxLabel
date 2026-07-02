package mp4

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestDroppedValues checks that droppedValues names exactly the canonical keys an MP4
// write cannot store without mutation. Track and disc slots are uint16, so
// non-numeric, negative, and >65535 values are lost. A literal "0" fits uint16, but
// pairItem can collapse the whole pair to absent; that user-supplied 0 is reported
// unless a non-zero counterpart keeps the pair.
func TestDroppedValues(t *testing.T) {
	cases := []struct {
		name string
		set  map[tag.Key]string
		want []tag.Key
	}{
		{"track non-numeric", map[tag.Key]string{tag.TrackNumber: "abc"}, []tag.Key{tag.TrackNumber}},
		{"track overflow", map[tag.Key]string{tag.TrackNumber: "70000"}, []tag.Key{tag.TrackNumber}},
		{"track negative", map[tag.Key]string{tag.TrackNumber: "-3"}, []tag.Key{tag.TrackNumber}},
		{"track zero collapses the pair, flagged", map[tag.Key]string{tag.TrackNumber: "0"}, []tag.Key{tag.TrackNumber}},
		{"track zero with a real total still drops the zero number", map[tag.Key]string{tag.TrackNumber: "0", tag.TrackTotal: "12"}, []tag.Key{tag.TrackNumber}},
		{"both zero flags both slots", map[tag.Key]string{tag.TrackNumber: "0", tag.TrackTotal: "0"}, []tag.Key{tag.TrackNumber, tag.TrackTotal}},
		{"total zero alone collapses the pair", map[tag.Key]string{tag.TrackTotal: "0"}, []tag.Key{tag.TrackTotal}},
		{"track valid", map[tag.Key]string{tag.TrackNumber: "3"}, nil},
		{"total slot named, not the pair", map[tag.Key]string{tag.TrackNumber: "3", tag.TrackTotal: "abc"}, []tag.Key{tag.TrackTotal}},
		{"explicit total overrides the /tail", map[tag.Key]string{tag.TrackNumber: "3/5", tag.TrackTotal: "abc"}, []tag.Key{tag.TrackTotal}},
		{"whitespace total overrides, no false drop", map[tag.Key]string{tag.TrackNumber: "3/70000", tag.TrackTotal: "   "}, nil},
		{"disc zero collapses the pair, flagged", map[tag.Key]string{tag.DiscNumber: "0"}, []tag.Key{tag.DiscNumber}},
		{"disc non-numeric", map[tag.Key]string{tag.DiscNumber: "x"}, []tag.Key{tag.DiscNumber}},
		{"mediatype non-numeric", map[tag.Key]string{tag.MediaType: "abc"}, []tag.Key{tag.MediaType}},
		{"mediatype uint32 stores fine", map[tag.Key]string{tag.MediaType: "70000"}, nil},
		{"mediatype negative", map[tag.Key]string{tag.MediaType: "-1"}, []tag.Key{tag.MediaType}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := tag.NewTagSet()
			for k, v := range c.set {
				ts.Set(k, v)
			}
			var got []tag.Key
			for _, dv := range droppedValues(ts) {
				got = append(got, dv.Key)
			}
			if !sameKeySet(got, c.want) {
				t.Errorf("droppedValues keys = %v, want %v", got, c.want)
			}
		})
	}
}

// sameKeySet reports whether a and b hold the same keys (order-independent).
func sameKeySet(a, b []tag.Key) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[tag.Key]int{}
	for _, k := range a {
		m[k]++
	}
	for _, k := range b {
		m[k]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}
