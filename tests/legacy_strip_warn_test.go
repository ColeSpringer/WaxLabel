package waxlabel_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/internal/testfile"
	"github.com/colespringer/waxlabel/tag"
)

// legacyOnlyMP3 builds an MP3 whose ID3v2 carries TITLE and ARTIST while its ID3v1 trailer
// is the only home for ALBUM, RECORDINGDATE, COMMENT and GENRE.
func legacyOnlyMP3(t *testing.T) []byte {
	t.Helper()
	data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "V2 Title"), testfile.TextFrame(3, "TPE1", "V2 Artist"))
	data = append(data, mp3Audio(t)...)
	return append(data, testfile.ID3v1("", "", "Legacy Album", "1999", "legacy comment", 17)...)
}

// warningFor returns the plan warning with the given code, and whether it was present.
func warningFor(plan *wl.Plan, code wl.WarningCode) (wl.Warning, bool) {
	for _, w := range plan.Report().Warnings {
		if w.Code == code {
			return w, true
		}
	}
	return wl.Warning{}, false
}

// doc.go's contract: unaffected data, legacy tags included, is preserved, never stripped. An
// explicit LegacyStrip destroys legacy-only values, so it must say which.
func TestLegacyStripWarnsAboutWhatItDestroys(t *testing.T) {
	data := legacyOnlyMP3(t)
	doc := mustParseBytes(t, data)
	if got := doc.LegacyOnlyKeys(); len(got) != 4 {
		t.Fatalf("setup: legacy-only keys = %v, want 4", got)
	}
	plan, err := doc.Edit().Set(tag.Title, "New").Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnLegacyStripDropped)
	if !ok {
		t.Fatalf("no legacy-strip-dropped warning; got %v", plan.Report().Warnings)
	}
	want := []tag.Key{tag.Album, tag.RecordingDate, tag.Comment, tag.Genre}
	if !slices.Equal(w.Keys, want) {
		t.Errorf("warning keys = %v, want %v", w.Keys, want)
	}
	// The values are gone, which is what the warning reports.
	if v, ok := mustParseBytes(t, applyToBytes(t, data, plan)).Get(tag.Album); ok {
		t.Errorf("ALBUM = %v, want gone: the strip destroyed it, which is what the warning reports", v)
	}
}

// Why the rule takes an authority argument: a strip that also writes the value the legacy
// container held loses nothing, so naming that key would be a false alarm, and a copy sets most of
// the source's keys on the destination editor, which would make nearly every key one.
func TestLegacyStripJudgesTheEditedTags(t *testing.T) {
	data := legacyOnlyMP3(t)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Album, "Written Now").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnLegacyStripDropped)
	if !ok {
		t.Fatalf("no legacy-strip-dropped warning; got %v", plan.Report().Warnings)
	}
	if slices.Contains(w.Keys, tag.Album) {
		t.Errorf("warning names ALBUM while the edit writes it: %v", w.Keys)
	}
	if re := mustParseBytes(t, applyToBytes(t, data, plan)); re.Fields().Album != "Written Now" {
		t.Errorf("ALBUM = %q, want the written value", re.Fields().Album)
	}
}

// The rule tests the edited tags, so a key the edit clears looks absent from the authority and
// could read as "held only in the legacy container".
func TestLegacyStripIgnoresKeysTheEditRemoved(t *testing.T) {
	// Both containers carry the same TITLE, so clearing it loses nothing either strips.
	data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Same Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, testfile.ID3v1("Same Title", "", "", "", "", 255)...)
	plan, err := mustParseBytes(t, data).Edit().Clear(tag.Title).
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnLegacyStripDropped); ok {
		t.Errorf("clearing a key the canonical set also held was reported as a strip loss: %v", w.Keys)
	}
}

func TestLegacyStripStillReportsGenuinelyLegacyOnly(t *testing.T) {
	data := legacyOnlyMP3(t)
	plan, err := mustParseBytes(t, data).Edit().Clear(tag.Title).
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnLegacyStripDropped)
	if !ok {
		t.Fatalf("a genuinely legacy-only value went unreported: %v", plan.Report().Warnings)
	}
	if !slices.Contains(w.Keys, tag.Album) {
		t.Errorf("warning keys = %v, want ALBUM among them", w.Keys)
	}
}

