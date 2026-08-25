package mp4

import (
	"bytes"
	"testing"

	"github.com/colespringer/waxlabel/internal/core"
	"github.com/colespringer/waxlabel/tag"
)

// TestITunesDroppedValues checks that droppedValues names exactly the iTunes structured
// values the atom encoders cannot store: an advisory or movement outside its atom's width or
// non-numeric, and a BPM the validator rejects (non-decimal, signed, or past 65535). A valid
// value, including a literal advisory 0 and a fractional BPM (a coercion, not a drop), is
// kept.
func TestITunesDroppedValues(t *testing.T) {
	cases := []struct {
		name string
		set  map[tag.Key]string
		want []tag.Key
	}{
		{"advisory non-numeric", map[tag.Key]string{tag.ITunesAdvisory: "abc"}, []tag.Key{tag.ITunesAdvisory}},
		{"advisory past the byte", map[tag.Key]string{tag.ITunesAdvisory: "256"}, []tag.Key{tag.ITunesAdvisory}},
		{"advisory explicit kept", map[tag.Key]string{tag.ITunesAdvisory: "2"}, nil},
		{"advisory zero kept", map[tag.Key]string{tag.ITunesAdvisory: "0"}, nil},
		{"movement past uint16", map[tag.Key]string{tag.Movement: "70000"}, []tag.Key{tag.Movement}},
		{"movement total past uint16", map[tag.Key]string{tag.MovementTotal: "70000"}, []tag.Key{tag.MovementTotal}},
		{"movement kept", map[tag.Key]string{tag.Movement: "3", tag.MovementTotal: "12"}, nil},
		{"bpm non-numeric", map[tag.Key]string{tag.BPM: "abc"}, []tag.Key{tag.BPM}},
		{"bpm negative", map[tag.Key]string{tag.BPM: "-1"}, []tag.Key{tag.BPM}},
		{"bpm past uint16", map[tag.Key]string{tag.BPM: "70000"}, []tag.Key{tag.BPM}},
		{"bpm fraction past the ceiling", map[tag.Key]string{tag.BPM: "65535.4"}, []tag.Key{tag.BPM}},
		{"bpm scientific form", map[tag.Key]string{tag.BPM: "1e3"}, []tag.Key{tag.BPM}},
		{"bpm integer kept", map[tag.Key]string{tag.BPM: "128"}, nil},
		{"bpm fraction kept (coerced, not dropped)", map[tag.Key]string{tag.BPM: "174.99"}, nil},
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

// TestITunesCoercedValues checks the iTunes structured coercions and their stored forms: a
// non-boolean pgap/shwm value stores 0, and a fractional BPM stores its nearest whole number.
// A numerically-lossless value ("174.0", "128") is not a coercion.
func TestITunesCoercedValues(t *testing.T) {
	for _, k := range []tag.Key{tag.ITunesGapless, tag.ShowMovement} {
		ts := tag.NewTagSet()
		ts.Set(k, "maybe")
		cvs := coercedValues(ts)
		if len(cvs) != 1 || cvs[0].Key != k || cvs[0].Stored != "0" {
			t.Errorf("coercedValues(%s=maybe) = %+v, want one coercion stored as 0", k, cvs)
		}
	}
	ts := tag.NewTagSet()
	ts.Set(tag.BPM, "174.99")
	cvs := coercedValues(ts)
	if len(cvs) != 1 || cvs[0].Key != tag.BPM || cvs[0].Stored != "175" {
		t.Errorf("coercedValues(BPM=174.99) = %+v, want one coercion stored as 175", cvs)
	}
	for _, v := range []string{"128", "174.0"} {
		nts := tag.NewTagSet()
		nts.Set(tag.BPM, v)
		if cvs := coercedValues(nts); len(cvs) != 0 {
			t.Errorf("coercedValues(BPM=%s) = %+v, want none (numerically lossless)", v, cvs)
		}
	}
}

// TestIntItemBytes pins the atom bytes intItem renders: a type-21 data atom holding the value
// as width big-endian bytes, with overflow and empty values rejected rather than widened or
// fabricated.
func TestIntItemBytes(t *testing.T) {
	cases := []struct {
		name     string
		atom     string
		width    int
		val      string
		wantOK   bool
		wantData []byte
	}{
		{"rtng one byte", "rtng", 1, "1", true, []byte{0x01}},
		{"movement two bytes", "\xa9mvi", 2, "3", true, []byte{0x00, 0x03}},
		{"stik one byte", "stik", 1, "2", true, []byte{0x02}},
		{"one-byte overflow rejected", "rtng", 1, "256", false, nil},
		{"two-byte overflow rejected", "\xa9mvi", 2, "65536", false, nil},
		{"non-numeric rejected", "rtng", 1, "abc", false, nil},
		{"empty rejected", "rtng", 1, "", false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, ok := intItem(c.atom, c.width, []string{c.val})
			if ok != c.wantOK {
				t.Fatalf("intItem(%q, %d, %q) ok = %v, want %v", c.atom, c.width, c.val, ok, c.wantOK)
			}
			if !ok {
				return
			}
			if it.id() != c.atom {
				t.Errorf("atom = %q, want %q", it.id(), c.atom)
			}
			checkSignedIntData(t, it.payload, c.wantData)
		})
	}
	if _, ok := intItem("rtng", 1, nil); ok {
		t.Error("intItem(nil) stored an atom, want dropped")
	}
}

// TestTmpoItemBytes pins the tmpo bytes: two big-endian bytes holding the value rounded to the
// nearest whole number, with everything ValidBPMValue rejects producing no atom (so the drop
// report and the encoder agree).
func TestTmpoItemBytes(t *testing.T) {
	cases := []struct {
		val      string
		wantOK   bool
		wantData []byte
	}{
		{"128", true, []byte{0x00, 0x80}},
		{"174.99", true, []byte{0x00, 0xAF}},
		{"174.0", true, []byte{0x00, 0xAE}},
		{"65535.4", false, nil},
		{"1e3", false, nil},
		{"abc", false, nil},
		{"", false, nil},
	}
	for _, c := range cases {
		it, ok := tmpoItem([]string{c.val})
		if ok != c.wantOK {
			t.Fatalf("tmpoItem(%q) ok = %v, want %v", c.val, ok, c.wantOK)
		}
		if !ok {
			continue
		}
		if it.id() != "tmpo" {
			t.Errorf("atom = %q, want tmpo", it.id())
		}
		checkSignedIntData(t, it.payload, c.wantData)
	}
}

// TestDecodeIntBoolTypeGuard pins that the integer and boolean decoders own only an
// integer-bearing data type (signed-int 21 or implicit 0). A text-typed atom would have its
// ASCII bytes misread as a big-endian number (type-1 "50" is 0x3530 = 13616) or, for a
// boolean, ASCII "0" (0x30, non-zero) read as true - and the bogus value would then be
// rewritten over the original on the next edit. Such an atom stays preserved-not-owned.
func TestDecodeIntBoolTypeGuard(t *testing.T) {
	cases := []struct {
		name      string
		it        item
		wantOwned bool
	}{
		{"tmpo signed int owned", item{name: atomName("tmpo"), payload: renderData(typeSignedInt, []byte{0, 128})}, true},
		{"tmpo implicit owned", item{name: atomName("tmpo"), payload: renderData(typeImplicit, []byte{0, 128})}, true},
		{"tmpo text preserved", item{name: atomName("tmpo"), payload: renderData(typeUTF8, []byte("50"))}, false},
		{"rtng text preserved", item{name: atomName("rtng"), payload: renderData(typeUTF8, []byte("1"))}, false},
		{"pgap text preserved", item{name: atomName("pgap"), payload: renderData(typeUTF8, []byte("0"))}, false},
		{"pgap signed int owned", item{name: atomName("pgap"), payload: renderData(typeSignedInt, []byte{1})}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeItem(c.it).owned; got != c.wantOwned {
				t.Errorf("owned = %v, want %v", got, c.wantOwned)
			}
		})
	}
}

