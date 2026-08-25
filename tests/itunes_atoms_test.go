package waxlabel_test

import (
	"bytes"
	"encoding/binary"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// mp4AllITunesAtoms builds a file carrying every iTunes structured atom this change
// projects, plus the three classic text atoms (©wrk, ©mvn, ©enc). pgap and shwm carry
// DISTINCT values (1 and 0), so a decoder that swapped the two targets fails the
// projection checks instead of passing on identical bytes.
func mp4AllITunesAtoms() []byte {
	return mp4Tagged(
		mp4Text("\xa9nam", "T"),
		mp4Atom("rtng", mp4Data(21, []byte{1})),
		mp4Atom("pgap", mp4Data(21, []byte{1})),
		mp4Atom("shwm", mp4Data(21, []byte{0})),
		mp4Atom("tmpo", mp4Data(21, []byte{0, 128})),
		mp4Atom("\xa9mvi", mp4Data(21, []byte{0, 3})),
		mp4Atom("\xa9mvc", mp4Data(21, []byte{0, 12})),
		mp4Text("\xa9wrk", "Symphony No. 5"),
		mp4Text("\xa9mvn", "Allegro con brio"),
		mp4Text("\xa9enc", "Jane Encoder"),
	)
}

// itunesAtomValues is the canonical projection mp4AllITunesAtoms must yield.
var itunesAtomValues = map[tag.Key]string{
	tag.ITunesAdvisory: "1",
	tag.ITunesGapless:  "1",
	tag.ShowMovement:   "0",
	tag.BPM:            "128",
	tag.Movement:       "3",
	tag.MovementTotal:  "12",
	tag.Work:           "Symphony No. 5",
	tag.MovementName:   "Allegro con brio",
	tag.EncodedBy:      "Jane Encoder",
}

// itunesDataAtom returns the type field and raw value of the first data atom inside the
// output's named item, mirroring gnreDataAtom.
func itunesDataAtom(t *testing.T, out []byte, name string) (typ uint32, value []byte) {
	t.Helper()
	j := bytes.Index(out, []byte(name))
	if j < 0 {
		t.Fatalf("no %q atom in output", name)
	}
	da := out[j+len(name):]
	if len(da) < 16 || string(da[4:8]) != "data" {
		t.Fatalf("%q payload is not a data atom: % x", name, da[:min(16, len(da))])
	}
	size := binary.BigEndian.Uint32(da[0:4])
	return binary.BigEndian.Uint32(da[8:12]) & 0x00FFFFFF, da[16:size]
}

// TestMP4ITunesAtomsProject reads the structured atoms into the canonical keys and the
// typed Fields projection. These atoms were preserved-but-invisible before this change.
func TestMP4ITunesAtomsProject(t *testing.T) {
	doc := mustParseBytes(t, mp4AllITunesAtoms())
	for key, want := range itunesAtomValues {
		if v, ok := doc.Get(key); !ok || len(v) != 1 || v[0] != want {
			t.Errorf("%s = %v (ok=%v), want %q", key, v, ok, want)
		}
	}
	f := doc.Fields()
	if f.ITunesAdvisory != "1" || f.BPM != "128" || f.Movement != "3" || f.MovementTotal != "12" {
		t.Errorf("numeric fields = %q/%q/%q/%q, want 1/128/3/12", f.ITunesAdvisory, f.BPM, f.Movement, f.MovementTotal)
	}
	if !f.ITunesGapless || f.ShowMovement {
		t.Errorf("boolean fields = %v/%v, want true/false (the fixture stores pgap=1, shwm=0)", f.ITunesGapless, f.ShowMovement)
	}
	if f.Work != "Symphony No. 5" || f.MovementName != "Allegro con brio" || f.EncodedBy != "Jane Encoder" {
		t.Errorf("text fields = %q/%q/%q", f.Work, f.MovementName, f.EncodedBy)
	}
}

// TestMP4ITunesAtomDecodeFidelity pins the lenient decode edges: a literal advisory 0 and
// the legacy 4 read faithfully, an implicit-type (0) data atom is accepted, and tmpo
// decodes at 1- and 4-byte widths.
func TestMP4ITunesAtomDecodeFidelity(t *testing.T) {
	cases := []struct {
		name string
		item []byte
		key  tag.Key
		want string
	}{
		{"advisory zero", mp4Atom("rtng", mp4Data(21, []byte{0})), tag.ITunesAdvisory, "0"},
		{"advisory legacy four", mp4Atom("rtng", mp4Data(21, []byte{4})), tag.ITunesAdvisory, "4"},
		{"implicit type accepted", mp4Atom("rtng", mp4Data(0, []byte{2})), tag.ITunesAdvisory, "2"},
		{"tmpo one byte", mp4Atom("tmpo", mp4Data(21, []byte{128})), tag.BPM, "128"},
		{"tmpo four bytes", mp4Atom("tmpo", mp4Data(21, []byte{0, 0, 0, 200})), tag.BPM, "200"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParseBytes(t, mp4Tagged(mp4Text("\xa9nam", "T"), c.item))
			if v, ok := doc.Get(c.key); !ok || len(v) != 1 || v[0] != c.want {
				t.Errorf("%s = %v (ok=%v), want %q", c.key, v, ok, c.want)
			}
		})
	}
}