func TestLegacyStripSilentWhenNothingIsLost(t *testing.T) {
	data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Same Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, testfile.ID3v1("Same Title", "", "", "", "", 255)...)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "A").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnLegacyStripDropped); ok {
		t.Errorf("a redundant legacy container lost nothing but warned: %v", plan.Report().Warnings)
	}
}

// PlanLintFix adds LegacyStrip only when neither loss predicate holds, computed from the same
// primitives against the same document, so the safe fix can never destroy what this warning
// reports.
func TestLintFixNeverTripsLegacyStripWarning(t *testing.T) {
	for _, tc := range []struct {
		name string
		data func(*testing.T) []byte
	}{
		{"legacy-only values", legacyOnlyMP3},
		{"redundant container", func(t *testing.T) []byte {
			data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Same Title"))
			data = append(data, mp3Audio(t)...)
			return append(data, testfile.ID3v1("Same Title", "", "", "", "", 255)...)
		}},
		{"legacy container echoing a stamped ENCODER", func(t *testing.T) []byte {
			// PlanLintFix clears the stamped ENCODER, so the ID3v1 comment carrying the same stamp reads as
			// legacy-only against the edited set. Nothing is lost: the canonical set held it too.
			data := testfile.ID3v2(3, testfile.TextFrame(3, "TIT2", "Song"), testfile.TextFrame(3, "TSSE", "Lavf62.3.100"))
			data = append(data, mp3Audio(t)...)
			return append(data, testfile.ID3v1("Song", "", "", "", "", 255)...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustParseBytes(t, tc.data(t))
			fix := doc.PlanLintFix()
			plan, err := doc.Edit().Apply(fix.Patch).Prepare(fix.Options...)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := warningFor(plan, wl.WarnLegacyStripDropped); ok {
				t.Errorf("lint --fix destroyed legacy-only data: %v", plan.Report().Warnings)
			}
		})
	}
}

// WAV and AIFF reuse LegacyStrip to mean "consolidate into the id3 chunk", where the values move
// rather than die, and they never mark a family Legacy.
func TestLegacyStripSilentOnWAV(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(), testfile.WAVInfo([2]string{"INAM", "Song"}, [2]string{"ICOP", "ACME"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnLegacyStripDropped); ok {
		t.Errorf("a WAV LIST/INFO consolidation warned as a destructive strip: %v", plan.Report().Warnings)
	}
	if re := mustParseBytes(t, applyToBytes(t, data, plan)); re.Fields().Copyright != "ACME" {
		t.Errorf("the consolidated value was lost: copyright = %q", re.Fields().Copyright)
	}
}

// WAV reuses LegacyStrip to mean "consolidate LIST/INFO into the id3 chunk", but an item with no
// canonical key (IKEY, ISBJ) has no frame to move into, so the chunk drop destroys it and must
// warn.
func TestWAVLegacyStripWarnsAboutUnmappedItems(t *testing.T) {
	data := testfile.WAV(testfile.WAVFmtPCM(),
		testfile.WAVInfo([2]string{"INAM", "Song"}, [2]string{"IKEY", "Alice"}, [2]string{"ISBJ", "Subj"}), testfile.WAVData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnLegacyStripDropped)
	if !ok {
		t.Fatalf("unmapped INFO items were destroyed silently; warnings = %v", plan.Report().Warnings)
	}
	for _, id := range []string{"IKEY", "ISBJ"} {
		if !strings.Contains(w.Message, id) {
			t.Errorf("warning does not name %s: %q", id, w.Message)
		}
	}
}

// stackedV1 is two ID3v1 tags, one after the other, holding values the clean fixtures do not.
func stackedV1() []byte {
	return slices.Concat(testfile.ID3v1("First Title", "", "First Album", "", "", 255),
		testfile.ID3v1("Second Title", "", "", "", "second comment", 255))
}

// Stacked ID3v1 tags after a WavPack, Monkey's Audio, Musepack, MP3, ADTS or FLAC stream are each read
// into the family view. A strip names the values only they held and removes the whole run in one go,
// leaving the clean file, and the digest is the clean file's throughout.
func TestStackedID3v1AfterTheStream(t *testing.T) {
	for _, path := range []string{notagsWV, notagsAPE, chaptersMPC, notagsMP3, notagsAAC, notagsFLAC} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			clean := readFixture(t, path)
			src := slices.Concat(clean, stackedV1())
			essence := essenceOf(t, clean)

			doc := mustParseBytes(t, src)
			var titles []string
			for _, f := range doc.Families() {
				if f.Family == wl.FamilyID3v1 && f.Key == tag.Title {
					titles = append(titles, f.Values...)
				}
			}
			if !slices.Equal(titles, []string{"First Title", "Second Title"}) {
				t.Errorf("ID3v1 titles in the family view = %v, want one per tag", titles)
			}
			if w, ok := docWarning(doc, wl.WarnTrailingID3v1); !ok || !strings.Contains(w.Message, "2 stacked") {
				t.Errorf("ID3v1 warning = %q (present %v), want it to count both tags", w.Message, ok)
			}
			if !essenceOf(t, src).Equal(essence) {
				t.Error("the stacked tags changed the digest")
			}

			plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
			if err != nil {
				t.Fatal(err)
			}
			w, ok := warningFor(plan, wl.WarnLegacyStripDropped)
			if want := []tag.Key{tag.Title, tag.Album, tag.Comment}; !ok || !slices.Equal(w.Keys, want) {
				t.Errorf("strip warning keys = %v (present %v), want %v", w.Keys, ok, want)
			}
			stripped := applyToBytes(t, src, plan)
			if !bytes.Equal(stripped, clean) {
				t.Error("one strip did not leave the clean file")
			}
			if !essenceOf(t, stripped).Equal(essence) {
				t.Error("the digest changed across the strip")
			}
		})
	}
}

