package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// mp4AllITunesAtoms builds a file carrying every projected iTunes structured atom, plus the three
// classic text atoms (©wrk, ©mvn, ©enc).
func mp4AllITunesAtoms() []byte {
	return testfile.MP4Tagged(
		testfile.MP4Text("\xa9nam", "T"),
		testfile.MP4Atom("rtng", testfile.MP4Data(21, []byte{1})),
		testfile.MP4Atom("pgap", testfile.MP4Data(21, []byte{1})),
		testfile.MP4Atom("shwm", testfile.MP4Data(21, []byte{0})),
		testfile.MP4Atom("tmpo", testfile.MP4Data(21, []byte{0, 128})),
		testfile.MP4Atom("\xa9mvi", testfile.MP4Data(21, []byte{0, 3})),
		testfile.MP4Atom("\xa9mvc", testfile.MP4Data(21, []byte{0, 12})),
		testfile.MP4Text("\xa9wrk", "Symphony No. 5"),
		testfile.MP4Text("\xa9mvn", "Allegro con brio"),
		testfile.MP4Text("\xa9enc", "Jane Encoder"),
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

// The structured atoms project into the canonical keys and the typed Fields projection.
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

// lenient decode edges: a literal advisory 0 and the legacy 4 read as stored, an implicit-type (0)
// data atom is accepted, and tmpo decodes at 1- and 4-byte widths.
func TestMP4ITunesAtomDecodeFidelity(t *testing.T) {
	cases := []struct {
		name string
		item []byte
		key  tag.Key
		want string
	}{
		{"advisory zero", testfile.MP4Atom("rtng", testfile.MP4Data(21, []byte{0})), tag.ITunesAdvisory, "0"},
		{"advisory legacy four", testfile.MP4Atom("rtng", testfile.MP4Data(21, []byte{4})), tag.ITunesAdvisory, "4"},
		{"implicit type accepted", testfile.MP4Atom("rtng", testfile.MP4Data(0, []byte{2})), tag.ITunesAdvisory, "2"},
		{"tmpo one byte", testfile.MP4Atom("tmpo", testfile.MP4Data(21, []byte{128})), tag.BPM, "128"},
		{"tmpo four bytes", testfile.MP4Atom("tmpo", testfile.MP4Data(21, []byte{0, 0, 0, 200})), tag.BPM, "200"},
		{"trkn of a number alone", testfile.MP4Atom("trkn", testfile.MP4Data(0, []byte{0, 0, 0, 7})), tag.TrackNumber, "7"},
		{"disk of a number alone", testfile.MP4Atom("disk", testfile.MP4Data(0, []byte{0, 0, 0, 2})), tag.DiscNumber, "2"},
		{"disk with a trailing pad", testfile.MP4Atom("disk", testfile.MP4Data(0, []byte{0, 0, 0, 1, 0, 2, 0, 0})), tag.DiscTotal, "2"},
		{"stik typed unsigned", testfile.MP4Atom("stik", testfile.MP4Data(22, []byte{2})), tag.MediaType, "2"},
		{"cpil typed unsigned", testfile.MP4Atom("cpil", testfile.MP4Data(22, []byte{1})), tag.Compilation, "1"},
		{"title typed implicit", testfile.MP4Atom("\xa9alb", testfile.MP4Data(0, []byte("Album"))), tag.Album, "Album"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), c.item)
			doc := mustParseBytes(t, data)
			if v, ok := doc.Get(c.key); !ok || len(v) != 1 || v[0] != c.want {
				t.Errorf("%s = %v (ok=%v), want %q", c.key, v, ok, c.want)
			}
			// An edit elsewhere keeps the item in the form the file gave it.
			plan, err := doc.Edit().Set(tag.Title, "A Longer Title").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if out := applyToBytes(t, data, plan); !bytes.Contains(out, c.item) {
				t.Error("an edit of the title re-encoded the item")
			}
			// An edit that stores nothing writes nothing.
			plan, err = doc.Edit().Set(tag.Movement, "abc").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if !plan.IsNoOp() {
				t.Errorf("a discarded value planned %q", plan.Report().Operations)
			}
		})
	}
}

