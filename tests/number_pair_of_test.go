package waxlabel_test

import (
	"bytes"
	"slices"
	"testing"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// ofPairFormat builds a file of one tag family whose track number is stored as the text v.
type ofPairFormat struct {
	name  string
	build func(v string) []byte
	utf16 bool // the text is stored as UTF-16, not as the bytes of v
}

// holds reports whether data holds the text v as the format stores it.
func (f ofPairFormat) holds(data []byte, v string) bool {
	if f.utf16 {
		return bytes.Contains(data, utf16LENoBOM(v))
	}
	return bytes.Contains(data, []byte(v))
}

func ofPairFormats(t *testing.T) []ofPairFormat {
	return []ofPairFormat{
		{name: "flac", build: func(v string) []byte { return flacWithComments("TRACKNUMBER=" + v) }},
		{name: "ogg flac", build: func(v string) []byte {
			return testfile.OggFLAC(testfile.CommentBlock("v", "TRACKNUMBER="+v))
		}},
		{name: "mp3", build: func(v string) []byte { return mp3WithFrames(t, testfile.TextFrame(4, "TRCK", v)) }},
		{name: "mp3 utf16", utf16: true, build: func(v string) []byte {
			body := append([]byte{1, 0xFF, 0xFE}, utf16LENoBOM(v)...)
			return append(testfile.ID3v2(3, testfile.ID3Frame(3, "TRCK", body)), mp3Audio(t)...)
		}},
		{name: "matroska", build: func(v string) []byte {
			tags := testfile.El(testfile.IDTags, testfile.El(testfile.IDTag, concat(testfile.El(testfile.IDTargets, nil), testfile.SimpleTag("PART_NUMBER", v))))
			return testfile.Matroska("matroska", "", tags)
		}},
		{name: "wav", build: func(v string) []byte {
			return testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"IPRT", v}), testfile.WAVData(400))
		}},
		{name: "mp4", build: func(v string) []byte {
			return testfile.MP4Tagged(mp4Freeform("com.apple.iTunes", "TRACKNUMBER", v))
		}},
		{name: "mp4 keys", build: func(v string) []byte { return testfile.MP4MdtaFile([]string{"track"}, []string{v}) }},
		{name: "musepack", build: func(v string) []byte {
			return append(mpcSV7(100, 0), apeTagText(map[string]string{"Track": v})...)
		}},
		{name: "wma", build: func(v string) []byte {
			return asfFile(asfFileProperties(3*time.Second, 500), asfStreamProperties(0x0161, 2, 44100, 16),
				asfExtContentDescription(asfDescriptor{"WM/TrackNumber", 0, asfUTF16(v)}))
		}},
	}
}

// A track number written "3 of 12" reads as the same number and total on every tag family, as
// "3/12" does. Text that only holds the word stays whole on the number key.
func TestNumberPairOfReadsAcrossFormats(t *testing.T) {
	for _, tc := range []struct {
		in           string
		num, tot     []string
		track, total int
	}{
		{"3 of 12", []string{"3"}, []string{"12"}, 3, 12},
		{"03 OF 09", []string{"03"}, []string{"09"}, 3, 9},
		{"3of12", []string{"3"}, []string{"12"}, 3, 12},
		{"of 2", nil, []string{"2"}, 0, 2},
		{"3 of", []string{"3"}, nil, 3, 0},
		{"Part 3 of 12", []string{"Part 3 of 12"}, nil, 0, 0},
		{"1 of 2 of 3", []string{"1 of 2 of 3"}, nil, 0, 0},
	} {
		for _, f := range ofPairFormats(t) {
			doc := mustParseBytes(t, f.build(tc.in))
			gotNum, _ := doc.Get(tag.TrackNumber)
			gotTot, _ := doc.Get(tag.TrackTotal)
			if !slices.Equal(gotNum, tc.num) || !slices.Equal(gotTot, tc.tot) {
				t.Errorf("%s %q: num=%v tot=%v, want num=%v tot=%v", f.name, tc.in, gotNum, gotTot, tc.num, tc.tot)
			}
			if got := doc.Fields(); got.TrackNumber != tc.track || got.TrackTotal != tc.total {
				t.Errorf("%s %q: Fields track %d of %d, want %d of %d",
					f.name, tc.in, got.TrackNumber, got.TrackTotal, tc.track, tc.total)
			}
		}
	}
}