// An ID3v1 tag sealed between the stream and the APE tag, where appending the APE tag after it left
// it, is found as a legacy tag rather than read as audio, next to the APE tag, and a strip removes
// it. After MP3, ADTS and FLAC, where the APE tag is legacy too, the strip removes both.
func TestID3v1AheadOfTheAPETag(t *testing.T) {
	coda := func(path string) []byte {
		return slices.Concat(readFixture(t, path), headedAPETag(t, "Title", "Coda Song"))
	}
	for _, c := range []struct {
		name   string
		clean  []byte
		legacy bool // the APE tag is legacy too, so the strip removes it
	}{
		{"sample.wv", readFixture(t, sampleWV), false},
		{"sample.ape", readFixture(t, sampleAPE), false},
		{"chapters.mpc", coda(chaptersMPC), false},
		{"notags.mp3", coda(notagsMP3), true},
		{"notags.aac", coda(notagsAAC), true},
		{"notags.flac", coda(notagsFLAC), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			start := apeTagStart(c.clean)
			src := slices.Concat(c.clean[:start], testfile.ID3v1("Sealed Title", "", "", "", "", 255), c.clean[start:])
			want := mustParseBytes(t, c.clean)
			essence := essenceOf(t, c.clean)

			doc := mustParseBytes(t, src)
			if !hasWarning(doc, wl.WarnTrailingID3v1) || !hasFamily(doc, wl.FamilyID3v1, tag.Title) {
				t.Errorf("the sealed ID3v1 tag was not found; warnings %v", doc.Warnings())
			}
			if !hasFamily(doc, wl.FamilyAPEv2, tag.Title) {
				t.Errorf("the APE tag after the sealed tag is not in the family view: %+v", doc.Families())
			}
			if !doc.Tags().Equal(want.Tags()) || !essenceOf(t, src).Equal(essence) {
				t.Error("the sealed tag changed the tags or the digest")
			}
			plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
			if err != nil {
				t.Fatal(err)
			}
			stripped := applyToBytes(t, src, plan)
			re := mustParseBytes(t, stripped)
			if bytes.Contains(stripped, []byte("Sealed Title")) || hasWarning(re, wl.WarnTrailingID3v1) {
				t.Error("the strip kept the sealed ID3v1 tag")
			}
			if gone := !bytes.Contains(stripped, []byte("APETAGEX")) && !hasWarning(re, wl.WarnLegacyAPE) &&
				!hasFamily(re, wl.FamilyAPEv2, tag.Title); gone != c.legacy {
				t.Errorf("APE tag gone after the strip = %v, want %v", gone, c.legacy)
			}
			if !re.Tags().Equal(want.Tags()) || !essenceOf(t, stripped).Equal(essence) {
				t.Error("the strip changed the tags or the digest")
			}
		})
	}
}