// TestITunesRestoreUnstorableSlots pins that an edit making a fixed-width integer or BPM
// slot unstorable restores the base value instead of deleting stored data, while a boolean
// coercion (written as 0, not dropped) is left alone.
func TestITunesRestoreUnstorableSlots(t *testing.T) {
	cases := []struct {
		name         string
		key          tag.Key
		baseVal, ed  string
		wantRestored bool
	}{
		{"advisory overflow restores", tag.ITunesAdvisory, "1", "256", true},
		{"advisory non-numeric restores", tag.ITunesAdvisory, "1", "abc", true},
		{"mediatype overflow restores", tag.MediaType, "2", "999", true},
		{"movement overflow restores", tag.Movement, "3", "70000", true},
		{"movement total restores", tag.MovementTotal, "12", "abc", true},
		{"bpm non-numeric restores", tag.BPM, "128", "abc", true},
		{"bpm valid edit not restored", tag.BPM, "128", "140", false},
		{"boolean coercion not restored", tag.ITunesGapless, "1", "maybe", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, edited := tag.NewTagSet(), tag.NewTagSet()
			base.Set(c.key, c.baseVal)
			edited.Set(c.key, c.ed)
			out, restored := restoreUnstorableSlots(base, edited)
			if restored != c.wantRestored {
				t.Fatalf("restored = %v, want %v", restored, c.wantRestored)
			}
			want := c.ed
			if c.wantRestored {
				want = c.baseVal
			}
			if got, _ := out.First(c.key); got != want {
				t.Errorf("%s = %q, want %q", c.key, got, want)
			}
		})
	}
}