// The worded pair is a numbering value, so lint says nothing of it; text around it still is not.
func TestNumberPairOfLint(t *testing.T) {
	if codes := findingCodes(mustParseBytes(t, flacWithComments("TRACKNUMBER=3 of 12", "DISCNUMBER=1 of 2")).Lint()); codes["malformed-number"] {
		t.Error("\"3 of 12\" linted malformed-number")
	}
	if codes := findingCodes(mustParseBytes(t, flacWithComments("TRACKNUMBER=Part 3 of 12")).Lint()); !codes["malformed-number"] {
		t.Error("\"Part 3 of 12\" did not lint malformed-number")
	}
}

// Reading the pair leaves the file alone: a write that does not touch the numbering keeps the
// stored text, and the numbers either way.
func TestNumberPairOfSurvivesUnrelatedEdit(t *testing.T) {
	for _, f := range ofPairFormats(t) {
		if f.name == "wma" {
			continue // read-only
		}
		src := f.build("3 of 12")
		doc := mustParseBytes(t, src)
		plan, err := doc.Edit().Prepare()
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if !plan.IsNoOp() {
			t.Errorf("%s: an edit of nothing is not a no-op", f.name)
		}
		plan, err = doc.Edit().Set(tag.Artist, "New").Prepare()
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		out := applyToBytes(t, src, plan)
		// An MP4 write moves a number held as text into its trkn atom, whatever the edit.
		if kept := f.holds(out, "3 of 12"); kept == (f.name == "mp4") {
			t.Errorf("%s: stored \"3 of 12\" kept = %v after an unrelated edit", f.name, kept)
		}
		if got := mustParseBytes(t, out).Fields(); got.TrackNumber != 3 || got.TrackTotal != 12 {
			t.Errorf("%s: after an unrelated edit track %d of %d, want 3 of 12", f.name, got.TrackNumber, got.TrackTotal)
		}
	}
}

// An edit of either half rewrites the pair in the format's own form, and the edited half
// does not come back from the words.
func TestNumberPairOfEdits(t *testing.T) {
	for _, f := range ofPairFormats(t) {
		if f.name == "wma" {
			continue // read-only
		}
		for _, tc := range []struct {
			name       string
			edit       func(doc *wl.Document) *wl.Editor
			num, total []string
		}{
			{"set total", func(d *wl.Document) *wl.Editor { return d.Edit().Set(tag.TrackTotal, "20") }, []string{"3"}, []string{"20"}},
			{"set number", func(d *wl.Document) *wl.Editor { return d.Edit().Set(tag.TrackNumber, "7") }, []string{"7"}, []string{"12"}},
			{"clear total", func(d *wl.Document) *wl.Editor { return d.Edit().Clear(tag.TrackTotal) }, []string{"3"}, nil},
		} {
			src := f.build("3 of 12")
			plan, err := tc.edit(mustParseBytes(t, src)).Prepare()
			if err != nil {
				t.Fatalf("%s %s: %v", f.name, tc.name, err)
			}
			out := applyToBytes(t, src, plan)
			if f.holds(out, "3 of 12") {
				t.Errorf("%s %s: the stored \"3 of 12\" outlived an edit of the pair", f.name, tc.name)
			}
			re := mustParseBytes(t, out)
			gotNum, _ := re.Get(tag.TrackNumber)
			gotTot, _ := re.Get(tag.TrackTotal)
			if !slices.Equal(gotNum, tc.num) || !slices.Equal(gotTot, tc.total) {
				t.Errorf("%s %s: num=%v tot=%v, want num=%v tot=%v", f.name, tc.name, gotNum, gotTot, tc.num, tc.total)
			}
		}
	}
}