// A WavPack file carrying every legacy shape at once - a leading ID3v2 tag and a gap before the first
// block, then after the blocks the APE tag, junk, a Lyrics3 block and stacked ID3v1 tags - reads as
// the clean file does. One legacy strip leaves the blocks, the junk and the APE tag, which a re-parse
// reads alike, and the digest is the clean file's throughout.
func TestWavPackEveryLegacyShape(t *testing.T) {
	clean := readFixture(t, sampleWV)
	start := apeTagStart(clean)
	stuck := trailerJunk(300)
	src := slices.Concat(leadingTitleID3(), leadingGap, clean, stuck,
		testfile.Lyrics3v2([2]string{"LYR", "words"}), stackedV1())
	want := mustParseBytes(t, clean)
	essence := essenceOf(t, clean)

	doc := mustParseBytes(t, src)
	if !doc.Tags().Equal(want.Tags()) || !reflect.DeepEqual(doc.Properties(), want.Properties()) {
		t.Errorf("tags %v, properties %+v; want the clean file's %v, %+v",
			doc.Tags().Keys(), doc.Properties(), want.Tags().Keys(), want.Properties())
	}
	for _, code := range []wl.WarningCode{wl.WarnStrayLeadingID3, wl.WarnLeadingBytes, wl.WarnTrailingBytes,
		wl.WarnTrailingLyrics3, wl.WarnTrailingID3v1} {
		if !hasWarning(doc, code) {
			t.Errorf("no %s warning; got %v", code, doc.Warnings())
		}
	}
	if !essenceOf(t, src).Equal(essence) {
		t.Error("the digest differs from the clean file's")
	}

	plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	stripped := applyToBytes(t, src, plan)
	if !bytes.HasPrefix(stripped, slices.Concat(clean[:start], stuck)) || apeTagStart(stripped) != start+len(stuck) {
		t.Error("the strip did not leave the blocks, the junk, then the APE tag")
	}
	re := mustParseBytes(t, stripped)
	if !re.Tags().Equal(doc.Tags()) || !reflect.DeepEqual(re.Properties(), doc.Properties()) {
		t.Errorf("after the strip: tags %v, properties %+v; want %v, %+v",
			re.Tags().Keys(), re.Properties(), doc.Tags().Keys(), doc.Properties())
	}
	for _, code := range []wl.WarningCode{wl.WarnStrayLeadingID3, wl.WarnLeadingBytes, wl.WarnTrailingLyrics3, wl.WarnTrailingID3v1} {
		if hasWarning(re, code) {
			t.Errorf("the strip kept what %s reports", code)
		}
	}
	if !hasWarning(re, wl.WarnTrailingBytes) {
		t.Error("the junk is no longer reported")
	}
	if !essenceOf(t, stripped).Equal(essence) {
		t.Error("the digest changed across the strip")
	}
}

