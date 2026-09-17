package main

import (
	"errors"
	"fmt"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// Sentinel for metadata mismatch: exit 1 (diff convention). Already-rendered so no error line over diff output.
var errFilesDiffer = errors.New("files differ")

// newDiffCmd builds diff: compare canonical metadata. Exit 0/1/≥2 like diff(1).
func newDiffCmd() *cobra.Command {
	var quiet bool
	cmd := &cobra.Command{
		Use:   "diff <a> <b>",
		Short: "Compare two files' canonical metadata",
		Example: "  waxlabel diff old.flac new.flac\n" +
			"  waxlabel diff --quiet a.mp3 b.mp3",
		Long: "Compare the canonical tags, pictures, chapters, and synced lyrics of two\n" +
			"files and report what was added, removed, or changed going from <a> to <b>.\n" +
			"The exit code follows diff(1): 0 if the metadata is identical, 1 if it\n" +
			"differs, and 2 or more on error. With --quiet nothing is printed and\n" +
			"only the exit code is set. One operand may be \"-\" to read it from\n" +
			"standard input.\n\n" +
			"A track/disc number or total is compared numerically (so \"007\" equals\n" +
			"\"7\") only when at least one side is an MP4, the one format whose integer\n" +
			"atoms canonicalize the value; between two text formats the literal strings\n" +
			"are compared. A slash inside a *total* field (TRACKTOTAL=1/2) is out of\n" +
			"spec: FLAC/Ogg keep it as literal text while MP3/M4A drop it, so two such\n" +
			"files can differ on that field by design.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// --json overrides --quiet (same as verify/set).
			quiet = quiet && !jsonMode(cmd)
			if err := checkEmptyOperands(args...); err != nil {
				return err
			}
			if args[0] == stdinArg && args[1] == stdinArg {
				return usagef("only one operand may be read from standard input (%q)", stdinArg)
			}
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), maxSizeFlag(cmd), args)
			if err != nil {
				return err
			}
			defer cleanup()
			if err := checkRegularInputs(realOf, true, args...); err != nil {
				return err
			}
			asJSON := jsonMode(cmd)
			errOut := cmd.ErrOrStderr()
			// Human mode: per-file error prefix. JSON unchanged (dispatch envelope).
			parse := func(arg string) (*wl.Document, error) {
				doc, err := parseInput(ctx, realOf(arg), arg)
				if err != nil {
					if asJSON {
						return nil, err
					}
					perFileError(errOut, arg, err)
					return nil, alreadyRendered(err)
				}
				return doc, nil
			}
			aDoc, err := parse(args[0])
			if err != nil {
				return err
			}
			bDoc, err := parse(args[1])
			if err != nil {
				return err
			}

			d := computeDiff(aDoc, bDoc)
			if !quiet {
				out := cmd.OutOrStdout()
				if asJSON {
					if err := writeJSON(out, toJSONDiff(args[0], args[1], d)); err != nil {
						return err
					}
				} else {
					renderDiff(out, args[0], args[1], d)
				}
			}
			if d.identical() {
				return nil
			}
			// Already-rendered: diff (or silence under --quiet) is output; exit code only.
			return alreadyRendered(errFilesDiffer)
		},
	}
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "print nothing; report the result through the exit code only (--json overrides this and still emits the object)")
	return cmd
}

// Canonical delta a→b. Tags use tag.Change; pictures/chapters have count deltas.
type diffResult struct {
	tags         []tag.Change
	picsA, picsB int
	picsDiffer   bool
	chapsA       int
	chapsB       int
	chapsDiffer  bool
	syncedA      int
	syncedB      int
	syncedDiffer bool
	// Ogg Opus header output gain (Q7.8). Not a tag, but set --output-gain edits it.
	gainA, gainB int
	gainDiffer   bool
}

// identical reports whether the two files carry the same canonical metadata.
func (d diffResult) identical() bool {
	return len(d.tags) == 0 && !d.picsDiffer && !d.chapsDiffer && !d.syncedDiffer && !d.gainDiffer
}

// Delta from a to b (a=left/old, b=right/new).
func computeDiff(a, b *wl.Document) diffResult {
	pa, pb := a.Pictures(), b.Pictures()
	ca, cb := a.Chapters(), b.Chapters()
	sa, sb := a.SyncedLyrics(), b.SyncedLyrics()
	return diffResult{
		tags:       numericAwareTagDiff(a.Tags(), b.Tags(), a.Format(), b.Format()),
		picsA:      len(pa),
		picsB:      len(pb),
		picsDiffer: !wl.EqualPictures(pa, pb),
		chapsA:     len(ca),
		chapsB:     len(cb),
		// Ignore reconstructable chapter ends (gapless interior, run-to-EOF). copy refills run-to-EOF to dest EOF.
		// Properties is Document.Properties(), not codec-path field.
		chapsDiffer:  !wl.EqualChaptersModuloEnds(ca, cb, a.Properties().Duration(), b.Properties().Duration()),
		syncedA:      len(sa),
		syncedB:      len(sb),
		syncedDiffer: !wl.EqualSyncedLyrics(sa, sb),
		gainA:        a.Properties().First().OutputGain,
		gainB:        b.Properties().First().OutputGain,
		gainDiffer:   a.Properties().First().OutputGain != b.Properties().First().OutputGain,
	}
}