// Setting "3 of 12" stores the number and the total, as setting "3/12" does.
func TestNumberPairOfSet(t *testing.T) {
	for _, f := range ofPairFormats(t) {
		if f.name == "wma" {
			continue // read-only
		}
		src := f.build("1")
		plan, err := mustParseBytes(t, src).Edit().Set(tag.TrackNumber, "3 of 12").Prepare()
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		out := applyToBytes(t, src, plan)
		if f.holds(out, "3 of 12") {
			t.Errorf("%s: the words were stored", f.name)
		}
		re := mustParseBytes(t, out)
		gotNum, _ := re.Get(tag.TrackNumber)
		gotTot, _ := re.Get(tag.TrackTotal)
		if !slices.Equal(gotNum, []string{"3"}) || !slices.Equal(gotTot, []string{"12"}) {
			t.Errorf("%s: num=%v tot=%v, want [3] and [12]", f.name, gotNum, gotTot)
		}
	}
}

// A copy carries the numbers, not the words: an MP4 destination gets them in its trkn atom.
func TestNumberPairOfCopiesToMP4(t *testing.T) {
	src := mustParseBytes(t, flacWithComments("TRACKNUMBER=3 of 12", "DISCNUMBER=1 of 2"))
	dstBytes := testfile.MP4Tagged()
	plan, _, err := src.PrepareTransfer(mustParseBytes(t, dstBytes))
	if err != nil {
		t.Fatal(err)
	}
	out := applyToBytes(t, dstBytes, plan)
	if !bytes.Contains(out, mp4Pair("trkn", 3, 12)) {
		t.Error("the copy did not write trkn 3/12")
	}
	if got := mustParseBytes(t, out).Fields(); got.TrackNumber != 3 || got.TrackTotal != 12 || got.DiscNumber != 1 || got.DiscTotal != 2 {
		t.Errorf("copied track %d of %d, disc %d of %d", got.TrackNumber, got.TrackTotal, got.DiscNumber, got.DiscTotal)
	}
}

// A legacy APE tag holding the track as one "3/12" or "3 of 12" item agrees with an ID3 tag
// that holds the same number and total: no conflict, and the safe fix strips the copy. A total
// only the legacy tag holds keeps it.
func TestLegacyPairAgreesWithTheTagRead(t *testing.T) {
	for _, v := range []string{"3/12", "3 of 12", " 3 / 12 "} {
		data := append(mp3WithFrames(t, testfile.TextFrame(4, "TRCK", "3/12")), apeTagText(map[string]string{"Track": v})...)
		doc := mustParseBytes(t, data)
		if findingCodes(doc.Lint())["conflicting-families"] {
			t.Errorf("APE Track=%q beside TRCK 3/12 lints conflicting-families", v)
		}
		if v == "3/12" || v == "3 of 12" {
			if out := applyLintFix(t, data); bytes.Contains(out, []byte("APETAGEX")) {
				t.Errorf("APE Track=%q: the safe fix kept a legacy tag that holds nothing of its own", v)
			}
		}
	}

	data := append(mp3WithFrames(t, testfile.TextFrame(4, "TRCK", "3")), apeTagText(map[string]string{"Track": "3 of 12"})...)
	doc := mustParseBytes(t, data)
	if findingCodes(doc.Lint())["conflicting-families"] {
		t.Error("an APE total the ID3 tag lacks lints conflicting-families")
	}
	if got := doc.LegacyOnlyKeys(); !slices.Equal(got, []tag.Key{tag.TrackTotal}) {
		t.Errorf("LegacyOnlyKeys = %v, want [TRACKTOTAL]", got)
	}
	if out := applyLintFix(t, data); !bytes.Contains(out, []byte("APETAGEX")) {
		t.Error("the safe fix stripped a legacy tag holding the only total")
	}

	data = append(mp3WithFrames(t, testfile.TextFrame(4, "TRCK", "3/12")), apeTagText(map[string]string{"Track": "4 of 12"})...)
	if !findingCodes(mustParseBytes(t, data).Lint())["conflicting-families"] {
		t.Error("a legacy number that differs does not lint conflicting-families")
	}
}