// An MP3 carrying every legacy shape at once - an ID3v2 tag and bytes before the first frame, then
// after the frames an APE tag, junk, a Lyrics3 block and stacked ID3v1 tags - reads as the tag and
// the frames alone do. One legacy strip leaves the tag, the bytes before the first frame, the frames
// and the junk, which a re-parse reads alike, and the digest is the bare stream's throughout.
func TestMP3EveryLegacyShape(t *testing.T) {
	audio := readFixture(t, notagsMP3)
	stuck := trailerJunk(300)
	src := slices.Concat(leadingTitleID3(), leadingGap, audio, apeTag(map[string]string{"Album": "APE Album"}), stuck,
		testfile.Lyrics3v2([2]string{"LYR", "words"}), stackedV1())
	want := mustParseBytes(t, slices.Concat(leadingTitleID3(), audio))
	essence := essenceOf(t, audio)

	doc := mustParseBytes(t, src)
	if !doc.Tags().Equal(want.Tags()) || !reflect.DeepEqual(doc.Properties(), want.Properties()) {
		t.Errorf("tags %v, properties %+v; want the tag and frames alone's %v, %+v",
			doc.Tags().Keys(), doc.Properties(), want.Tags().Keys(), want.Properties())
	}
	for _, code := range []wl.WarningCode{wl.WarnLeadingBytes, wl.WarnLegacyAPE, wl.WarnTrailingBytes,
		wl.WarnTrailingLyrics3, wl.WarnTrailingID3v1} {
		if !hasWarning(doc, code) {
			t.Errorf("no %s warning; got %v", code, doc.Warnings())
		}
	}
	if !hasFamily(doc, wl.FamilyAPEv2, tag.Album) || !hasFamily(doc, wl.FamilyID3v1, tag.Comment) {
		t.Errorf("the APE and ID3v1 tags are not in the family view: %+v", doc.Families())
	}
	if !essenceOf(t, src).Equal(essence) {
		t.Error("the digest differs from the bare stream's")
	}

	plan, err := doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	stripped := applyToBytes(t, src, plan)
	if !bytes.HasSuffix(stripped, slices.Concat(leadingGap, audio, stuck)) {
		t.Error("the strip did not leave the bytes before the first frame, the frames, then the junk")
	}
	re := mustParseBytes(t, stripped)
	if !re.Tags().Equal(doc.Tags()) || !reflect.DeepEqual(re.Properties(), doc.Properties()) {
		t.Errorf("after the strip: tags %v, properties %+v; want %v, %+v",
			re.Tags().Keys(), re.Properties(), doc.Tags().Keys(), doc.Properties())
	}
	for _, code := range []wl.WarningCode{wl.WarnLegacyAPE, wl.WarnTrailingLyrics3, wl.WarnTrailingID3v1} {
		if hasWarning(re, code) {
			t.Errorf("the strip kept what %s reports", code)
		}
	}
	for _, code := range []wl.WarningCode{wl.WarnLeadingBytes, wl.WarnTrailingBytes} {
		if !hasWarning(re, code) {
			t.Errorf("the strip lost what %s reports", code)
		}
	}
	if !essenceOf(t, stripped).Equal(essence) {
		t.Error("the digest changed across the strip")
	}
}