// TestMP4ITunesWriteCreatesAtoms sets the eight keys on a bare file and pins the atoms and
// their bytes: structured atoms with type-21 data, no "----" freeform fallback, and a clean
// round-trip including a literal advisory 0.
func TestMP4ITunesWriteCreatesAtoms(t *testing.T) {
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	// The two flags get distinct values so a swapped pgap/shwm encoder fails the byte pins.
	plan, err := mustParseBytes(t, data).Edit().
		Set(tag.ITunesAdvisory, "1").
		Set(tag.ITunesGapless, "1").
		Set(tag.ShowMovement, "0").
		Set(tag.BPM, "128").
		Set(tag.Movement, "3").
		Set(tag.MovementTotal, "12").
		Set(tag.Work, "W").
		Set(tag.MovementName, "M").
		Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	if bytes.Contains(out, []byte("----")) {
		t.Error("a structured key leaked into a ---- freeform atom")
	}
	for name, want := range map[string][]byte{
		"rtng":    {1},
		"pgap":    {1},
		"shwm":    {0},
		"tmpo":    {0, 128},
		"\xa9mvi": {0, 3},
		"\xa9mvc": {0, 12},
	} {
		typ, val := itunesDataAtom(t, out, name)
		if typ != 21 {
			t.Errorf("%q data type = %d, want 21 (signed integer)", name, typ)
		}
		if !bytes.Equal(val, want) {
			t.Errorf("%q value = % X, want % X", name, val, want)
		}
	}
	re := mustParseBytes(t, out)
	for key, want := range map[tag.Key]string{
		tag.ITunesAdvisory: "1", tag.ITunesGapless: "1", tag.ShowMovement: "0",
		tag.BPM: "128", tag.Movement: "3", tag.MovementTotal: "12",
		tag.Work: "W", tag.MovementName: "M",
	} {
		if v, ok := re.Get(key); !ok || len(v) != 1 || v[0] != want {
			t.Errorf("%s round-trip = %v (ok=%v), want %q", key, v, ok, want)
		}
	}

	// A literal advisory 0 stores and reads back as "0", not absent.
	plan0, err := mustParseBytes(t, data).Edit().Set(tag.ITunesAdvisory, "0").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := mustParseBytes(t, applyToBytes(t, data, plan0)).Get(tag.ITunesAdvisory); !ok || len(v) != 1 || v[0] != "0" {
		t.Errorf("ITUNESADVISORY=0 round-trip = %v (ok=%v), want [0]", v, ok)
	}
}

// TestMP4ITunesAtomsSurviveUnrelatedEdit is the ownership guard: every decode case must have
// its encode case, or a Title-only edit would silently delete the atom it now owns.
func TestMP4ITunesAtomsSurviveUnrelatedEdit(t *testing.T) {
	data := mp4AllITunesAtoms()
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "Renamed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)

	re := mustParseBytes(t, out)
	for key, want := range itunesAtomValues {
		if v, ok := re.Get(key); !ok || len(v) != 1 || v[0] != want {
			t.Errorf("%s after unrelated edit = %v (ok=%v), want %q", key, v, ok, want)
		}
	}
	for _, name := range []string{"rtng", "pgap", "shwm", "tmpo", "\xa9mvi", "\xa9mvc", "\xa9wrk", "\xa9mvn", "\xa9enc"} {
		if !bytes.Contains(out, []byte(name)) {
			t.Errorf("atom %q missing after an unrelated edit", name)
		}
	}
}