// TestITunesTransferGrading pins the per-key transfer grades through the same capability
// layer the caps and copy commands consume: an unstorable value grades Dropped, a storable
// one Carried, a fractional BPM Lossy (tmpo rounds it), and a structured single-atom key
// holding several values Lossy (only the first is stored).
func TestITunesTransferGrading(t *testing.T) {
	caps := Codec{}.Capabilities(nil, core.WriteOptions{})
	cases := []struct {
		name string
		key  tag.Key
		vals []string
		want core.Disposition
	}{
		{"advisory valid", tag.ITunesAdvisory, []string{"1"}, core.Carried},
		{"advisory overflow", tag.ITunesAdvisory, []string{"256"}, core.Dropped},
		{"movement valid", tag.Movement, []string{"3"}, core.Carried},
		{"movement overflow", tag.Movement, []string{"70000"}, core.Dropped},
		{"movement total overflow", tag.MovementTotal, []string{"70000"}, core.Dropped},
		{"gapless valid", tag.ITunesGapless, []string{"yes"}, core.Carried},
		{"gapless non-boolean", tag.ITunesGapless, []string{"maybe"}, core.Dropped},
		{"showmovement non-boolean", tag.ShowMovement, []string{"maybe"}, core.Dropped},
		{"bpm integer", tag.BPM, []string{"128"}, core.Carried},
		{"bpm lossless respell", tag.BPM, []string{"174.0"}, core.Carried},
		{"bpm fraction is lossy", tag.BPM, []string{"174.99"}, core.Lossy},
		{"bpm invalid", tag.BPM, []string{"abc"}, core.Dropped},
		{"bpm multi-value is lossy", tag.BPM, []string{"12", "34"}, core.Lossy},
		{"mediatype multi-value is lossy", tag.MediaType, []string{"1", "2"}, core.Lossy},
		{"work multi-value carries", tag.Work, []string{"A", "B"}, core.Carried},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := tag.NewTagSet()
			ts.Set(c.key, c.vals...)
			got := dispositionOf(core.ProjectTransfer(&core.Media{Tags: ts}, caps), c.key)
			if got != c.want {
				t.Errorf("disposition of %s=%v = %v, want %v", c.key, c.vals, got, c.want)
			}
		})
	}
}

// TestStructuredSingleAtomKeySet pins the shared enumeration: every structured key is
// excluded from the multi-atom note and has its surplus values reported, while a ©-text key
// (WORK) stays a genuine multi-atom field.
func TestStructuredSingleAtomKeySet(t *testing.T) {
	for key := range structuredSingleAtomKeys {
		ts := tag.NewTagSet()
		ts.Set(key, "1", "2")
		if keys := multiValueDataKeys(ts); len(keys) != 0 {
			t.Errorf("multiValueDataKeys(%s x2) = %v, want none (single-atom slot)", key, keys)
		}
		ev := extraStructuredValues(ts)
		if len(ev) != 1 || ev[0].Key != key || ev[0].Value != "2" {
			t.Errorf("extraStructuredValues(%s x2) = %+v, want the surplus value named", key, ev)
		}
	}
	ts := tag.NewTagSet()
	ts.Set(tag.Work, "A", "B")
	if keys := multiValueDataKeys(ts); len(keys) != 1 || keys[0] != tag.Work {
		t.Errorf("multiValueDataKeys(WORK x2) = %v, want [WORK]", keys)
	}
	if ev := extraStructuredValues(ts); len(ev) != 0 {
		t.Errorf("extraStructuredValues(WORK x2) = %+v, want none (multi-atom text)", ev)
	}
}

// checkSignedIntData asserts an item payload holds exactly one type-21 (signed integer) data
// atom with the given value bytes.
func checkSignedIntData(t *testing.T, payload, want []byte) {
	t.Helper()
	atoms, ok := parseDataAtoms(payload)
	if !ok || len(atoms) != 1 {
		t.Fatalf("payload did not parse as one data atom (ok=%v, n=%d)", ok, len(atoms))
	}
	if atoms[0].typ != typeSignedInt {
		t.Errorf("data type = %d, want %d (signed integer)", atoms[0].typ, typeSignedInt)
	}
	if !bytes.Equal(atoms[0].value, want) {
		t.Errorf("value bytes = % X, want % X", atoms[0].value, want)
	}
}