// An MP4 text item of a number key holds what its text says and no more: a number with no
// total leaves the total to the item that has one, and a pair that repeats the trkn atom
// agrees with it.
func TestMP4TextNumberHoldsOnlyWhatItSays(t *testing.T) {
	freeform := func(name, v string) []byte { return mp4Freeform("com.apple.iTunes", name, v) }
	for _, v := range []string{"3/", "3 of"} {
		for name, data := range map[string][]byte{
			"a text total":    testfile.MP4Tagged(freeform("TRACKNUMBER", v), freeform("TRACKTOTAL", "20")),
			"the trkn total":  testfile.MP4Tagged(freeform("TRACKNUMBER", v), mp4Pair("trkn", 0, 20)),
			"a total, before": testfile.MP4Tagged(freeform("TRACKTOTAL", "20"), freeform("TRACKNUMBER", v)),
		} {
			doc := mustParseBytes(t, data)
			if got := doc.Fields(); got.TrackNumber != 3 || got.TrackTotal != 20 {
				t.Errorf("%q beside %s: track %d of %d, want 3 of 20", v, name, got.TrackNumber, got.TrackTotal)
			}
			plan, err := doc.Edit().Set(tag.Artist, "New").Prepare()
			if err != nil {
				t.Fatal(err)
			}
			if got := mustParseBytes(t, applyToBytes(t, data, plan)).Fields(); got.TrackNumber != 3 || got.TrackTotal != 20 {
				t.Errorf("%q beside %s: an unrelated edit left track %d of %d, want 3 of 20", v, name, got.TrackNumber, got.TrackTotal)
			}
		}
	}
	// A total with no number leaves the number to the trkn atom.
	doc := mustParseBytes(t, testfile.MP4Tagged(freeform("TRACKNUMBER", "/20"), mp4Pair("trkn", 3, 0)))
	if got := doc.Fields(); got.TrackNumber != 3 || got.TrackTotal != 20 {
		t.Errorf("\"/20\" beside trkn 3: track %d of %d, want 3 of 20", got.TrackNumber, got.TrackTotal)
	}

	for _, v := range []string{"3/12", "3 of 12"} {
		doc := mustParseBytes(t, testfile.MP4Tagged(mp4Pair("trkn", 3, 12), freeform("TRACKNUMBER", v)))
		if findingCodes(doc.Lint())["conflicting-families"] {
			t.Errorf("a text %q beside trkn 3/12 lints conflicting-families", v)
		}
		doc = mustParseBytes(t, testfile.MP4Tagged(mp4Pair("trkn", 3, 12), freeform("TRACKNUMBER", "4"+v[1:])))
		if !findingCodes(doc.Lint())["conflicting-families"] {
			t.Errorf("a text %q beside trkn 3/12 does not lint conflicting-families", "4"+v[1:])
		}
	}
}

// Clearing the total of an MP4 whose number is held as several text values, one with a total,
// leaves no total, and a repeat of the edit writes nothing.
func TestMP4ClearTotalOfAPairAmongSeveral(t *testing.T) {
	for _, v := range []string{"3/12", "3 of 12"} {
		data := testfile.MP4Tagged(mp4Freeform("com.apple.iTunes", "TRACKNUMBER", v), mp4Freeform("com.apple.iTunes", "TRACKNUMBER", "4"))
		plan, err := mustParseBytes(t, data).Edit().Clear(tag.TrackTotal).Prepare()
		if err != nil {
			t.Fatal(err)
		}
		out := applyToBytes(t, data, plan)
		re := mustParseBytes(t, out)
		if tot, ok := re.Get(tag.TrackTotal); ok {
			t.Errorf("%q: TRACKTOTAL = %v after the clear, want none", v, tot)
		}
		if again, err := re.Edit().Clear(tag.TrackTotal).Prepare(); err != nil || !again.IsNoOp() {
			t.Errorf("%q: the same clear again is not a no-op (err %v)", v, err)
		}
	}
}