// A discarded edit of a file ffmpeg tagged writes nothing: its disk atom is two bytes longer than
// the one a rebuild renders.
func TestMP4DifferentialDiscardedEditWritesNothing(t *testing.T) {
	requireTool(t, "ffmpeg")
	path := filepath.Join(t.TempDir(), "tagged.m4a")
	ffmpegEncode(t, path, "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "aac",
		"-metadata", "title=T", "-metadata", "track=3/12", "-metadata", "disc=1/2", "-metadata", "genre=Rock")
	doc := mustParseFile(t, path)
	if v, _ := doc.Get(tag.DiscTotal); !slices.Equal(v, []string{"2"}) {
		t.Fatalf("DISCTOTAL = %v", v)
	}
	plan, err := doc.Edit().Set(tag.BPM, "abc").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() || !reportHasWarning(plan.Report().Warnings, wl.WarnValueDropped) {
		t.Errorf("planned %q with warnings %v, want nothing written and the drop reported", plan.Report().Operations, plan.Report().Warnings)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = doc.Edit().Set(tag.Album, "A").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.Execute(context.Background(), wl.SaveBack()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"trkn", "disk", "\xa9nam", "\xa9gen"} {
		at := bytes.Index(before, []byte(name)) - 4
		item := before[at : at+int(binary.BigEndian.Uint32(before[at:]))]
		if !bytes.Contains(after, item) {
			t.Errorf("an edit of the album re-encoded ffmpeg's %q item", name)
		}
	}
}

// Setting the eight keys on a bare file writes structured atoms with type-21 data, no "----"
// freeform fallback, and round-trips, including a literal advisory 0.
func TestMP4ITunesWriteCreatesAtoms(t *testing.T) {
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
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

// Every decode case must have its encode case, or a Title-only edit would delete the atom it owns.
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

// write-time warnings: an unstorable integer or BPM drops with WarnValueDropped, a non-boolean flag
// coerces to 0, and a fractional BPM coerces with the rounded value named in the message.
func TestMP4ITunesDropAndCoerceWarnings(t *testing.T) {
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))
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

// A file holding both the structured rtng and a freeform ITUNESADVISORY: the first is read and the
// other shows in the families, unselected when it disagrees.
func TestMP4ITunesAdvisoryDualRepresentation(t *testing.T) {
	build := func(freeformVal string) []byte {
		return testfile.MP4Tagged(
			testfile.MP4Text("\xa9nam", "T"),
			testfile.MP4Atom("rtng", testfile.MP4Data(21, []byte{1})),
			mp4Freeform("com.apple.iTunes", "ITUNESADVISORY", freeformVal),
		)
	}
	data := build("2")
	doc := mustParseBytes(t, data)
	if got, _ := doc.Tags().Get(tag.ITunesAdvisory); !slices.Equal(got, []string{"1"}) {
		t.Fatalf("ITUNESADVISORY = %v, want [1], the first atom's", got)
	}
	if !hasLintCode(doc, "conflicting-families") || hasLintCode(doc, "single-valued-multi") {
		t.Errorf("lint = %v, want conflicting-families and no single-valued-multi", doc.Lint())
	}
	if selected, ok := familySelected(doc, tag.ITunesAdvisory); !ok || selected {
		t.Errorf("distinct values: family Selected = %v (found=%v), want false", selected, ok)
	}
	same := mustParseBytes(t, build("1"))
	if selected, ok := familySelected(same, tag.ITunesAdvisory); !ok || !selected {
		t.Errorf("identical values: family Selected = %v (found=%v), want true", selected, ok)
	}

	// An edit elsewhere keeps both atoms, a freeform first as well, which no rebuild would write.
	for _, d := range [][]byte{data, testfile.MP4Tagged(
		mp4Freeform("com.apple.iTunes", "ITUNESADVISORY", "2"),
		testfile.MP4Atom("rtng", testfile.MP4Data(21, []byte{1})),
	)} {
		plan, err := mustParseBytes(t, d).Edit().Set(tag.Album, "A").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, d, plan)
		if !bytes.Contains(out, testfile.MP4Atom("rtng", testfile.MP4Data(21, []byte{1}))) ||
			!bytes.Contains(out, mp4Freeform("com.apple.iTunes", "ITUNESADVISORY", "2")) {
			t.Error("an edit of the album dropped or rewrote an advisory atom")
		}
		before, _ := mustParseBytes(t, d).Get(tag.ITunesAdvisory)
		if after, _ := mustParseBytes(t, out).Get(tag.ITunesAdvisory); !slices.Equal(after, before) {
			t.Errorf("ITUNESADVISORY = %v after the edit, was %v", after, before)
		}
	}
	plan, err := doc.Edit().Set(tag.ITunesAdvisory, "0").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, data, plan)
	if bytes.Contains(out, []byte("ITUNESADVISORY")) || !bytes.Contains(out, testfile.MP4Atom("rtng", testfile.MP4Data(21, []byte{0}))) {
		t.Error("an edit of the advisory did not leave the rtng atom alone holding it")
	}
}

