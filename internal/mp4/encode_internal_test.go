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

// TestBoolItemDropsEmpty checks that boolItem drops a present-empty COMPILATION value rather
// than fabricating a definite 0 (which would read back as a real, strict-clean false). This
// mirrors the MP4 empty-number drop; the remaining cross-format split - MP4 drops the empty
// value while FLAC keeps it verbatim - is intentional and matches how numbers behave, so a
// future reader should not "fix" it into a fabricated 0.
func TestBoolItemDropsEmpty(t *testing.T) {
	for _, v := range []string{"", "   "} {
		if _, ok := boolItem("cpil", []string{v}); ok {
			t.Errorf("boolItem(%q) stored an atom, want dropped (a present-empty value is not a definite 0)", v)
		}
	}
	if _, ok := boolItem("cpil", nil); ok {
		t.Error("boolItem(nil) stored an atom, want dropped")
	}
	// A real boolean still stores.
	if _, ok := boolItem("cpil", []string{"1"}); !ok {
		t.Error("boolItem(\"1\") dropped a real boolean")
	}
}

// TestRestoreUnstorablePairSlots pins the gate for preserving a good existing trkn/disk value
// when an edit makes a slot genuinely unstorable: it restores from base only when the edited
// value is unstorable AND base holds a storable, present value. A representable 0, an empty
// slot, an unstorable base, and a storable edit are all left untouched.
func TestRestoreUnstorablePairSlots(t *testing.T) {
	cases := []struct {
		name         string
		base, edited map[tag.Key]string
		wantRestored bool
		wantVals     map[tag.Key]string // checked when restored
	}{
		{"overflow restores base", map[tag.Key]string{tag.TrackNumber: "2"}, map[tag.Key]string{tag.TrackNumber: "99999"}, true, map[tag.Key]string{tag.TrackNumber: "2"}},
		{"non-numeric restores base", map[tag.Key]string{tag.TrackNumber: "5"}, map[tag.Key]string{tag.TrackNumber: "abc"}, true, map[tag.Key]string{tag.TrackNumber: "5"}},
		{"fractional restores base", map[tag.Key]string{tag.TrackNumber: "5"}, map[tag.Key]string{tag.TrackNumber: "3.5"}, true, map[tag.Key]string{tag.TrackNumber: "5"}},
		{"disc slot restores base", map[tag.Key]string{tag.DiscNumber: "1"}, map[tag.Key]string{tag.DiscNumber: "x"}, true, map[tag.Key]string{tag.DiscNumber: "1"}},
		{"only the unstorable slot is restored", map[tag.Key]string{tag.TrackNumber: "2", tag.TrackTotal: "9"}, map[tag.Key]string{tag.TrackNumber: "99999", tag.TrackTotal: "12"}, true, map[tag.Key]string{tag.TrackNumber: "2", tag.TrackTotal: "12"}},
		{"no base value: nothing to restore", nil, map[tag.Key]string{tag.TrackNumber: "99999"}, false, nil},
		{"unstorable base is not restored", map[tag.Key]string{tag.TrackNumber: "88888"}, map[tag.Key]string{tag.TrackNumber: "99999"}, false, nil},
		{"literal zero is left alone (ZeroUnset)", map[tag.Key]string{tag.TrackNumber: "2"}, map[tag.Key]string{tag.TrackNumber: "0"}, false, nil},
		{"empty edited slot stays cleared", map[tag.Key]string{tag.TrackNumber: "2"}, map[tag.Key]string{tag.TrackNumber: ""}, false, nil},
		{"storable edit is not restored", map[tag.Key]string{tag.TrackNumber: "2"}, map[tag.Key]string{tag.TrackNumber: "3"}, false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, edited := tag.NewTagSet(), tag.NewTagSet()
			for k, v := range c.base {
				base.Set(k, v)
			}
			for k, v := range c.edited {
				edited.Set(k, v)
			}
			out, restored := restoreUnstorablePairSlots(base, edited)
			if restored != c.wantRestored {
				t.Fatalf("restored = %v, want %v", restored, c.wantRestored)
			}
			if !restored {
				return
			}
			for k, want := range c.wantVals {
				if got, _ := out.First(k); got != want {
					t.Errorf("restored %s = %q, want %q", k, got, want)
				}
			}
		})
	}
}

