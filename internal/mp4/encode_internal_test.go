package mp4

import (
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
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

// TestCoercedValues checks that coercedValues names a trkn/disk slot stored in a normalized form
// (a leading zero or sign the uint16 atom drops) and carries the canonical integer it becomes, so
// the write warning can show the on-disk value. A dropped slot (0, overflow, non-numeric) is not
// here, because drop and reduce are mutually exclusive, and COMPILATION's coercion carries no
// Normalized (its stored form is always "0 (false)").
func TestCoercedValues(t *testing.T) {
	cases := []struct {
		name       string
		set        map[tag.Key]string
		wantKey    tag.Key // the number key expected coerced, or "" for none
		wantNormal string  // the expected Normalized value
	}{
		{"leading zero", map[tag.Key]string{tag.TrackNumber: "03"}, tag.TrackNumber, "3"},
		{"signed", map[tag.Key]string{tag.DiscNumber: "+2"}, tag.DiscNumber, "2"},
		{"total leading zero", map[tag.Key]string{tag.TrackTotal: "09"}, tag.TrackTotal, "9"},
		{"embedded total normalized", map[tag.Key]string{tag.TrackNumber: "3/09"}, tag.TrackTotal, "9"},
		{"canonical is not coerced", map[tag.Key]string{tag.TrackNumber: "3"}, "", ""},
		{"zero is dropped not coerced", map[tag.Key]string{tag.TrackNumber: "0"}, "", ""},
		{"overflow is dropped not coerced", map[tag.Key]string{tag.TrackNumber: "70000"}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := tag.NewTagSet()
			for k, v := range c.set {
				ts.Set(k, v)
			}
			cvs := coercedValues(ts)
			if c.wantKey == "" {
				for _, cv := range cvs {
					if cv.Key == tag.TrackNumber || cv.Key == tag.TrackTotal ||
						cv.Key == tag.DiscNumber || cv.Key == tag.DiscTotal {
						t.Errorf("expected no number coercion, got %+v", cv)
					}
				}
				return
			}
			var found *droppedValue
			for i := range cvs {
				if cvs[i].Key == c.wantKey {
					found = &cvs[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("expected a coercion for %s, got %+v", c.wantKey, cvs)
			}
			if found.Normalized != c.wantNormal {
				t.Errorf("Normalized = %q, want %q", found.Normalized, c.wantNormal)
			}
		})
	}
}

// TestNumberSlotTransferGrading checks that a copy grades a normalized trkn/disk number (a leading
// zero or sign) Lossy, a canonical one Carried, and an unrepresentable one (overflow, or the
// 0-reads-back-absent case) Dropped. Without the reduction predicate a copy graded 03 as Carried
// while diff reported the 03 -> 3 change, the contradiction this pins shut.
func TestNumberSlotTransferGrading(t *testing.T) {
	caps := Codec{}.Capabilities(nil, core.WriteOptions{})
	cases := []struct {
		name string
		key  tag.Key
		val  string
		want core.Disposition
	}{
		{"number leading zero is lossy", tag.TrackNumber, "03", core.Lossy},
		{"number signed is lossy", tag.TrackNumber, "+3", core.Lossy},
		{"number canonical is carried", tag.TrackNumber, "3", core.Carried},
		{"number ceiling is carried", tag.TrackNumber, "65535", core.Carried},
		{"number overflow is dropped", tag.TrackNumber, "70000", core.Dropped},
		{"number zero is dropped", tag.TrackNumber, "0", core.Dropped},
		{"number double-zero is dropped", tag.TrackNumber, "00", core.Dropped},
		{"disc leading zero is lossy", tag.DiscNumber, "03", core.Lossy},
		{"total leading zero is lossy", tag.TrackTotal, "03", core.Lossy},
		{"total canonical is carried", tag.TrackTotal, "12", core.Carried},
		{"total zero is dropped", tag.TrackTotal, "0", core.Dropped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := tag.NewTagSet()
			ts.Set(c.key, c.val)
			got := dispositionOf(core.ProjectTransfer(&core.Media{Tags: ts}, caps), c.key)
			if got != c.want {
				t.Errorf("disposition of %s=%q = %v, want %v", c.key, c.val, got, c.want)
			}
		})
	}
}

// TestNumberDispositionAgreesWithRoundTrip pins the contract directly: for a range of number forms,
// copy's per-item disposition and diff's equality verdict must agree. A form is graded Carried by
// copy exactly when it round-trips byte-identically through the real encode (pairItem) and decode
// (decodePair), which is exactly when diff would report no change. The bug this guards against was
// copy and diff disagreeing, so it asserts the whole property, not just today's examples.
func TestNumberDispositionAgreesWithRoundTrip(t *testing.T) {
	caps := Codec{}.Capabilities(nil, core.WriteOptions{})
	forms := []string{"3", "12", "65535", "03", "003", "+3", "0", "00", "+0", "70000", "-3", "abc"}
	for _, form := range forms {
		t.Run(form, func(t *testing.T) {
			ts := tag.NewTagSet()
			ts.Set(tag.TrackNumber, form)
			disp := dispositionOf(core.ProjectTransfer(&core.Media{Tags: ts}, caps), tag.TrackNumber)
			back, present := roundTripTrackNumber(form)
			diffEqual := present && back == form
			if (disp == core.Carried) != diffEqual {
				t.Errorf("form %q: copy disposition=%v but diff-equal=%v (round-trip=%q present=%v) - copy and diff disagree",
					form, disp, diffEqual, back, present)
			}
		})
	}
}

// roundTripTrackNumber stores form in a trkn number slot via the real encoder and reads it back via
// the real decoder, returning the read-back value and whether the number slot survived (a 0/overflow
// slot reads back absent). It is the faithful "what does MP4 store and read back" diff would compare.
func roundTripTrackNumber(form string) (string, bool) {
	ts := tag.NewTagSet()
	ts.Set(tag.TrackNumber, form)
	// Pair with a fixed real total so the atom is always emitted (pairItem collapses an all-zero
	// pair), isolating the number slot's round-trip from the pair-level collapse.
	ts.Set(tag.TrackTotal, "99")
	it, ok := pairItem("trkn", ts, tag.TrackNumber, tag.TrackTotal, true)
	if !ok {
		return "", false
	}
	for _, c := range decodePair(it, tag.TrackNumber, tag.TrackTotal).contribs {
		if c.Key == tag.TrackNumber {
			return c.Value, true
		}
	}
	return "", false
}

// dispositionOf returns the disposition ProjectTransfer assigned the field item for key.
func dispositionOf(items []core.TransferItem, key tag.Key) core.Disposition {
	for _, it := range items {
		if it.Kind == core.TransferField && it.Key == key {
			return it.Disposition
		}
	}
	return core.Excluded // not found: a sentinel distinct from Carried/Lossy/Dropped
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