// A numeric gnre beside a text genre atom: the first is read, an edit elsewhere keeps both where
// ffprobe reads them, and an edit of the genre leaves one atom.
func TestMP4GnreBesideTextGenre(t *testing.T) {
	gnre := testfile.MP4Atom("gnre", testfile.MP4Data(0, []byte{0, 18})) // Rock
	text := testfile.MP4Text("\xa9gen", "Custom")
	for _, c := range []struct {
		name  string
		items [][]byte
		want  string
	}{
		{"gnre first", [][]byte{gnre, text}, "Rock"},
		{"text first", [][]byte{text, gnre}, "Custom"},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := testfile.MP4Tagged(c.items...)
			doc := mustParseBytes(t, data)
			if g := doc.Fields().Genres; !slices.Equal(g, []string{c.want}) {
				t.Fatalf("GENRE = %v, want [%s]", g, c.want)
			}
			if selected, ok := familySelected(doc, tag.Genre); !ok || selected {
				t.Errorf("family selected = %v (found %v), want an unselected entry", selected, ok)
			}
			// Only the genre read is reported as resolved from a number.
			if got := hasWarning(doc, wl.WarnNumericGenre); got != (c.want == "Rock") {
				t.Errorf("numeric-genre warned = %v with GENRE read as %s", got, c.want)
			}
			plan, err := doc.Edit().Set(tag.Title, "T").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, data, plan)
			if !bytes.Contains(out, gnre) || !bytes.Contains(out, text) {
				t.Error("an edit of the title dropped or rewrote a genre atom")
			}
			if g := mustParseBytes(t, out).Fields().Genres; !slices.Equal(g, []string{c.want}) {
				t.Errorf("GENRE = %v after the edit, want [%s]", g, c.want)
			}
			if got, want := ffprobeTagsIfPresent(t, out), ffprobeTagsIfPresent(t, data); got != nil && got["genre"] != want["genre"] {
				t.Errorf("ffprobe genre = %q after the edit, was %q", got["genre"], want["genre"])
			}

			plan, err = doc.Edit().Set(tag.Genre, "Jazz").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out = applyToBytes(t, data, plan)
			if n := bytes.Count(out, []byte("gnre")) + bytes.Count(out, []byte("\xa9gen")); n != 1 {
				t.Errorf("%d genre atoms after an edit of the genre, want 1", n)
			}
			re := mustParseBytes(t, out)
			if g := re.Fields().Genres; !slices.Equal(g, []string{"Jazz"}) {
				t.Errorf("GENRE = %v, want [Jazz]", g)
			}
			if got := ffprobeTagsIfPresent(t, out); got != nil && got["genre"] != "Jazz" {
				t.Errorf("ffprobe genre = %q, want Jazz", got["genre"])
			}
		})
	}
}

// freeform-only ITUNESADVISORY file: it reads, and any edit (buildItems rebuilds every owned item)
// migrates it to the structured rtng atom with the value intact.
func TestMP4ITunesAdvisoryFreeformMigration(t *testing.T) {
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), mp4Freeform("com.apple.iTunes", "ITUNESADVISORY", "2"))
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

