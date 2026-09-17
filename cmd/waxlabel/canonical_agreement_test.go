package main

import (
	"encoding/json"
	"fmt"
	"testing"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
)

// copyReportPath runs copy and returns parsed report plus destination path for follow-up diff.
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

// fieldGrade returns transfer disposition for a field key and whether it was found.
func fieldGrade(jc jsonCopy, key string) (string, bool) {
	for _, it := range jc.Transfer {
		if it.Kind == "field" && it.Key == key {
			return it.Disposition, true
		}
	}
	return "", false
}

// TestCanonicalCopyDiffAgreement: copy grade and diff verdict agree on canonical keys across
// format pairs. Catches numeric-fold, MEDIATYPE, and lenient-split drift.
func TestCanonicalCopyDiffAgreement(t *testing.T) {
	t.Parallel()
	// ITUNESADVISORY and BPM: MP4 integer atoms. "174.0" whole number: carried/folded for BPM on MP4.
	// "174.99" fraction: lossy on MP4 legs. "+3" dropped on unsigned keys, folded on signed trkn slots.
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
					// Skip when source never stored the value (nothing to grade/diff).
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
					// carried => diff identical; dropped/lossy => diff changed.
					if carried == changed {
						t.Errorf("copy grade %q and diff change=%v disagree for %s=%q (%s): a carried field must read diff-identical, a dropped/lossy one must show a change",
							grade, changed, key, val, pr.name)
					}
				})
			}
		}
	}
}

// TestCompilationBooleanCopyDiffAgreement: boolean normalizes to "1"/"0"; copy carried, diff unchanged.
// Before normalization FLAC kept "true", M4A "1": copy carried but diff reported change.
func TestCompilationBooleanCopyDiffAgreement(t *testing.T) {
	t.Parallel()
	pairs := []struct{ name, src, dst string }{
		{"flac-mp4", notagsFLAC, notagsM4A},
		{"mp4-flac", notagsM4A, notagsFLAC},
	}
	runBooleanCopyDiffAgreement(t, tag.Compilation, pairs)
}

// TestGaplessBooleanCopyDiffAgreement: ITUNESGAPLESS across MP3 (TXXX) and Matroska legs.
// Without normalization MP3 kept "yes", FLAC "1": copy/diff would disagree.
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

// runBooleanCopyDiffAgreement: recognized boolean words copy carried and diff identical.
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