// An ID3 tag that holds its track number in a TXXX:TRACKNUMBER frame reads a number with its
// total there as it reads TRCK, and an edit of either half leaves one number and one total.
func TestID3UserTextNumberPair(t *testing.T) {
	get := func(doc *wl.Document) (num, total []string) {
		num, _ = doc.Get(tag.TrackNumber)
		total, _ = doc.Get(tag.TrackTotal)
		return num, total
	}
	for _, v := range []string{"3/12", "3 of 12"} {
		data := mp3WithFrames(t, txxxFrame(4, "TRACKNUMBER", v))
		doc := mustParseBytes(t, data)
		if num, total := get(doc); !slices.Equal(num, []string{"3"}) || !slices.Equal(total, []string{"12"}) {
			t.Errorf("%q: read num=%v total=%v, want [3] and [12]", v, num, total)
		}
		if plan, err := doc.Edit().Prepare(); err != nil || !plan.IsNoOp() {
			t.Errorf("%q: an edit of nothing is not a no-op (err %v)", v, err)
		}
		for _, tc := range []struct {
			name       string
			edit       func(*wl.Document) *wl.Editor
			num, total []string
			kept       bool
		}{
			{"set total", func(d *wl.Document) *wl.Editor { return d.Edit().Set(tag.TrackTotal, "20") }, []string{"3"}, []string{"20"}, false},
			{"set number", func(d *wl.Document) *wl.Editor { return d.Edit().Set(tag.TrackNumber, "4") }, []string{"4"}, []string{"12"}, false},
			{"clear total", func(d *wl.Document) *wl.Editor { return d.Edit().Clear(tag.TrackTotal) }, []string{"3"}, nil, false},
			{"clear number", func(d *wl.Document) *wl.Editor { return d.Edit().Clear(tag.TrackNumber) }, nil, []string{"12"}, false},
			{"another key", func(d *wl.Document) *wl.Editor { return d.Edit().Set(tag.Artist, "A") }, []string{"3"}, []string{"12"}, true},
		} {
			plan, err := tc.edit(mustParseBytes(t, data)).Prepare()
			if err != nil {
				t.Fatalf("%q %s: %v", v, tc.name, err)
			}
			out := applyToBytes(t, data, plan)
			re := mustParseBytes(t, out)
			if num, total := get(re); !slices.Equal(num, tc.num) || !slices.Equal(total, tc.total) {
				t.Errorf("%q %s: num=%v total=%v, want num=%v total=%v", v, tc.name, num, total, tc.num, tc.total)
			}
			if kept := bytes.Contains(out, []byte(v)); kept != tc.kept {
				t.Errorf("%q %s: the stored text kept = %v, want %v", v, tc.name, kept, tc.kept)
			}
			if again, err := tc.edit(re).Prepare(); err != nil || !again.IsNoOp() {
				t.Errorf("%q %s: the same edit again is not a no-op (err %v)", v, tc.name, err)
			}
		}
	}
}

// An ID3 number frame holds one number and one total. An edit of either half re-renders it, and
// a second value the other half held goes with that: the plan says so, as an MP4's does.
func TestID3PairEditWarnsOfValuesItDrops(t *testing.T) {
	dropped := func(plan *wl.Plan, key tag.Key) bool {
		return slices.ContainsFunc(plan.Report().Warnings, func(w wl.Warning) bool {
			return w.Code == wl.WarnValueDropped && slices.Contains(w.Keys, key)
		})
	}
	for _, trck := range []string{"3\x004", "3/12\x004", "3 of 12\x004"} {
		doc := mustParseBytes(t, mp3WithFrames(t, testfile.TextFrame(4, "TRCK", trck)))
		plan, err := doc.Edit().Set(tag.TrackTotal, "20").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if !dropped(plan, tag.TrackNumber) {
			t.Errorf("TRCK %q, set total: no value-dropped warning for the second TRACKNUMBER", trck)
		}
		// An edit that leaves the frame alone drops nothing.
		plan, err = doc.Edit().Set(tag.Artist, "A").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if dropped(plan, tag.TrackNumber) {
			t.Errorf("TRCK %q, an unrelated edit: warned of a drop", trck)
		}
	}
	// One number and one total: nothing to drop.
	plan, err := mustParseBytes(t, mp3WithFrames(t, testfile.TextFrame(4, "TRCK", "3/12"))).Edit().Set(tag.TrackTotal, "20").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if dropped(plan, tag.TrackNumber) || dropped(plan, tag.TrackTotal) {
		t.Error("a single pair warned of a drop")
	}
}