// The iTunes TV atoms read as the TV keys, and a new value of one goes to its atom. A number the
// integer atom cannot hold as written goes to a freeform, which loses nothing.
func TestMP4ITunesTVAtoms(t *testing.T) {
	u32 := func(name string, n byte) []byte {
		return testfile.MP4Atom(name, testfile.MP4Data(21, []byte{0, 0, 0, n}))
	}
	atoms := [][]byte{
		testfile.MP4Text("tvsh", "The Show"), testfile.MP4Text("tven", "S01E02"), testfile.MP4Text("tvnn", "Net"),
		testfile.MP4Text("sosn", "Show, The"), u32("tves", 2), u32("tvsn", 1),
	}
	want := map[tag.Key]string{
		tag.Show: "The Show", tag.EpisodeID: "S01E02", tag.Network: "Net", tag.ShowSort: "Show, The",
		tag.EpisodeNumber: "2", tag.SeasonNumber: "1",
	}
	data := testfile.MP4Tagged(atoms...)
	doc := mustParseBytes(t, data)
	for k, v := range want {
		if got, _ := doc.Get(k); !slices.Equal(got, []string{v}) {
			t.Errorf("%s = %v, want [%s]", k, got, v)
		}
	}
	if len(doc.Warnings()) != 0 {
		t.Errorf("warnings: %v", doc.Warnings())
	}

	ed := mustParseBytes(t, testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"))).Edit()
	for k, v := range want {
		ed.Set(k, v)
	}
	plan, err := ed.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T")), plan)
	for _, a := range atoms {
		if !bytes.Contains(out, a) {
			t.Errorf("the write did not render %q as the atom iTunes writes", a[4:8])
		}
	}
	if bytes.Contains(out, []byte("----")) {
		t.Error("a TV key was written as a freeform")
	}
	if probe := ffprobeTagsIfPresent(t, out); probe != nil {
		for k, v := range map[string]string{"show": "The Show", "episode_id": "S01E02", "network": "Net", "episode_sort": "2", "season_number": "1"} {
			if probe[k] != v {
				t.Errorf("ffprobe %s = %q, want %q", k, probe[k], v)
			}
		}
	}

	for _, vals := range [][]string{{"12a"}, {"07"}, {"4294967296"}, {"3", "4"}} {
		plan, err := mustParseBytes(t, data).Edit().Set(tag.EpisodeNumber, vals...).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		if got, _ := mustParseBytes(t, out).Get(tag.EpisodeNumber); !slices.Equal(got, vals) {
			t.Errorf("EPISODENUMBER %q read back as %v", vals, got)
		}
		if bytes.Contains(out, []byte("tves")) || reportHasWarning(plan.Report().Warnings, wl.WarnValueDropped) {
			t.Errorf("EPISODENUMBER %q: the integer atom stayed, or warnings %v", vals, plan.Report().Warnings)
		}
	}
}

// Picard writes an originalyear freeform beside originaldate, and the atom order puts the year
// first. The date is the value read and the year its echo: no conflict, and both stay until the
// date is edited.
func TestMP4YearBesideItsDateIsAnEcho(t *testing.T) {
	year := mp4Freeform("com.apple.iTunes", "originalyear", "1999")
	date := mp4Freeform("com.apple.iTunes", "originaldate", "1999-05-01")
	for name, items := range map[string][][]byte{"year first": {year, date}, "date first": {date, year}} {
		t.Run(name, func(t *testing.T) {
			data := testfile.MP4Tagged(slices.Concat([][]byte{testfile.MP4Text("\xa9nam", "T")}, items)...)
			doc := mustParseBytes(t, data)
			if v, _ := doc.Get(tag.OriginalDate); !slices.Equal(v, []string{"1999-05-01"}) {
				t.Errorf("ORIGINALDATE = %v, want the full date", v)
			}
			if len(doc.Lint()) != 0 {
				t.Errorf("lint: %v", doc.Lint())
			}
			plan, err := doc.Edit().Set(tag.Title, "Other").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if out := applyToBytes(t, data, plan); !bytes.Contains(out, year) || !bytes.Contains(out, date) {
				t.Error("an edit of the title dropped or rewrote a date atom")
			}
			plan, err = doc.Edit().Set(tag.OriginalDate, "2001-02-03").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, data, plan)
			re := mustParseBytes(t, out)
			if v, _ := re.Get(tag.OriginalDate); !slices.Equal(v, []string{"2001-02-03"}) || bytes.Contains(out, []byte("originalyear")) || len(re.Lint()) != 0 {
				t.Errorf("after an edit of the date: %v, lint %v, or the year atom stayed", v, re.Lint())
			}
		})
	}
	// A year that opens no date of the item read is a disagreement, the first read as ever.
	for name, c := range map[string]struct {
		items [][]byte
		want  string
	}{
		"another year first":     {[][]byte{mp4Freeform("com.apple.iTunes", "originalyear", "1998"), date}, "1998"},
		"another year second":    {[][]byte{date, mp4Freeform("com.apple.iTunes", "originalyear", "1998")}, "1999-05-01"},
		"a year and a month":     {[][]byte{mp4Freeform("com.apple.iTunes", "originalyear", "1999-06"), date}, "1999-06"},
		"a date in another form": {[][]byte{year, mp4Freeform("com.apple.iTunes", "originaldate", "1999/05/01")}, "1999"},
	} {
		doc := mustParseBytes(t, testfile.MP4Tagged(c.items...))
		if v, _ := doc.Get(tag.OriginalDate); !slices.Equal(v, []string{c.want}) || !hasLintCode(doc, "conflicting-families") {
			t.Errorf("%s: ORIGINALDATE = %v, lint %v; want %s and a conflict", name, v, doc.Lint(), c.want)
		}
	}
	// A year echoes any date the atoms read hold, the second of two under one name too.
	two := mustParseBytes(t, testfile.MP4Tagged(date, mp4Freeform("com.apple.iTunes", "originaldate", "2001-01-01"), mp4Freeform("com.apple.iTunes", "originalyear", "2001")))
	if v, _ := two.Get(tag.OriginalDate); !slices.Equal(v, []string{"1999-05-01", "2001-01-01"}) || hasLintCode(two, "conflicting-families") {
		t.Errorf("two dates and the second's year: ORIGINALDATE = %v, lint %v", v, two.Lint())
	}
	// The same bare year twice is one value.
	doc := mustParseBytes(t, testfile.MP4Tagged(year, mp4Freeform("com.apple.iTunes", "originaldate", "1999")))
	if v, _ := doc.Get(tag.OriginalDate); !slices.Equal(v, []string{"1999"}) || len(doc.Lint()) != 0 {
		t.Errorf("two atoms of one year: ORIGINALDATE = %v, lint %v", v, doc.Lint())
	}
}