// TestMP4ITunesDropAndCoerceWarnings pins the write-time warnings: an unstorable integer or
// BPM drops with WarnValueDropped, a non-boolean flag coerces to 0, and a fractional BPM
// coerces with the rounded value named in the message.
func TestMP4ITunesDropAndCoerceWarnings(t *testing.T) {
	data := mp4Tagged(mp4Text("\xa9nam", "T"))
	planWarning := func(key tag.Key, val string, code wl.WarningCode) (string, bool) {
		t.Helper()
		p, err := mustParseBytes(t, data).Edit().Set(key, val).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range p.Report().Warnings {
			if w.Code == code {
				return w.Message, true
			}
		}
		return "", false
	}

	for _, c := range []struct {
		key tag.Key
		val string
	}{
		{tag.ITunesAdvisory, "256"},
		{tag.Movement, "70000"},
		{tag.BPM, "abc"},
	} {
		if _, ok := planWarning(c.key, c.val, wl.WarnValueDropped); !ok {
			t.Errorf("%s=%s must warn value-dropped", c.key, c.val)
		}
	}

	msg, ok := planWarning(tag.ITunesGapless, "maybe", wl.WarnValueCoerced)
	if !ok || !strings.Contains(msg, "stored as 0") {
		t.Errorf("ITUNESGAPLESS=maybe coercion = %q (ok=%v), want a stored-as-0 warning", msg, ok)
	}
	msg, ok = planWarning(tag.BPM, "174.99", wl.WarnValueCoerced)
	if !ok || !strings.Contains(msg, "rounded to 175") {
		t.Errorf("BPM=174.99 coercion = %q (ok=%v), want the rounded value 175 named", msg, ok)
	}
	// The stored form is the rounded integer.
	p, err := mustParseBytes(t, data).Edit().Set(tag.BPM, "174.99").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, data, p)).Get(tag.BPM); len(v) != 1 || v[0] != "175" {
		t.Errorf("BPM=174.99 stored = %v, want [175]", v)
	}
	// A whole-number spelling is numerically lossless: no coercion warning.
	if msg, ok := planWarning(tag.BPM, "174.0", wl.WarnValueCoerced); ok {
		t.Errorf("BPM=174.0 wrongly warned coerced: %q", msg)
	}
}

// TestMP4ITunesAdvisoryDualRepresentation covers a file holding both the structured rtng and
// a freeform ITUNESADVISORY: both project, distinct values leave the family unselected and
// lint flags the single-valued key, identical values stay selected.
func TestMP4ITunesAdvisoryDualRepresentation(t *testing.T) {
	build := func(freeformVal string) []byte {
		return mp4Tagged(
			mp4Text("\xa9nam", "T"),
			mp4Atom("rtng", mp4Data(21, []byte{1})),
			mp4Freeform("com.apple.iTunes", "ITUNESADVISORY", freeformVal),
		)
	}
	doc := mustParseBytes(t, build("2"))
	if got, _ := doc.Tags().Get(tag.ITunesAdvisory); len(got) != 2 {
		t.Fatalf("ITUNESADVISORY = %v, want both representations to project", got)
	}
	if !hasLintCode(doc, "single-valued-multi") {
		t.Errorf("expected a single-valued-multi finding; got %v", doc.Lint())
	}
	if selected, ok := familySelected(doc, tag.ITunesAdvisory); !ok || selected {
		t.Errorf("distinct values: family Selected = %v (found=%v), want false", selected, ok)
	}
	same := mustParseBytes(t, build("1"))
	if selected, ok := familySelected(same, tag.ITunesAdvisory); !ok || !selected {
		t.Errorf("identical values: family Selected = %v (found=%v), want true", selected, ok)
	}
}