// The two spellings of a pair are one value: a copy between files that differ only in the
// spelling writes nothing, whichever way it goes.
func TestNumberPairSpellingsCopyAsOne(t *testing.T) {
	for _, f := range ofPairFormats(t) {
		if f.name == "wma" {
			continue // read-only
		}
		for _, c := range [][2]string{{"3/12", "3 of 12"}, {"3 of 12", "3/12"}, {"03 OF 012", "03/012"}} {
			src, dst := mustParseBytes(t, f.build(c[0])), mustParseBytes(t, f.build(c[1]))
			plan, _, err := src.PrepareTransfer(dst)
			if err != nil {
				t.Fatalf("%s %q onto %q: %v", f.name, c[0], c[1], err)
			}
			if !plan.IsNoOp() {
				t.Errorf("%s: a copy of %q onto %q writes: %v", f.name, c[0], c[1], plan.Changes())
			}
		}
	}
}

// A disc number takes the words as a track number does.
func TestDiscNumberOfReads(t *testing.T) {
	for name, data := range map[string][]byte{
		"flac": flacWithComments("DISCNUMBER=1 of 2"),
		"mp3":  mp3WithFrames(t, testfile.TextFrame(4, "TPOS", "1 of 2")),
		"mp4":  testfile.MP4Tagged(mp4Freeform("com.apple.iTunes", "DISCNUMBER", "1 of 2")),
	} {
		if got := mustParseBytes(t, data).Fields(); got.DiscNumber != 1 || got.DiscTotal != 2 {
			t.Errorf("%s: disc %d of %d, want 1 of 2", name, got.DiscNumber, got.DiscTotal)
		}
	}
}

// A file that holds its numbering in more than one place keeps the numbers it reads through an
// edit of another key, and the edit settles: made again, it writes nothing.
func TestNumberPairInTwoPlacesSettles(t *testing.T) {
	freeform := func(name, v string) []byte { return mp4Freeform("com.apple.iTunes", name, v) }
	wav := func(chunks ...[]byte) []byte {
		return testfile.WAV(append(append([][]byte{testfile.WAVFmtPCM()}, chunks...), testfile.WAVData(400))...)
	}
	for name, data := range map[string][]byte{
		"mp4 text total beside trkn number":  testfile.MP4Tagged(freeform("TRACKNUMBER", "/12"), mp4Pair("trkn", 3, 0)),
		"mp4 text number beside trkn total":  testfile.MP4Tagged(freeform("TRACKNUMBER", "3 of"), mp4Pair("trkn", 0, 20)),
		"mp4 text pair beside the same trkn": testfile.MP4Tagged(mp4Pair("trkn", 3, 12), freeform("TRACKNUMBER", "3 of 12")),
		"mp4 text pair beside a text total":  testfile.MP4Tagged(freeform("TRACKNUMBER", "3/12"), freeform("TRACKTOTAL", "20")),
		"mp4 two text numbers":               testfile.MP4Tagged(freeform("TRACKNUMBER", "3 of 12"), freeform("TRACKNUMBER", "4")),
		"mp4 keys pair beside a total":       testfile.MP4MdtaFile([]string{"track", "TRACKTOTAL"}, []string{"3 of 12", "20"}),
		"mp3 TRCK beside TXXX pair":          mp3WithFrames(t, testfile.TextFrame(4, "TRCK", "3/12"), txxxFrame(4, "TRACKNUMBER", "3 of 12")),
		"mp3 TXXX pair beside TXXX total":    mp3WithFrames(t, txxxFrame(4, "TRACKNUMBER", "3 of 12"), txxxFrame(4, "TRACKTOTAL", "12")),
		"mp3 TRCK beside an APE pair":        append(mp3WithFrames(t, testfile.TextFrame(4, "TRCK", "3/12")), apeTagText(map[string]string{"Track": "3 of 12"})...),
		"wav IPRT pair beside an id3 pair":   wav(testfile.WAVInfo([2]string{"IPRT", "3 of 12"}), wavID3(testfile.ID3v2(4, testfile.TextFrame(4, "TRCK", "3/12")))),
		"wav IPRT total beside an id3 title": wav(testfile.WAVInfo([2]string{"IPRT", "of 12"}), wavID3(testfile.ID3v2(4, testfile.TextFrame(4, "TIT2", "T")))),
		"flac pair beside a total":           flacWithComments("TRACKNUMBER=3 of 12", "TRACKTOTAL=20"),
		"flac pair among several":            flacWithComments("TRACKNUMBER=3 of 12", "TRACKNUMBER=4"),
	} {
		doc := mustParseBytes(t, data)
		before := doc.Fields()
		if plan, err := doc.Edit().Prepare(); err != nil || !plan.IsNoOp() {
			t.Errorf("%s: an edit of nothing is not a no-op (err %v)", name, err)
		}
		plan, err := doc.Edit().Set(tag.Artist, "A").Prepare()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		re := mustParseBytes(t, applyToBytes(t, data, plan))
		if after := re.Fields(); after.TrackNumber != before.TrackNumber || after.TrackTotal != before.TrackTotal {
			t.Errorf("%s: track %d of %d became %d of %d through an edit of the artist",
				name, before.TrackNumber, before.TrackTotal, after.TrackNumber, after.TrackTotal)
		}
		if again, err := re.Edit().Set(tag.Artist, "A").Prepare(); err != nil || !again.IsNoOp() {
			t.Errorf("%s: the same edit again is not a no-op (err %v)", name, err)
		}
	}
}

