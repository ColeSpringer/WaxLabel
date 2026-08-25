package main

import (
	"encoding/json"
	"fmt"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// copyReportPath runs "copy src dst" and returns the parsed report plus the written destination
// path, so a follow-up diff can run against the same bytes copy graded.
func copyReportPath(t *testing.T, src, dstFixture string) (jsonCopy, string) {
	t.Helper()
	dst := copyFixture(t, dstFixture)
	out, _, code := runCLI(t, "--json", "copy", src, dst)
	if code != 0 {
		t.Fatalf("copy %s -> %s exit = %d, want 0\n%s", src, dstFixture, code, out)
	}
	var jc jsonCopy
	if err := json.Unmarshal([]byte(out), &jc); err != nil {
		t.Fatalf("copy JSON: %v\n%s", err, out)
	}
	return jc, dst
}

// fieldGrade returns the transfer report's disposition for a field key, and whether it was found.
func fieldGrade(jc jsonCopy, key string) (string, bool) {
	for _, it := range jc.Transfer {
		if it.Kind == "field" && it.Key == key {
			return it.Disposition, true
		}
	}
	return "", false
}

// TestCanonicalCopyDiffAgreement is the standing guard against copy/diff drift on the canonical
// keys a format canonicalizes or drops. For every {key} x {value} x {format-pair}, it authors the
// value on the source, copies it into the destination, and asserts copy's grade and diff's verdict
// agree: a field copy grades losslessly "carried" must read back diff-identical, and one it grades
// "dropped"/"lossy" must show a diff change. copy's grade, diff's fold, and the MP4 writer each keep
// their own notion of what a format canonicalizes; this single matrix would have caught all three of
// the numeric-fold, MEDIATYPE, and lenient-split drifts at once.
func TestCanonicalCopyDiffAgreement(t *testing.T) {
	t.Parallel()
	// ITUNESADVISORY and BPM ride the same matrix: both are MP4-canonical integer atoms
	// (rtng, tmpo). "174.0" is BPM's all-zero-fraction spelling, which tmpo stores as the
	// same whole number: carried and diff-folded for BPM, dropped (a decimal) everywhere
	// else. "174.99" is a genuine fraction: tmpo rounds it with a coercion warning, so BPM
	// grades lossy on the MP4 legs and diff reports the change - agreement holds from the
	// lossy side. "+3" is dropped on the unsigned keys (ParseUint) and never diff-folded
	// there, while the signed trkn slots store and fold it.
	keys := []tag.Key{tag.TrackNumber, tag.TrackTotal, tag.DiscNumber, tag.DiscTotal,
		tag.MediaType, tag.ITunesAdvisory, tag.BPM}
	values := []string{"01", "+3", "007", "1/2/3", "3/abc", "5", "128", "174.0", "174.99"}
	pairs := []struct{ name, src, dst string }{
		{"text-text", notagsFLAC, notagsFLAC}, // FLAC -> FLAC (verbatim both sides)
		{"text-mp4", notagsFLAC, notagsM4A},   // FLAC -> M4A (MP4 canonicalizes/drops)
		{"mp4-mp4", notagsM4A, notagsM4A},     // M4A -> M4A
	}
	for _, pr := range pairs {
		for _, key := range keys {
			for _, val := range values {
				pr, key, val := pr, key, val
				t.Run(fmt.Sprintf("%s/%s/%s", pr.name, key, val), func(t *testing.T) {
					t.Parallel()
					src := buildTransferSource(t, pr.src, func(e *wl.Editor) *wl.Editor {
						return e.Set(key, val)
					})
					// Only meaningful when the source format actually stored the value; an MP4
					// source drops an unstorable value before it can enter the copy/diff pipeline,
					// so there is nothing to grade or diff.
					if len(tagValues(dumpJSON(t, src), string(key))) == 0 {
						t.Skipf("source %s did not store %s=%q (nothing to copy/diff)", pr.name, key, val)
					}

					jc, dst := copyReportPath(t, src, pr.dst)
					grade, ok := fieldGrade(jc, string(key))
					if !ok {
						t.Fatalf("copy report has no field item for %s though the source holds it", key)
					}

					dout, _, dcode := runCLI(t, "--json", "diff", src, dst)
					if dcode > 1 {
						t.Fatalf("diff --json exit = %d (>1 is an error)\n%s", dcode, dout)
					}
					var jd jsonDiff
					if err := json.Unmarshal([]byte(dout), &jd); err != nil {
						t.Fatalf("diff JSON: %v\n%s", err, dout)
					}

					carried := grade == "carried"
					changed := hasTagChange(jd, string(key))
					// A losslessly carried field must read back diff-identical; a dropped or lossy
					// one must show a diff change. Any other combination means copy and diff
					// disagree on what this format did to the value.
					if carried == changed {
						t.Errorf("copy grade %q and diff change=%v disagree for %s=%q (%s): a carried field must read diff-identical, a dropped/lossy one must show a change",
							grade, changed, key, val, pr.name)
					}
				})
			}
		}
	}
}

// TestCompilationBooleanCopyDiffAgreement is the copy/diff guard for the COMPILATION boolean:
// because every format now normalizes a recognized boolean word to "1"/"0" on write, a copy of it
// across FLAC and M4A grades carried and diff reports no change, so the two agree. Before the
// normalization, FLAC kept the literal "true" while M4A stored "1", so copy graded carried yet diff
// reported "true -> 1" - the drift this pins shut.
func TestCompilationBooleanCopyDiffAgreement(t *testing.T) {
	t.Parallel()
	pairs := []struct{ name, src, dst string }{
		{"flac-mp4", notagsFLAC, notagsM4A},
		{"mp4-flac", notagsM4A, notagsFLAC},
	}
	runBooleanCopyDiffAgreement(t, tag.Compilation, pairs)
}

// TestGaplessBooleanCopyDiffAgreement clones the boolean guard for ITUNESGAPLESS across the
// MP3 and Matroska legs. Its ID3 home is a TXXX user frame, the first boolean key stored
// there, so this locks the TXXX-branch canonicalization: without it MP3 would keep the
// literal "yes" while FLAC stores "1", and copy would grade carried what diff reports
// changed. The MKA legs lock the same canonicalization in the Matroska SimpleTag emit.
func TestGaplessBooleanCopyDiffAgreement(t *testing.T) {
	t.Parallel()
	pairs := []struct{ name, src, dst string }{
		{"flac-mp3", notagsFLAC, notagsMP3},
		{"mp3-flac", notagsMP3, notagsFLAC},
		{"flac-mka", notagsFLAC, notagsMKA},
		{"mka-flac", notagsMKA, notagsFLAC},
	}
	runBooleanCopyDiffAgreement(t, tag.ITunesGapless, pairs)
}

// runBooleanCopyDiffAgreement is the shared body: every recognized boolean word copies
// carried and diffs identical across each format pair.
func runBooleanCopyDiffAgreement(t *testing.T, key tag.Key, pairs []struct{ name, src, dst string }) {
	for _, val := range []string{"true", "yes", "1", "false", "no", "0"} {
		for _, pr := range pairs {
			val, pr := val, pr
			t.Run(pr.name+"/"+val, func(t *testing.T) {
				t.Parallel()
				src := buildTransferSource(t, pr.src, func(e *wl.Editor) *wl.Editor {
					return e.Set(key, val)
				})
				if len(tagValues(dumpJSON(t, src), string(key))) == 0 {
					t.Skipf("source %s did not store %s=%q", pr.name, key, val)
				}
				jc, dst := copyReportPath(t, src, pr.dst)
				grade, ok := fieldGrade(jc, string(key))
				if !ok {
					t.Fatalf("copy report has no %s field though the source holds it", key)
				}
				dout, _, dcode := runCLI(t, "--json", "diff", src, dst)
				if dcode > 1 {
					t.Fatalf("diff --json exit = %d (>1 is an error)\n%s", dcode, dout)
				}
				var jd jsonDiff
				if err := json.Unmarshal([]byte(dout), &jd); err != nil {
					t.Fatalf("diff JSON: %v\n%s", err, dout)
				}
				if grade != "carried" {
					t.Errorf("%s=%q (%s): copy grade = %q, want carried (a boolean word normalizes losslessly)", key, val, pr.name, grade)
				}
				if hasTagChange(jd, string(key)) {
					t.Errorf("%s=%q (%s): diff reports a change, want none (copy and diff must agree)", key, val, pr.name)
				}
			})
		}
	}
}