// TestCoercedValues checks that coercedValues names only a COMPILATION non-boolean stored as 0
// (false). A trkn/disk number's leading zero or sign is a numerically-lossless canonicalization,
// not a coercion worth warning, so no number slot appears here (a copy grades it Carried and diff
// treats it as no change). A dropped slot (0, overflow, non-numeric) is not here either.
func TestCoercedValues(t *testing.T) {
	// A non-boolean COMPILATION is the one coercion reported.
	ts := tag.NewTagSet()
	ts.Set(tag.Compilation, "maybe")
	if cvs := coercedValues(ts); len(cvs) != 1 || cvs[0].Key != tag.Compilation {
		t.Fatalf("coercedValues = %+v, want one COMPILATION coercion", cvs)
	}

	// No trkn/disk number form is reported as coerced: a leading zero, a sign, a slashed total,
	// and a canonical value all leave the number keys out of coercedValues.
	for _, c := range []struct {
		name string
		set  map[tag.Key]string
	}{
		{"leading zero", map[tag.Key]string{tag.TrackNumber: "03"}},
		{"signed", map[tag.Key]string{tag.DiscNumber: "+2"}},
		{"total leading zero", map[tag.Key]string{tag.TrackTotal: "09"}},
		{"embedded total normalized", map[tag.Key]string{tag.TrackNumber: "3/09"}},
		{"canonical", map[tag.Key]string{tag.TrackNumber: "3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			nts := tag.NewTagSet()
			for k, v := range c.set {
				nts.Set(k, v)
			}
			for _, cv := range coercedValues(nts) {
				if cv.Key == tag.TrackNumber || cv.Key == tag.TrackTotal ||
					cv.Key == tag.DiscNumber || cv.Key == tag.DiscTotal {
					t.Errorf("number slot wrongly reported coerced: %+v", cv)
				}
			}
		})
	}
}

// TestNumberSlotTransferGrading checks that a copy grades a canonical or normalized trkn/disk
// number (a leading zero or sign, which the uint16 atom stores as the same integer) Carried, and
// an unrepresentable one (overflow, or the 0-reads-back-absent case) Dropped. A leading zero or
// sign is a numerically-lossless canonicalization, so copy grades it Carried, matching diff, which
// treats a sign/leading-zero-only delta as no change.
func TestNumberSlotTransferGrading(t *testing.T) {
	caps := Codec{}.Capabilities(nil, core.WriteOptions{})
	cases := []struct {
		name string
		key  tag.Key
		val  string
		want core.Disposition
	}{
		{"number leading zero is carried", tag.TrackNumber, "03", core.Carried},
		{"number signed is carried", tag.TrackNumber, "+3", core.Carried},
		{"number canonical is carried", tag.TrackNumber, "3", core.Carried},
		{"number ceiling is carried", tag.TrackNumber, "65535", core.Carried},
		{"number overflow is dropped", tag.TrackNumber, "70000", core.Dropped},
		{"number zero is dropped", tag.TrackNumber, "0", core.Dropped},
		{"number double-zero is dropped", tag.TrackNumber, "00", core.Dropped},
		{"disc leading zero is carried", tag.DiscNumber, "03", core.Carried},
		{"total leading zero is carried", tag.TrackTotal, "03", core.Carried},
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
// copy exactly when it round-trips through the real encode (pairItem) and decode (decodePair) to a
// numerically-equal value, which is exactly when the numeric-aware diff would report no change. The
// diff verdict uses numeric equality (tag.NumericValuesEqual): a leading zero or sign stored as the
// same integer is no change, so copy grades it Carried too.
func TestNumberDispositionAgreesWithRoundTrip(t *testing.T) {
	caps := Codec{}.Capabilities(nil, core.WriteOptions{})
	forms := []string{"3", "12", "65535", "03", "003", "+3", "0", "00", "+0", "70000", "-3", "abc"}
	for _, form := range forms {
		t.Run(form, func(t *testing.T) {
			ts := tag.NewTagSet()
			ts.Set(tag.TrackNumber, form)
			disp := dispositionOf(core.ProjectTransfer(&core.Media{Tags: ts}, caps), tag.TrackNumber)
			back, present := roundTripTrackNumber(form)
			diffEqual := present && tag.NumericValuesEqual(tag.TrackNumber, []string{back}, []string{form})
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