// A total held twice by atoms that also hold the number: the first is read, the conflict shows,
// an edit elsewhere keeps both atoms, and an edit that stores nothing settles nothing.
func TestMP4TotalHeldBesideThePair(t *testing.T) {
	pair := mp4Pair("trkn", 3, 12)
	free := mp4Freeform("com.apple.iTunes", "TRACKTOTAL", "15")
	for name, c := range map[string]struct {
		items [][]byte
		total string
	}{
		"pair first": {[][]byte{pair, free}, "12"}, "freeform first": {[][]byte{free, pair}, "15"},
	} {
		t.Run(name, func(t *testing.T) {
			data := testfile.MP4Tagged(c.items...)
			doc := mustParseBytes(t, data)
			if n, _ := doc.Get(tag.TrackNumber); !slices.Equal(n, []string{"3"}) {
				t.Errorf("TRACKNUMBER = %v", n)
			}
			if v, _ := doc.Get(tag.TrackTotal); !slices.Equal(v, []string{c.total}) || !hasLintCode(doc, "conflicting-families") || hasLintCode(doc, "single-valued-multi") {
				t.Errorf("TRACKTOTAL = %v, lint %v; want [%s] and the conflict", v, doc.Lint(), c.total)
			}
			for _, edit := range []func(*wl.Editor) *wl.Editor{
				func(e *wl.Editor) *wl.Editor { return e.Set(tag.Album, "A") },
				func(e *wl.Editor) *wl.Editor { return e.Set(tag.Album, "A").Set(tag.TrackNumber, "abc") },
			} {
				plan, err := edit(doc.Edit()).Prepare()
				if err != nil {
					t.Fatal(err)
				}
				if out := applyToBytes(t, data, plan); !bytes.Contains(out, pair) || !bytes.Contains(out, free) {
					t.Errorf("an edit that leaves the track alone dropped or rewrote one of its atoms (%q)", plan.Report().Operations)
				}
			}
			plan, err := doc.Edit().Set(tag.TrackNumber, "abc").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if !plan.IsNoOp() || !reportHasWarning(plan.Report().Warnings, wl.WarnValueDropped) {
				t.Errorf("a discarded track number planned %q with %v", plan.Report().Operations, plan.Report().Warnings)
			}
			plan, err = doc.Edit().Set(tag.TrackTotal, "20").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			out := applyToBytes(t, data, plan)
			re := mustParseBytes(t, out)
			if v, _ := re.Get(tag.TrackTotal); !slices.Equal(v, []string{"20"}) || bytes.Contains(out, []byte("TRACKTOTAL")) || len(re.Lint()) != 0 {
				t.Errorf("after an edit of the total: %v, lint %v, or the freeform stayed", v, re.Lint())
			}
		})
	}
}

// An edit that stores nothing writes nothing, a respelling it would otherwise carry along
// included: here one that makes the ilst a byte longer, which no padding absorbs.
func TestMP4DiscardedEditDoesNotMigrate(t *testing.T) {
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), mp4Freeform("com.apple.iTunes", "MUSICBRAINZ_ALBUMSTATUS", "official"))
	if v, _ := mustParseBytes(t, data).Get(tag.ReleaseStatus); !slices.Equal(v, []string{"official"}) {
		t.Fatalf("RELEASESTATUS = %v", v)
	}
	plan, err := mustParseBytes(t, data).Edit().Set(tag.BPM, "abc").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.IsNoOp() || !reportHasWarning(plan.Report().Warnings, wl.WarnValueDropped) {
		t.Errorf("planned %q with warnings %v", plan.Report().Operations, plan.Report().Warnings)
	}
}

