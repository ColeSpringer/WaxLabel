package main

import (
	"errors"
	"fmt"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// errFilesDiffer is the sentinel diff returns when two files' canonical metadata
// differs. It is plain (unclassified), so it maps to exit code 1 - the
// diff(1)/`git diff --exit-code` convention of "1 = differences found" - and it is
// returned already-rendered so no error line is printed over the diff output.
var errFilesDiffer = errors.New("files differ")

// newDiffCmd builds the "diff" command, which compares two files' canonical
// metadata. Its exit code follows the diff(1) convention: 0 when the metadata is
// identical, 1 when it differs, and >=2 for an actual error.
func newDiffCmd() *cobra.Command {
	var quiet bool
	cmd := &cobra.Command{
		Use:   "diff <a> <b>",
		Short: "Compare two files' canonical metadata",
		Example: "  waxlabel diff old.flac new.flac\n" +
			"  waxlabel diff --quiet a.mp3 b.mp3",
		Long: "Compare the canonical tags, pictures, and chapters of two files and\n" +
			"report what was added, removed, or changed going from <a> to <b>. The\n" +
			"exit code follows diff(1): 0 if the metadata is identical, 1 if it\n" +
			"differs, and 2 or more on error. With --quiet nothing is printed and\n" +
			"only the exit code is set. One operand may be \"-\" to read it from\n" +
			"standard input.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// --json has a fixed shape, so it overrides --quiet: a quiet JSON diff still
			// emits the documented object (the exit code carries the verdict either way).
			// This mirrors verify and set, so the flag pair behaves the same everywhere.
			quiet = quiet && !jsonMode(cmd)
			if err := checkEmptyOperands(args...); err != nil {
				return err
			}
			if args[0] == stdinArg && args[1] == stdinArg {
				return usagef("only one operand may be read from standard input (%q)", stdinArg)
			}
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), args)
			if err != nil {
				return err
			}
			defer cleanup()
			if err := checkRegularInputs(realOf, true, args...); err != nil {
				return err
			}
			asJSON := jsonMode(cmd)
			errOut := cmd.ErrOrStderr()
			// On a parse failure, prefix the per-file "waxlabel: <path>: <reason>" line the
			// other commands print in human mode, instead of the classifier's bare reason
			// without the operand. JSON is unchanged: the raw error returns to dispatch,
			// which emits the not-found/invalid-data envelope scripts read.
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
			// Already-rendered: the diff (or nothing, under --quiet) is the output;
			// dispatch keeps the exit code without printing an error line.
			return alreadyRendered(errFilesDiffer)
		},
	}
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "print nothing; report the result through the exit code only (--json overrides this and still emits the object)")
	return cmd
}

// diffResult is the canonical-metadata delta from file a to file b. The tag
// delta is a shared [tag.Change] list (the same primitive the write-plan preview
// uses); the picture and chapter sets keep their own count-deltas.
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
}

// identical reports whether the two files carry the same canonical metadata.
func (d diffResult) identical() bool {
	return len(d.tags) == 0 && !d.picsDiffer && !d.chapsDiffer && !d.syncedDiffer
}

// computeDiff compares the canonical tags, pictures, chapters, and synced lyrics of a and
// b, reporting the delta from a to b (a is the left/old side, b the right/new side).
func computeDiff(a, b *wl.Document) diffResult {
	pa, pb := a.Pictures(), b.Pictures()
	ca, cb := a.Chapters(), b.Chapters()
	sa, sb := a.SyncedLyrics(), b.SyncedLyrics()
	return diffResult{
		tags:       tag.Diff(a.Tags(), b.Tags()),
		picsA:      len(pa),
		picsB:      len(pb),
		picsDiffer: !wl.EqualPictures(pa, pb),
		chapsA:     len(ca),
		chapsB:     len(cb),
		// Duration-aware so diff agrees with how copy grades chapters: a reconstructable end
		// difference (a gapless interior end, or a trailing end that runs to EOF) is not a
		// difference. Properties is a method on the Document here (not the codec-path field).
		chapsDiffer:  !wl.EqualChaptersModuloEnds(ca, cb, a.Properties().Duration(), b.Properties().Duration()),
		syncedA:      len(sa),
		syncedB:      len(sb),
		syncedDiffer: !wl.EqualSyncedLyrics(sa, sb),
	}
}

// renderDiff prints the canonical-metadata delta with diff-style -/+/~ markers.
func renderDiff(w io.Writer, a, b string, d diffResult) {
	// Escape and stdin-relabel the file paths for the single-line headers (consistent
	// with dump/lint/caps), so a hostile filename from a glob cannot forge a line.
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
}

// renderChangeLine prints one tag change with diff-style -/+/~ markers at the
// given indent. It delegates to [tag.Change.String] - the single change-line
// formatter shared by the diff command, the write-plan change preview, and
// library consumers - so their formatting cannot drift and the untrusted change
// values are sanitized for the terminal in exactly one place.
func renderChangeLine(w io.Writer, indent string, c tag.Change) {
	fmt.Fprintf(w, "%s%s\n", indent, c.String())
}

// renderCountDelta prints a picture/chapter set delta. When the counts are equal
// but the contents differ (e.g. a replaced cover or a retitled chapter), "N -> N"
// would read as a no-op, so it says the contents differ explicitly.
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

// jsonDiff is the machine-readable canonical-metadata delta. The three count objects are
// always present (like the tags array), each carrying a `changed` discriminator, so a consumer
// reads `chapters.changed` rather than inferring a difference from key presence or a != b -
// the latter cannot distinguish "contents differ at equal count" (a replaced cover, a retitled
// chapter) from a no-op.
type jsonDiff struct {
	SchemaVersion int           `json:"schemaVersion"`
	FileA         string        `json:"a"`
	FileB         string        `json:"b"`
	Identical     bool          `json:"identical"`
	Tags          []jsonDiffTag `json:"tags"`
	Pictures      jsonDiffCount `json:"pictures"`
	Chapters      jsonDiffCount `json:"chapters"`
	SyncedLyrics  jsonDiffCount `json:"syncedLyrics"`
}

type jsonDiffTag struct {
	Key    string   `json:"key"`
	Change string   `json:"change"`
	A      []string `json:"a,omitempty"`
	B      []string `json:"b,omitempty"`
}

// jsonDiffCount is a set's before/after count plus whether the set changed at all - the flag
// disambiguates an equal-count contents change (Changed true, A == B) from no difference.
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
	for _, t := range d.tags {
		jd.Tags = append(jd.Tags, jsonDiffTag{Key: string(t.Key), Change: t.Kind.String(), A: t.Old, B: t.New})
	}
	return jd
}