// TestMP4ITunesAdvisoryFreeformMigration covers a freeform-only ITUNESADVISORY file: it
// reads, and any edit (buildItems rebuilds every owned item) migrates it to the structured
// rtng atom with the value intact.
func TestMP4ITunesAdvisoryFreeformMigration(t *testing.T) {
	data := mp4Tagged(mp4Text("\xa9nam", "T"), mp4Freeform("com.apple.iTunes", "ITUNESADVISORY", "2"))
	doc := mustParseBytes(t, data)
	if v, _ := doc.Get(tag.ITunesAdvisory); len(v) != 1 || v[0] != "2" {
		t.Fatalf("freeform ITUNESADVISORY = %v, want [2]", v)
	}

	plan, err := doc.Edit().Set(tag.Title, "Renamed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("rtng")) {
		t.Error("the freeform advisory did not migrate to a structured rtng atom")
	}
	if bytes.Contains(out, []byte("ITUNESADVISORY")) {
		t.Error("the freeform ITUNESADVISORY atom survived the rebuild")
	}
	if v, _ := mustParseBytes(t, out).Get(tag.ITunesAdvisory); len(v) != 1 || v[0] != "2" {
		t.Errorf("advisory after migration = %v, want [2]", v)
	}
}

// TestMP4EncodedBySpellingMigration covers the ©enc mapping: an old freeform ----:ENCODEDBY
// still reads (valid-key fallback), any edit migrates it to ©enc, and a ©enc file
// round-trips.
func TestMP4EncodedBySpellingMigration(t *testing.T) {
	data := mp4Tagged(mp4Text("\xa9nam", "T"), mp4Freeform("com.apple.iTunes", "ENCODEDBY", "Jane"))
	doc := mustParseBytes(t, data)
	if v, _ := doc.Get(tag.EncodedBy); len(v) != 1 || v[0] != "Jane" {
		t.Fatalf("freeform ENCODEDBY = %v, want [Jane]", v)
	}

	plan, err := doc.Edit().Set(tag.Title, "Renamed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, []byte("\xa9enc")) {
		t.Error("ENCODEDBY did not migrate to the \xa9enc atom")
	}
	if bytes.Contains(out, []byte("ENCODEDBY")) {
		t.Error("the freeform ENCODEDBY atom survived the rebuild")
	}

	enc := mp4Tagged(mp4Text("\xa9enc", "Jane"))
	plan2, err := mustParseBytes(t, enc).Edit().Set(tag.Title, "T2").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, enc, plan2)).Get(tag.EncodedBy); len(v) != 1 || v[0] != "Jane" {
		t.Errorf("\xa9enc round-trip = %v, want [Jane]", v)
	}
}

// TestID3BPMDualRepresentation covers an MP3 carrying both TBPM and TXXX:BPM. Both project
// (lint flags the single-valued key), and a BPM edit drops the stale TXXX and leaves exactly
// one TBPM. This is the first id3TextFrames addition since the stale-representation drop
// landed; nothing else locks that interaction.
func TestID3BPMDualRepresentation(t *testing.T) {
	data := append(id3v2(3, textFrame(3, "TBPM", "128"), txxxFrame(3, "BPM", "140")), mp3Audio(t)...)
	doc := mustParseBytes(t, data)
	if got, _ := doc.Tags().Get(tag.BPM); len(got) != 2 {
		t.Fatalf("BPM = %v, want both frames to project", got)
	}
	if !hasLintCode(doc, "single-valued-multi") {
		t.Errorf("expected a single-valued-multi finding; got %v", doc.Lint())
	}

	plan, err := doc.Edit().Set(tag.BPM, "150").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if n := bytes.Count(out, []byte("TBPM")); n != 1 {
		t.Errorf("output carries %d TBPM frames, want 1", n)
	}
	if bytes.Contains(out, []byte("TXXX")) {
		t.Error("the stale TXXX:BPM frame survived a BPM edit")
	}
	if v, _ := mustParseBytes(t, out).Get(tag.BPM); !slices.Equal(v, []string{"150"}) {
		t.Errorf("BPM after edit = %v, want [150]", v)
	}
}