// The warning is for a value the file loses. One another frame still holds, or one that only
// repeats the value kept, is not dropped; and a value another warning reports is reported once.
func TestID3PairEditWarnsOnlyOfValuesTheFileLoses(t *testing.T) {
	drops := func(plan *wl.Plan, key tag.Key) (n int) {
		for _, w := range plan.Report().Warnings {
			if w.Code == wl.WarnValueDropped && slices.Contains(w.Keys, key) {
				n++
			}
		}
		return n
	}
	for _, tc := range []struct {
		name   string
		frames [][]byte
		edit   func(*wl.Document) *wl.Editor
		key    tag.Key
		want   []string // what the key reads after the edit
	}{
		{"a total a TXXX frame repeats", [][]byte{testfile.TextFrame(4, "TRCK", "3/12"), txxxFrame(4, "TOTALTRACKS", "12")},
			func(d *wl.Document) *wl.Editor { return d.Edit().Set(tag.TrackNumber, "4") }, tag.TrackTotal, []string{"12"}},
		{"a second total a TXXX frame holds", [][]byte{testfile.TextFrame(4, "TRCK", "3/12"), txxxFrame(4, "TRACKTOTAL", "13")},
			func(d *wl.Document) *wl.Editor { return d.Edit().Set(tag.TrackNumber, "4") }, tag.TrackTotal, []string{"12", "13"}},
		{"a second number a TXXX frame holds", [][]byte{testfile.TextFrame(4, "TRCK", "3"), txxxFrame(4, "TRACKNUMBER", "4")},
			func(d *wl.Document) *wl.Editor { return d.Edit().Set(tag.TrackTotal, "20") }, tag.TrackNumber, []string{"3", "4"}},
		{"a number a TXXX frame repeats", [][]byte{testfile.TextFrame(4, "TRCK", "3/12"), txxxFrame(4, "TRACKNUMBER", "3 of 12")},
			func(d *wl.Document) *wl.Editor { return d.Edit().Set(tag.TrackTotal, "20") }, tag.TrackNumber, []string{"3"}},
		{"a second movement a TXXX frame holds", [][]byte{testfile.TextFrame(4, "MVIN", "2/5"), txxxFrame(4, "MOVEMENT", "3")},
			func(d *wl.Document) *wl.Editor { return d.Edit().Set(tag.MovementTotal, "6") }, tag.Movement, []string{"2", "3"}},
	} {
		data := mp3WithFrames(t, tc.frames...)
		plan, err := tc.edit(mustParseBytes(t, data)).Prepare()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got, _ := mustParseBytes(t, applyToBytes(t, data, plan)).Get(tc.key); !slices.Equal(got, tc.want) {
			t.Fatalf("%s: %s = %v after the edit, want %v", tc.name, tc.key, got, tc.want)
		}
		if n := drops(plan, tc.key); n != 0 {
			t.Errorf("%s: %d value-dropped warning(s) for %s, which lost nothing: %v", tc.name, n, tc.key, plan.Report().Warnings)
		}
	}

	blank := mp3WithFrames(t, testfile.TextFrame(4, "TIT2", "T"))
	plan, err := mustParseBytes(t, blank).Edit().Set(tag.TrackNumber, "3", "").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if n := drops(plan, tag.TrackNumber); n != 1 {
		t.Errorf("a trailing empty number: %d value-dropped warnings, want 1: %v", n, plan.Report().Warnings)
	}
	plan, err = mustParseBytes(t, blank).Edit().Set(tag.TrackNumber, "A1").Set(tag.TrackTotal, "12", "13").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if n := drops(plan, tag.TrackTotal); n != 1 {
		t.Errorf("totals beside a number that is none: %d value-dropped warnings, want 1: %v", n, plan.Report().Warnings)
	}
}

