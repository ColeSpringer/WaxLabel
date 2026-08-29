package waxlabel_test

import (
	"slices"
	"strings"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// legacyOnlyMP3 builds an MP3 whose ID3v2 carries TITLE and ARTIST while its ID3v1 trailer
// is the only home for ALBUM, RECORDINGDATE, COMMENT and GENRE.
func legacyOnlyMP3(t *testing.T) []byte {
	t.Helper()
	data := id3v2(3, textFrame(3, "TIT2", "V2 Title"), textFrame(3, "TPE1", "V2 Artist"))
	data = append(data, mp3Audio(t)...)
	return append(data, id3v1("", "", "Legacy Album", "1999", "legacy comment", 17)...)
}

// warningFor returns the plan warning with the given code, and whether it was present. It is
// reportHasWarning's counterpart for a test that asserts on the warning's keys or message
// rather than only its presence.
func warningFor(plan *wl.Plan, code wl.WarningCode) (wl.Warning, bool) {
	for _, w := range plan.Report().Warnings {
		if w.Code == code {
			return w, true
		}
	}
	return wl.Warning{}, false
}

// TestLegacyStripWarnsAboutWhatItDestroys is the contract doc.go freezes: unaffected data,
// legacy tags included, is preserved and warned, never stripped silently. An explicit
// LegacyStrip does destroy legacy-only values, so it must say which.
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
	// And the values really are gone, which is what the warning is for.
	if v, ok := mustParseBytes(t, applyToBytes(t, data, plan)).Get(tag.Album); ok {
		t.Errorf("ALBUM = %v, want gone: the strip destroyed it, which is what the warning reports", v)
	}
}

// TestLegacyStripJudgesTheEditedTags is the reason the rule takes an authority argument:
// a strip that is also writing the value the legacy container held loses nothing, so
// naming that key would be a false alarm - and a copy sets most of the source's keys on
// the destination editor, which would make nearly every key a false alarm.
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

// TestLegacyStripIgnoresKeysTheEditRemoved: the rule tests the edited tags, so a key the edit
// CLEARS looks absent from the authority and read as "held only in the legacy container".
// Nothing is lost there - the user asked for the removal, and the canonical set carried the
// same value - so reporting it is a false alarm that --strict turns into a refused write.
func TestLegacyStripIgnoresKeysTheEditRemoved(t *testing.T) {
	// Both containers carry the same TITLE, so clearing it loses nothing either strips.
	data := id3v2(3, textFrame(3, "TIT2", "Same Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, id3v1("Same Title", "", "", "", "", 255)...)
	plan, err := mustParseBytes(t, data).Edit().Clear(tag.Title).
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := warningFor(plan, wl.WarnLegacyStripDropped); ok {
		t.Errorf("clearing a key the canonical set also held was reported as a strip loss: %v", w.Keys)
	}
}

// TestLegacyStripStillReportsGenuinelyLegacyOnly is the companion, so the filter above cannot
// silence the case the warning exists for.
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

// TestLegacyStripSilentWhenNothingIsLost: a legacy container fully redundant with the
// canonical set loses nothing on a strip, so there is nothing to warn about.
func TestLegacyStripSilentWhenNothingIsLost(t *testing.T) {
	data := id3v2(3, textFrame(3, "TIT2", "Same Title"))
	data = append(data, mp3Audio(t)...)
	data = append(data, id3v1("Same Title", "", "", "", "", 255)...)
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Artist, "A").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warningFor(plan, wl.WarnLegacyStripDropped); ok {
		t.Errorf("a redundant legacy container lost nothing but warned: %v", plan.Report().Warnings)
	}
}

// TestLintFixNeverTripsLegacyStripWarning pins the complement the two gates form: PlanLintFix
// adds LegacyStrip only when neither loss predicate holds, computed from the same primitives
// against the same document, so the safe fix can never destroy what this warning reports.
func TestLintFixNeverTripsLegacyStripWarning(t *testing.T) {
	for _, tc := range []struct {
		name string
		data func(*testing.T) []byte
	}{
		{"legacy-only values", legacyOnlyMP3},
		{"redundant container", func(t *testing.T) []byte {
			data := id3v2(3, textFrame(3, "TIT2", "Same Title"))
			data = append(data, mp3Audio(t)...)
			return append(data, id3v1("Same Title", "", "", "", "", 255)...)
		}},
		{"legacy container echoing a stamped ENCODER", func(t *testing.T) []byte {
			// PlanLintFix clears the stamped ENCODER, so the ID3v1 comment carrying the same
			// stamp reads as legacy-only against the edited set. Nothing is lost: the canonical
			// set held it too.
			data := id3v2(3, textFrame(3, "TIT2", "Song"), textFrame(3, "TSSE", "Lavf62.3.100"))
			data = append(data, mp3Audio(t)...)
			return append(data, id3v1("Song", "", "", "", "", 255)...)
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

// TestLegacyStripSilentOnWAV pins the exclusion by construction: WAV and AIFF reuse
// LegacyStrip to mean "consolidate into the id3 chunk", where the values move rather than
// die, and they never mark a family Legacy.
func TestLegacyStripSilentOnWAV(t *testing.T) {
	data := wavFile(wavFmtPCM(), wavInfo([2]string{"INAM", "Song"}, [2]string{"ICOP", "ACME"}), wavData(400))
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

// TestWAVLegacyStripWarnsAboutUnmappedItems closes the adjacent hole on the same flag: WAV
// reuses LegacyStrip to mean "consolidate LIST/INFO into the id3 chunk", but an item with no
// canonical key (IENG, ISBJ) has no frame to move into, so the chunk drop destroys it.
func TestWAVLegacyStripWarnsAboutUnmappedItems(t *testing.T) {
	data := wavFile(wavFmtPCM(),
		wavInfo([2]string{"INAM", "Song"}, [2]string{"IENG", "Alice"}, [2]string{"ISBJ", "Subj"}), wavData(400))
	plan, err := mustParseBytes(t, data).Edit().Set(tag.Title, "New").
		Prepare(wl.WithLegacyPolicy(wl.LegacyStrip))
	if err != nil {
		t.Fatal(err)
	}
	w, ok := warningFor(plan, wl.WarnLegacyStripDropped)
	if !ok {
		t.Fatalf("unmapped INFO items were destroyed silently; warnings = %v", plan.Report().Warnings)
	}
	for _, id := range []string{"IENG", "ISBJ"} {
		if !strings.Contains(w.Message, id) {
			t.Errorf("warning does not name %s: %q", id, w.Message)
		}
	}
}