// TestID3BPMLegacyTXXXMigration covers a TXXX:BPM-only file: a value-changing BPM edit drops
// the stale user frame and emits a fresh TBPM, while a Title-only edit preserves the TXXX
// frame verbatim (ID3 preserves non-dirty frames, unlike MP4's full rebuild).
func TestID3BPMLegacyTXXXMigration(t *testing.T) {
	data := append(id3v2(3, txxxFrame(3, "BPM", "140")), mp3Audio(t)...)
	doc := mustParseBytes(t, data)
	if v, _ := doc.Get(tag.BPM); !slices.Equal(v, []string{"140"}) {
		t.Fatalf("TXXX:BPM = %v, want [140]", v)
	}

	plan, err := doc.Edit().Set(tag.BPM, "150").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("TXXX")) {
		t.Error("the stale TXXX:BPM frame survived a value-changing BPM edit")
	}
	if n := bytes.Count(out, []byte("TBPM")); n != 1 {
		t.Errorf("output carries %d TBPM frames, want 1", n)
	}
	if v, _ := mustParseBytes(t, out).Get(tag.BPM); !slices.Equal(v, []string{"150"}) {
		t.Errorf("BPM after migration = %v, want [150]", v)
	}

	// An unrelated edit leaves the non-dirty TXXX:BPM frame untouched.
	unrelated, err := mustParseBytes(t, data).Edit().Set(tag.Title, "T").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out2 := applyToBytes(t, data, unrelated)
	if !bytes.Contains(out2, []byte("TXXX")) {
		t.Error("an unrelated edit must preserve the TXXX:BPM frame verbatim")
	}
	if v, _ := mustParseBytes(t, out2).Get(tag.BPM); !slices.Equal(v, []string{"140"}) {
		t.Errorf("BPM after unrelated edit = %v, want [140]", v)
	}
}

// TestID3MovementPair covers the MVIN frame: an "n/total" pair splits on read, composes on
// write, handles the total-only edge like TRCK, preserves a malformed number verbatim with
// the total-dropped warning, and keeps the accepted MOVEMENT="3/12" edge round-tripping.
func TestID3MovementPair(t *testing.T) {
	// Read: an MVIN pair splits into the two canonical keys, MVNM reads beside it.
	data := append(id3v2(3, textFrame(3, "MVIN", "3/12"), textFrame(3, "MVNM", "Allegro")), mp3Audio(t)...)
	doc := mustParseBytes(t, data)
	if v, _ := doc.Get(tag.Movement); !slices.Equal(v, []string{"3"}) {
		t.Errorf("MOVEMENT = %v, want [3]", v)
	}
	if v, _ := doc.Get(tag.MovementTotal); !slices.Equal(v, []string{"12"}) {
		t.Errorf("MOVEMENTTOTAL = %v, want [12]", v)
	}
	if v, _ := doc.Get(tag.MovementName); !slices.Equal(v, []string{"Allegro"}) {
		t.Errorf("MOVEMENTNAME = %v, want [Allegro]", v)
	}

	// Write: setting both keys on a bare MP3 emits one MVIN "3/12".
	bare := readFixture(t, notagsMP3)
	plan, err := mustParseBytes(t, bare).Edit().Set(tag.Movement, "3").Set(tag.MovementTotal, "12").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, bare, plan)
	if n := bytes.Count(out, []byte("MVIN")); n != 1 {
		t.Errorf("output carries %d MVIN frames, want 1", n)
	}
	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.Movement); !slices.Equal(v, []string{"3"}) {
		t.Errorf("MOVEMENT round-trip = %v, want [3]", v)
	}
	if v, _ := re.Get(tag.MovementTotal); !slices.Equal(v, []string{"12"}) {
		t.Errorf("MOVEMENTTOTAL round-trip = %v, want [12]", v)
	}

	// Total-only edge: a lone MOVEMENTTOTAL round-trips as "/12", like TRCK.
	planTot, err := mustParseBytes(t, bare).Edit().Set(tag.MovementTotal, "12").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	reTot := mustParseBytes(t, applyToBytes(t, bare, planTot))
	if v, _ := reTot.Get(tag.MovementTotal); !slices.Equal(v, []string{"12"}) {
		t.Errorf("total-only MOVEMENTTOTAL round-trip = %v, want [12]", v)
	}
	if v, ok := reTot.Get(tag.Movement); ok {
		t.Errorf("total-only write fabricated a MOVEMENT = %v", v)
	}

	// A malformed number preserves verbatim and warns the canonical total dropped.
	planBad, err := mustParseBytes(t, bare).Edit().Set(tag.Movement, "abc").Set(tag.MovementTotal, "12").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	droppedTotal := false
	for _, w := range planBad.Report().Warnings {
		if w.Code == wl.WarnValueDropped && slices.Contains(w.Keys, tag.MovementTotal) {
			droppedTotal = true
		}
	}
	if !droppedTotal {
		t.Errorf("MOVEMENT=abc + MOVEMENTTOTAL=12 must warn the total dropped; got %v", planBad.Report().Warnings)
	}
	reBad := mustParseBytes(t, applyToBytes(t, bare, planBad))
	if v, _ := reBad.Get(tag.Movement); !slices.Equal(v, []string{"abc"}) {
		t.Errorf("malformed MOVEMENT = %v, want [abc] preserved verbatim", v)
	}

	// Accepted edge: MOVEMENT="3/12" (lints malformed at the tag level) writes the MVIN body
	// verbatim, so it reads back as the split pair.
	planPair, err := mustParseBytes(t, bare).Edit().Set(tag.Movement, "3/12").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	rePair := mustParseBytes(t, applyToBytes(t, bare, planPair))
	if v, _ := rePair.Get(tag.Movement); !slices.Equal(v, []string{"3"}) {
		t.Errorf("MOVEMENT=3/12 read back = %v, want [3] (the MVIN pair splits)", v)
	}
	if v, _ := rePair.Get(tag.MovementTotal); !slices.Equal(v, []string{"12"}) {
		t.Errorf("MOVEMENTTOTAL from the pair = %v, want [12]", v)
	}
}