// ©enc mapping: an old freeform ----:ENCODEDBY still reads (valid-key fallback), any edit migrates
// it to ©enc, and a ©enc file round-trips.
func TestMP4EncodedBySpellingMigration(t *testing.T) {
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), mp4Freeform("com.apple.iTunes", "ENCODEDBY", "Jane"))
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

	enc := testfile.MP4Tagged(testfile.MP4Text("\xa9enc", "Jane"))
	plan2, err := mustParseBytes(t, enc).Edit().Set(tag.Title, "T2").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := mustParseBytes(t, applyToBytes(t, enc, plan2)).Get(tag.EncodedBy); len(v) != 1 || v[0] != "Jane" {
		t.Errorf("\xa9enc round-trip = %v, want [Jane]", v)
	}
}

// MP3 carrying both TBPM and TXXX:BPM. Both project (lint flags the single-valued key), and a BPM
// edit drops the stale TXXX and leaves exactly one TBPM.
func TestID3BPMDualRepresentation(t *testing.T) {
	data := append(testfile.ID3v2(3, testfile.TextFrame(3, "TBPM", "128"), txxxFrame(3, "BPM", "140")), mp3Audio(t)...)
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

// TXXX:BPM-only file: a value-changing BPM edit drops the stale user frame and emits a fresh TBPM,
// while a Title-only edit preserves the TXXX frame verbatim (ID3 preserves non-dirty frames, unlike
// MP4's full rebuild).
func TestID3BPMLegacyTXXXMigration(t *testing.T) {
	data := append(testfile.ID3v2(3, txxxFrame(3, "BPM", "140")), mp3Audio(t)...)
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

// MVIN frame: an "n/total" pair splits on read, composes on write, handles the total-only edge like
// TRCK, preserves a malformed number verbatim with the total-dropped warning, and keeps the
// accepted MOVEMENT="3/12" edge round-tripping.
func TestID3MovementPair(t *testing.T) {
	// Read: an MVIN pair splits into the two canonical keys, MVNM reads beside it.
	data := append(testfile.ID3v2(3, testfile.TextFrame(3, "MVIN", "3/12"), testfile.TextFrame(3, "MVNM", "Allegro")), mp3Audio(t)...)
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

// sets the eight keys on each main format, re-parses, and spot-checks the native representations;
// then copies M4A -> MP3 -> M4A and checks nothing is lost (an integer BPM, so the tmpo leg is
// lossless).
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

// A structured atom carrying a text-typed data atom (a nonconformant file another tool patched):
// its ASCII bytes must not be misread as a big-endian integer, so it projects nothing, and an
// unrelated edit preserves it verbatim.
func TestMP4TextTypedIntAtomPreserved(t *testing.T) {
	textTmpo := testfile.MP4Atom("tmpo", testfile.MP4Data(1, []byte("50")))
	data := testfile.MP4Tagged(testfile.MP4Text("\xa9nam", "T"), textTmpo)
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

// An MP3 already carrying an MVIN pair edited to a new MOVEMENT re-renders the one frame, not a
// second beside the stale original.
func TestID3MovementFrameManaged(t *testing.T) {
	data := append(testfile.ID3v2(3, testfile.TextFrame(3, "MVIN", "3/12")), mp3Audio(t)...)
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

// movementSplit's validity gate: an MVIN body whose side is not a valid movement integer stays one
// verbatim MOVEMENT value, with no total fabricated.
func TestID3MovementMalformedPairVerbatim(t *testing.T) {
	for _, body := range []string{"abc/1", "3/70000", "ab/cd/12"} {
		data := append(testfile.ID3v2(3, testfile.TextFrame(3, "MVIN", body)), mp3Audio(t)...)
		doc := mustParseBytes(t, data)
		if v, _ := doc.Get(tag.Movement); !slices.Equal(v, []string{body}) {
			t.Errorf("MVIN %q: MOVEMENT = %v, want the whole value verbatim", body, v)
		}
		if v, ok := doc.Get(tag.MovementTotal); ok {
			t.Errorf("MVIN %q: fabricated MOVEMENTTOTAL = %v, want none", body, v)
		}
	}
}