// A legacy strip must not move the audio end. MP3 and ADTS measure their end as a strip leaves the
// file, so an APE tag between two concatenated streams leaves the second in the essence, kept around
// the tag, and a tag filling the MP3 resync window hides nothing from it. Each shape parses, strips
// under the essence check and re-reads to the digest of the stripped file, which a plain edit, which
// keeps the tags, shares; a long second stream costs a few reads, not a walk of its frames, which
// only the length of a stream of mixed bitrates takes, so the long rows open with a headed stream
// of one bitrate. The ID3 tags between two joined files are no tail tags: they stay in the audio
// with both streams, reported by their count, whether or not the last tag is stripped.
func TestLegacyStripKeepsTheAudioEnd(t *testing.T) {
	mp3, aac := readFixture(t, notagsMP3), readFixture(t, notagsAAC)
	headed := mp3VBRStream(40, 40)
	headed[43] = 3 // frame count and byte count present
	binary.BigEndian.PutUint32(headed[48:52], uint32(len(headed)))
	ape := headedAPETag(t, "Title", "Coda")
	v1 := testfile.ID3v1("V1", "", "", "", "", 255)
	long := mp3Frames(10000)
	lead := ffmpegTag()
	for _, c := range []struct {
		name   string
		data   []byte   // the file
		audio  []byte   // what its essence covers
		strip  []byte   // what the strip leaves
		ape    bool     // the APE tag is a legacy tail tag, in the family view
		reads  int      // most reads a parse may make, 0 for no bound
		native []string // the native view's entries
		warn   []string // how its trailing-bytes warnings begin, in order
	}{
		{"mp3: a tag between two streams", slices.Concat(mp3, ape, mp3, v1), slices.Concat(mp3, mp3), slices.Concat(mp3, mp3), true, 0,
			[]string{"MPEG audio", "APEv2", "MPEG audio", "ID3v1"}, []string{"16482 byte(s) after an APE tag are audio"}},
		{"aac: a tag between two streams", slices.Concat(aac, ape, aac, v1), slices.Concat(aac, aac), slices.Concat(aac, aac), true, 0,
			[]string{"ADTS audio", "APEv2", "ADTS audio", "ID3v1"}, []string{"23548 byte(s) after an APE tag are audio"}},
		{"mp3: a tag before a long second stream", slices.Concat(headed, ape, long, v1), slices.Concat(headed, long), slices.Concat(headed, long), true, 64,
			[]string{"MPEG audio", "APEv2", "MPEG audio", "ID3v1"}, []string{fmt.Sprintf("%d byte(s) after an APE tag are audio", len(long))}},
		{"mp3: a tag filling the resync window", slices.Concat(mp3Frames(40), apeTag(map[string]string{"Title": strings.Repeat("x", 100<<10)}), trailerJunk(10)),
			mp3Frames(40), slices.Concat(mp3Frames(40), trailerJunk(10)), true, 0,
			[]string{"MPEG audio", "APEv2", "trailing bytes"}, []string{"10 byte(s) after the MPEG stream belong to no tag"}},
		// The count of the first stream ends at a tag the second goes on behind, as cat leaves two
		// tagged files: the tags between stay in the audio and are counted, whether they lie in the
		// resync window before the tags at the end or before it.
		{"mp3: an ID3v1 tag between two streams", slices.Concat(mp3, v1, mp3, v1), slices.Concat(mp3, v1, mp3), slices.Concat(mp3, v1, mp3), false, 0,
			[]string{"MPEG audio", "ID3v1"}, []string{"1 ID3 tag(s) between joined MPEG streams"}},
		{"mp3: two ffmpeg-made files", slices.Concat(lead, mp3, lead, mp3, v1), slices.Concat(mp3, lead, mp3),
			slices.Concat(lead, mp3, lead, mp3), false, 0,
			[]string{"ID3v2.4", "MPEG audio", "ID3v1"}, []string{"1 ID3 tag(s) between joined MPEG streams"}},
		{"mp3: two ffmpeg-made files, the second long", slices.Concat(lead, headed, lead, long, v1), slices.Concat(headed, lead, long),
			slices.Concat(lead, headed, lead, long), false, 64,
			[]string{"ID3v2.4", "MPEG audio", "ID3v1"}, []string{"1 ID3 tag(s) between joined MPEG streams"}},
		// The resync window opens 34 bytes before the tag, inside the first stream's last frame.
		{"mp3: two ffmpeg-made files, the join at the window's start", slices.Concat(lead, mp3, lead, mp3Frames(157)),
			slices.Concat(mp3, lead, mp3Frames(157)), slices.Concat(lead, mp3, lead, mp3Frames(157)), false, 0,
			[]string{"ID3v2.4", "MPEG audio"}, []string{"1 ID3 tag(s) between joined MPEG streams"}},
		// With no VBR header the frames in the window pass the tag the second file opens with.
		{"mp3: two files without a VBR header", slices.Concat(mp3Frames(40), lead, mp3Frames(40), trailerJunk(10)),
			slices.Concat(mp3Frames(40), lead, mp3Frames(40)), slices.Concat(mp3Frames(40), lead, mp3Frames(40), trailerJunk(10)), false, 0,
			[]string{"MPEG audio", "trailing bytes"},
			[]string{"10 byte(s) after the MPEG stream belong to no tag", "1 ID3 tag(s) between joined MPEG streams"}},
		// The frame walk goes on past the tag the second file opens with, as the frames after it are
		// the same stream's, and past every such tag after it.
		{"aac: two ffmpeg-made files", slices.Concat(lead, aac, lead, aac, v1), slices.Concat(aac, lead, aac),
			slices.Concat(lead, aac, lead, aac), false, 0,
			[]string{"ID3v2.4", "ADTS audio", "ID3v1"}, []string{"1 ID3 tag(s) between joined ADTS streams"}},
		{"aac: three ffmpeg-made files", slices.Concat(lead, aac, lead, aac, lead, aac, v1), slices.Concat(aac, lead, aac, lead, aac),
			slices.Concat(lead, aac, lead, aac, lead, aac), false, 0,
			[]string{"ID3v2.4", "ADTS audio", "ID3v1"}, []string{"2 ID3 tag(s) between joined ADTS streams"}},
		{"aac: an ID3v1 and an ID3v2 tag between two streams", slices.Concat(aac, v1, lead, aac, v1), slices.Concat(aac, v1, lead, aac),
			slices.Concat(aac, v1, lead, aac), false, 0,
			[]string{"ADTS audio", "ID3v1"}, []string{"2 ID3 tag(s) between joined ADTS streams"}},
		// The first stream's count holds over the file, where a single frame follows it before the tag,
		// and not over it without the tag, where the frames after the tag go on from that frame.
		{"mp3: one frame, then a tag the stream goes on past", slices.Concat(mp3, mp3Frames(1), ape, mp3Frames(39), v1),
			slices.Concat(mp3, mp3Frames(40)), slices.Concat(mp3, mp3Frames(40)), true, 0,
			[]string{"MPEG audio", "MPEG audio", "APEv2", "MPEG audio", "ID3v1"},
			[]string{"417 byte(s) before an APE tag are audio", "16263 byte(s) after an APE tag are audio"}},
		// With no VBR header the frames in the window claim the tag between two runs of them as audio,
		// and the tag wins: it is read and stripped, and the second run goes on from the first across it.
		{"mp3: a tag between two runs of frames", slices.Concat(mp3Frames(40), ape, mp3Frames(40), trailerJunk(10)),
			slices.Concat(mp3Frames(40), mp3Frames(40)), slices.Concat(mp3Frames(40), mp3Frames(40), trailerJunk(10)), true, 0,
			[]string{"MPEG audio", "APEv2", "MPEG audio", "trailing bytes"},
			[]string{fmt.Sprintf("%d byte(s) after an APE tag are audio", 40*417), "10 byte(s) after the MPEG stream belong to no tag"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := &countingSource{ReaderAtSized: wl.BytesSource(c.data)}
			doc, err := wl.Parse(context.Background(), src)
			if err != nil {
				t.Fatal(err)
			}
			if c.reads > 0 && src.reads > c.reads {
				t.Errorf("%d reads of a %d-byte file, want at most %d", src.reads, len(c.data), c.reads)
			}
			var native []string
			for _, e := range doc.Native().Describe() {
				if !strings.HasPrefix(e.Kind, " ") {
					native = append(native, e.Kind)
				}
			}
			if !slices.Equal(native, c.native) {
				t.Errorf("native view = %q, want %q", native, c.native)
			}
			var warns []string
			for _, w := range doc.Warnings() {
				if w.Code == wl.WarnTrailingBytes {
					warns = append(warns, w.Message)
				}
			}
			if len(warns) != len(c.warn) {
				t.Errorf("trailing-bytes warnings = %q, want %d", warns, len(c.warn))
			} else {
				for i, want := range c.warn {
					if !strings.HasPrefix(warns[i], want) {
						t.Errorf("trailing-bytes warning %q does not begin %q", warns[i], want)
					}
				}
			}
			essence := essenceOf(t, c.audio)
			if !essenceOf(t, c.data).Equal(essence) {
				t.Error("the digest is not the audio's")
			}
			if hasFamily(doc, wl.FamilyAPEv2, tag.Title) != c.ape {
				t.Errorf("APE title in the family view = %v, want %v", !c.ape, c.ape)
			}
			if w, ok := docWarning(doc, wl.WarnTruncatedAudio); ok {
				t.Errorf("a stream that goes on reads as truncated: %q", w.Message)
			}
			plans := map[string]struct {
				prepare func() (*wl.Plan, error)
				want    func(out []byte) bool
			}{
				"strip": {func() (*wl.Plan, error) {
					return doc.Edit().Prepare(wl.WithLegacyPolicy(wl.LegacyStrip), wl.WithVerifyEssence())
				}, func(out []byte) bool { return bytes.Equal(out, c.strip) }},
				"edit": {func() (*wl.Plan, error) {
					return doc.Edit().Set(tag.Title, "Edited").Prepare(wl.WithVerifyEssence())
				}, func(out []byte) bool { return bytes.HasSuffix(out, c.data[frontTagLen(c.data):]) }},
			}
			for name, p := range plans {
				plan, err := p.prepare()
				if err != nil {
					t.Fatalf("%s: prepare: %v", name, err)
				}
				var w writerTo
				if _, _, err := plan.Execute(context.Background(), wl.WriteTo(&w, wl.BytesSource(c.data))); err != nil {
					t.Errorf("%s under the essence check: %v", name, err)
					continue
				}
				if !p.want(w.b) {
					t.Errorf("%s did not leave the bytes expected", name)
				}
				if !essenceOf(t, w.b).Equal(essence) {
					t.Errorf("%s changed the digest", name)
				}
			}
		})
	}
}

// frontTagLen is the length of the ID3v2 tag that opens b, its header's sync-safe size plus the
// header, or 0 when none does.
func frontTagLen(b []byte) int {
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return 0
	}
	return 10 + (int(b[6])<<21 | int(b[7])<<14 | int(b[8])<<7 | int(b[9]))
}