// TestITunesKeysCrossFormat sets the eight keys on each main format, re-parses, and
// spot-checks the native representations; then copies M4A -> MP3 -> M4A and checks nothing
// is lost (an integer BPM, so the tmpo leg is lossless).
func TestITunesKeysCrossFormat(t *testing.T) {
	set := map[tag.Key]string{
		tag.ITunesAdvisory: "1",
		tag.ITunesGapless:  "1",
		tag.ShowMovement:   "1",
		tag.BPM:            "128",
		tag.Work:           "Symphony No. 5",
		tag.MovementName:   "Allegro",
		tag.Movement:       "3",
		tag.MovementTotal:  "12",
	}
	write := func(src []byte) []byte {
		t.Helper()
		ed := mustParseBytes(t, src).Edit()
		for k, v := range set {
			ed = ed.Set(k, v)
		}
		plan, err := ed.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		return applyToBytes(t, src, plan)
	}
	check := func(label string, d *wl.Document) {
		t.Helper()
		for k, want := range set {
			if v, ok := d.Get(k); !ok || len(v) != 1 || v[0] != want {
				t.Errorf("%s: %s = %v (ok=%v), want %q", label, k, v, ok, want)
			}
		}
	}

	for _, f := range []string{sampleFLAC, sampleMP3, sampleMP4, sampleMKA} {
		out := write(readFixture(t, f))
		check(f, mustParseBytes(t, out))
		switch f {
		case sampleMP3:
			for _, frame := range []string{"TBPM", "MVNM", "MVIN"} {
				if !bytes.Contains(out, []byte(frame)) {
					t.Errorf("%s: expected a %s frame in the output", f, frame)
				}
			}
			for _, desc := range []string{"ITUNESADVISORY", "WORK"} {
				if !bytes.Contains(out, []byte(desc)) {
					t.Errorf("%s: expected a TXXX:%s user frame in the output", f, desc)
				}
			}
		case sampleMP4:
			for _, name := range []string{"rtng", "pgap", "shwm", "tmpo", "\xa9mvi", "\xa9mvc", "\xa9wrk", "\xa9mvn"} {
				if !bytes.Contains(out, []byte(name)) {
					t.Errorf("%s: expected a %q atom in the output", f, name)
				}
			}
		case sampleFLAC, sampleMKA:
			for _, name := range []string{"ITUNESADVISORY", "BPM", "WORK", "MOVEMENTNAME"} {
				if !bytes.Contains(out, []byte(name)) {
					t.Errorf("%s: expected the identity spelling %s in the output", f, name)
				}
			}
		}
	}

	// Copy M4A -> MP3 -> M4A: every key survives both legs.
	m4a := write(readFixture(t, sampleMP4))
	mp3Dst := mustParseBytes(t, readFixture(t, notagsMP3))
	planToMP3, _, err := mustParseBytes(t, m4a).PrepareTransfer(mp3Dst)
	if err != nil {
		t.Fatalf("PrepareTransfer to MP3: %v", err)
	}
	mp3Out := applyToBytes(t, readFixture(t, notagsMP3), planToMP3)
	check("M4A->MP3", mustParseBytes(t, mp3Out))

	m4aDst := mustParseBytes(t, readFixture(t, "../testdata/notags.m4a"))
	planBack, _, err := mustParseBytes(t, mp3Out).PrepareTransfer(m4aDst)
	if err != nil {
		t.Fatalf("PrepareTransfer back to M4A: %v", err)
	}
	check("MP3->M4A", mustParseBytes(t, applyToBytes(t, readFixture(t, "../testdata/notags.m4a"), planBack)))
}

