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
		Long: "Compare the canonical tags, pictures, and chapters of two files and\n" +
			"report what was added, removed, or changed going from <a> to <b>. The\n" +
			"exit code follows diff(1): 0 if the metadata is identical, 1 if it\n" +
			"differs, and 2 or more on error. With --quiet nothing is printed and\n" +
			"only the exit code is set. One operand may be \"-\" to read it from\n" +
			"standard input.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if args[0] == stdinArg && args[1] == stdinArg {
				return usagef("only one operand may be read from standard input (%q)", stdinArg)
			}
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), args)
			if err != nil {
				return err
			}
			defer cleanup()
			aDoc, err := wl.ParseFile(ctx, realOf(args[0]))
			if err != nil {
				return err
			}
			bDoc, err := wl.ParseFile(ctx, realOf(args[1]))
			if err != nil {
				return err
			}

			d := computeDiff(aDoc, bDoc)
			if !quiet {
				out := cmd.OutOrStdout()
				if jsonMode(cmd) {
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
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "print nothing; report the result through the exit code only")
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
}

// identical reports whether the two files carry the same canonical metadata.
func (d diffResult) identical() bool {
	return len(d.tags) == 0 && !d.picsDiffer && !d.chapsDiffer
}

// computeDiff compares the canonical tags, pictures, and chapters of a and b,
// reporting the delta from a to b (a is the left/old side, b the right/new side).
func computeDiff(a, b *wl.Document) diffResult {
	pa, pb := a.Pictures(), b.Pictures()
	ca, cb := a.Chapters(), b.Chapters()
	return diffResult{
		tags:        tag.Diff(a.Tags(), b.Tags()),
		picsA:       len(pa),
		picsB:       len(pb),
		picsDiffer:  !wl.EqualPictures(pa, pb),
		chapsA:      len(ca),
		chapsB:      len(cb),
		chapsDiffer: !wl.EqualChapters(ca, cb),
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

// jsonDiff is the machine-readable canonical-metadata delta.
type jsonDiff struct {
	SchemaVersion int            `json:"schemaVersion"`
	FileA         string         `json:"a"`
	FileB         string         `json:"b"`
	Identical     bool           `json:"identical"`
	Tags          []jsonDiffTag  `json:"tags"`
	Pictures      *jsonDiffCount `json:"pictures,omitempty"`
	Chapters      *jsonDiffCount `json:"chapters,omitempty"`
}

type jsonDiffTag struct {
	Key    string   `json:"key"`
	Change string   `json:"change"`
	A      []string `json:"a,omitempty"`
	B      []string `json:"b,omitempty"`
}

type jsonDiffCount struct {
	A int `json:"a"`
	B int `json:"b"`
}

func toJSONDiff(a, b string, d diffResult) jsonDiff {
	jd := jsonDiff{
		SchemaVersion: schemaVersion,
		FileA:         a,
		FileB:         b,
		Identical:     d.identical(),
		Tags:          []jsonDiffTag{},
	}
	for _, t := range d.tags {
		jd.Tags = append(jd.Tags, jsonDiffTag{Key: string(t.Key), Change: t.Kind.String(), A: t.Old, B: t.New})
	}
	if d.picsDiffer {
		jd.Pictures = &jsonDiffCount{A: d.picsA, B: d.picsB}
	}
	if d.chapsDiffer {
		jd.Chapters = &jsonDiffCount{A: d.chapsA, B: d.chapsB}
	}
	return jd
}