// tag.Diff plus MP4 canonical-key fold when one side is MP4.
// Leading '+'/zeros on IsMP4CanonicalKey keys not reported as change (trkn/disk/stik/rtng/©mvi/©mvc/tmpo).
// Matches copy Carried grading; same idea as chapter end normalization.
// BPM: fold all-zero fraction ("174.0" vs "174"); genuine fractions still report (warned tmpo rounding).
// Scope: at least one MP4 side. Text-to-text "01" vs "1" is a real difference.
// Not applied to other numeric keys (play count, etc.). Added/removed keys unaffected.
func numericAwareTagDiff(a, b tag.TagSet, fa, fb wl.Format) []tag.Change {
	changes := tag.Diff(a, b)
	if fa != wl.FormatMP4 && fb != wl.FormatMP4 {
		return changes // no MP4 side; any numeric delta is genuine
	}
	out := changes[:0]
	for _, c := range changes {
		if c.Kind == tag.ChangeChanged && tag.IsMP4CanonicalKey(c.Key) && tag.NumericValuesEqual(c.Key, c.Old, c.New) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Canonical-metadata delta with diff-style -/+/~ markers.
func renderDiff(w io.Writer, a, b string, d diffResult) {
	// Escape/relabel paths for headers (same as dump/lint/caps).
	na, nb := displayName(a), displayName(b)
	if d.identical() {
		fmt.Fprintf(w, "%s and %s: identical metadata\n", na, nb)
		return
	}
	fmt.Fprintf(w, "%s -> %s\n", na, nb)
	for _, t := range d.tags {
		renderChangeLine(w, "  ", t)
	}
	renderCountDelta(w, "pictures", d.picsDiffer, d.picsA, d.picsB)
	renderCountDelta(w, "chapters", d.chapsDiffer, d.chapsA, d.chapsB)
	renderCountDelta(w, "synced lyrics", d.syncedDiffer, d.syncedA, d.syncedB)
	if d.gainDiffer {
		fmt.Fprintf(w, "  output gain: %s -> %s\n", wl.OutputGainDB(d.gainA), wl.OutputGainDB(d.gainB))
	}
}

// One tag change at indent. Delegates to tag.Change.String (shared with write-plan preview); sanitization in one place.
func renderChangeLine(w io.Writer, indent string, c tag.Change) {
	fmt.Fprintf(w, "%s%s\n", indent, c.String())
}

// Set count delta. Equal count but different contents: say "contents differ", not "N -> N".
func renderCountDelta(w io.Writer, label string, differ bool, a, b int) {
	if !differ {
		return
	}
	if a == b {
		fmt.Fprintf(w, "  %s: %d (contents differ)\n", label, a)
		return
	}
	fmt.Fprintf(w, "  %s: %d -> %d\n", label, a, b)
}

// Machine-readable delta. Count objects always present with changed flag.
// Avoids inferring from presence or a!=b (equal-count content change vs no-op).
type jsonDiff struct {
	SchemaVersion int           `json:"schemaVersion"`
	FileA         string        `json:"a"`
	FileB         string        `json:"b"`
	Identical     bool          `json:"identical"`
	Tags          []jsonDiffTag `json:"tags"`
	Pictures      jsonDiffCount `json:"pictures"`
	Chapters      jsonDiffCount `json:"chapters"`
	SyncedLyrics  jsonDiffCount `json:"syncedLyrics"`
	// Omitted unless gain differs; most formats have none.
	OutputGain *jsonDiffValue `json:"outputGain,omitempty"`
}

// Single-value delta; counterpart to jsonDiffCount.
type jsonDiffValue struct {
	A string `json:"a"`
	B string `json:"b"`
}

type jsonDiffTag struct {
	Key    string   `json:"key"`
	Change string   `json:"change"`
	A      []string `json:"a,omitempty"`
	B      []string `json:"b,omitempty"`
}

// Set before/after count plus changed; disambiguates equal-count content change from no-op.
type jsonDiffCount struct {
	A       int  `json:"a"`
	B       int  `json:"b"`
	Changed bool `json:"changed"`
}

func toJSONDiff(a, b string, d diffResult) jsonDiff {
	jd := jsonDiff{
		SchemaVersion: schemaVersion,
		FileA:         jsonFileName(a),
		FileB:         jsonFileName(b),
		Identical:     d.identical(),
		Tags:          []jsonDiffTag{},
		Pictures:      jsonDiffCount{A: d.picsA, B: d.picsB, Changed: d.picsDiffer},
		Chapters:      jsonDiffCount{A: d.chapsA, B: d.chapsB, Changed: d.chapsDiffer},
		SyncedLyrics:  jsonDiffCount{A: d.syncedA, B: d.syncedB, Changed: d.syncedDiffer},
	}
	if d.gainDiffer {
		jd.OutputGain = &jsonDiffValue{A: wl.OutputGainDB(d.gainA), B: wl.OutputGainDB(d.gainB)}
	}
	for _, t := range d.tags {
		jd.Tags = append(jd.Tags, jsonDiffTag{Key: string(t.Key), Change: t.Kind.String(), A: t.Old, B: t.New})
	}
	return jd
}