// An MP4 number given several values, the first holding its total, stores that first number,
// as it stores the first of several plain ones. It kept the number the file had.
func TestMP4StoresTheFirstOfSeveralNumbersGivenAsAPair(t *testing.T) {
	for _, v := range []string{"3 of 12", "3/12", "3"} {
		data := testfile.MP4Tagged(mp4Pair("trkn", 5, 20))
		plan, err := mustParseBytes(t, data).Edit().Set(tag.TrackNumber, v, "4").Prepare()
		if err != nil {
			t.Fatal(err)
		}
		re := mustParseBytes(t, applyToBytes(t, data, plan))
		if got, _ := re.Get(tag.TrackNumber); !slices.Equal(got, []string{"3"}) {
			t.Errorf("Set(TRACKNUMBER, %q, \"4\"): TRACKNUMBER = %v, want [3]", v, got)
		}
		// The total the file holds stays: the edit names the number alone.
		if got, _ := re.Get(tag.TrackTotal); !slices.Equal(got, []string{"20"}) {
			t.Errorf("Set(TRACKNUMBER, %q, \"4\"): TRACKTOTAL = %v, want [20]", v, got)
		}
	}
}

// A later value that only respells what the frame keeps is no loss: the same number with a
// leading zero, or the number and total the tag already holds written as a pair.
func TestID3PairEditDoesNotWarnOfARespelling(t *testing.T) {
	blank := mp3WithFrames(t, testfile.TextFrame(4, "TIT2", "T"))
	for _, tc := range []struct {
		name     string
		num, tot []string
		drops    int
	}{
		{"the number again, zero-padded", []string{"3", "03"}, nil, 0},
		{"the number and total again, as a pair", []string{"3", "3/12"}, []string{"12"}, 0},
		{"the same in words", []string{"3", "03 of 012"}, []string{"12"}, 0},
		{"a pair whose total the tag lacks", []string{"3", "3 of 12"}, nil, 1},
		{"a pair with another number", []string{"3", "4/12"}, []string{"12"}, 1},
		{"another number", []string{"3", "4"}, nil, 1},
	} {
		e := mustParseBytes(t, blank).Edit().Set(tag.TrackNumber, tc.num...)
		if tc.tot != nil {
			e = e.Set(tag.TrackTotal, tc.tot...)
		}
		plan, err := e.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		if n := countKeyed(plan, wl.WarnValueDropped, tag.TrackNumber); n != tc.drops {
			t.Errorf("%s: %d value-dropped warnings for TRACKNUMBER, want %d: %v", tc.name, n, tc.drops, plan.Report().Warnings)
		}
	}
	// A total respelled.
	plan, err := mustParseBytes(t, blank).Edit().Set(tag.TrackNumber, "3").Set(tag.TrackTotal, "12", "012").Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if n := countKeyed(plan, wl.WarnValueDropped, tag.TrackTotal); n != 0 {
		t.Errorf("a total respelled: %d value-dropped warnings, want 0: %v", n, plan.Report().Warnings)
	}
}
