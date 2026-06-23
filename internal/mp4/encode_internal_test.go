package mp4

import (
	"testing"

	"github.com/colespringer/waxlabel/tag"
)

// TestDroppedValues (F1): droppedValues names exactly the canonical keys whose value
// the iTunes atoms cannot represent and the encoder silently drops. trkn/disk are
// uint16, so a non-numeric, negative, or >65535 value is lost - and the per-slot
// detection names the offending slot (TRACKTOTAL, not the merged pair). stik is
// uint32, so a large 70000 stores fine while a non-numeric or negative value is lost.
// The pair encoder treats a literal 0 as "absent", so it is never a drop.
func TestDroppedValues(t *testing.T) {
	cases := []struct {
		name string
		set  map[tag.Key]string
		want []tag.Key
	}{
		{"track non-numeric", map[tag.Key]string{tag.TrackNumber: "abc"}, []tag.Key{tag.TrackNumber}},
		{"track overflow", map[tag.Key]string{tag.TrackNumber: "70000"}, []tag.Key{tag.TrackNumber}},
		{"track negative", map[tag.Key]string{tag.TrackNumber: "-3"}, []tag.Key{tag.TrackNumber}},
		{"track zero is absent", map[tag.Key]string{tag.TrackNumber: "0"}, nil},
		{"track valid", map[tag.Key]string{tag.TrackNumber: "3"}, nil},
		{"total slot named, not the pair", map[tag.Key]string{tag.TrackNumber: "3", tag.TrackTotal: "abc"}, []tag.Key{tag.TrackTotal}},
		{"explicit total overrides the /tail", map[tag.Key]string{tag.TrackNumber: "3/5", tag.TrackTotal: "abc"}, []tag.Key{tag.TrackTotal}},
		{"whitespace total overrides, no false drop", map[tag.Key]string{tag.TrackNumber: "3/70000", tag.TrackTotal: "   "}, nil},
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