// TestMP4TextTypedIntAtomPreserved covers a structured atom carrying a text-typed data atom
// (a nonconformant file another tool patched): its ASCII bytes must not be misread as a
// big-endian integer, so it projects nothing, and an unrelated edit preserves it verbatim
// instead of rewriting it as the bogus number.
func TestMP4TextTypedIntAtomPreserved(t *testing.T) {
	textTmpo := mp4Atom("tmpo", mp4Data(1, []byte("50")))
	data := mp4Tagged(mp4Text("\xa9nam", "T"), textTmpo)
	doc := mustParseBytes(t, data)
	if v, ok := doc.Get(tag.BPM); ok {
		t.Fatalf("a text-typed tmpo projected BPM = %v, want nothing (ASCII \"50\" is not 13616)", v)
	}

	plan, err := doc.Edit().Set(tag.Title, "Renamed").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if !bytes.Contains(out, textTmpo) {
		t.Error("the text-typed tmpo atom was not preserved byte-for-byte across an unrelated edit")
	}
	if v, ok := mustParseBytes(t, out).Get(tag.BPM); ok {
		t.Errorf("BPM after the edit = %v, want still nothing", v)
	}
}

// TestID3MovementFrameManaged pins MVIN's managed status: an MP3 already carrying an MVIN
// pair edited to a new MOVEMENT re-renders the one frame rather than emitting a second
// beside the stale original.
func TestID3MovementFrameManaged(t *testing.T) {
	data := append(id3v2(3, textFrame(3, "MVIN", "3/12")), mp3Audio(t)...)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Movement, "5").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if n := bytes.Count(out, []byte("MVIN")); n != 1 {
		t.Errorf("output carries %d MVIN frames, want 1 (the original must be re-rendered, not duplicated)", n)
	}
	re := mustParseBytes(t, out)
	if v, _ := re.Get(tag.Movement); !slices.Equal(v, []string{"5"}) {
		t.Errorf("MOVEMENT = %v, want [5]", v)
	}
	if v, _ := re.Get(tag.MovementTotal); !slices.Equal(v, []string{"12"}) {
		t.Errorf("MOVEMENTTOTAL = %v, want [12] (the unchanged total re-renders with the pair)", v)
	}
}

// TestID3MovementMalformedPairVerbatim pins movementSplit's validity gate: an MVIN body
// whose side is not a valid movement integer stays one verbatim MOVEMENT value instead of
// fabricating a total from garbage.
func TestID3MovementMalformedPairVerbatim(t *testing.T) {
	for _, body := range []string{"abc/1", "3/70000", "ab/cd/12"} {
		data := append(id3v2(3, textFrame(3, "MVIN", body)), mp3Audio(t)...)
		doc := mustParseBytes(t, data)
		if v, _ := doc.Get(tag.Movement); !slices.Equal(v, []string{body}) {
			t.Errorf("MVIN %q: MOVEMENT = %v, want the whole value verbatim", body, v)
		}
		if v, ok := doc.Get(tag.MovementTotal); ok {
			t.Errorf("MVIN %q: fabricated MOVEMENTTOTAL = %v, want none", body, v)
		}
	}
}
