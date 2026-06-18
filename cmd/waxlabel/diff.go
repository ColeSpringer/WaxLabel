package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	wl "github.com/colespringer/waxlabel"
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
			"only the exit code is set.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			aDoc, err := wl.ParseFile(ctx, args[0])
			if err != nil {
				return err
			}
			bDoc, err := wl.ParseFile(ctx, args[1])
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

// tagChange names how one key changed between the two files.
type tagChange struct {
	key    string
	change string // "added" (only in b), "removed" (only in a), "changed"
	a, b   []string
}

// diffResult is the canonical-metadata delta from file a to file b.
type diffResult struct {
	tags         []tagChange
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
	ta, tb := a.Tags(), b.Tags()
	var tags []tagChange
	// a's keys first, in a's order: removed or changed.
	for _, k := range ta.Keys() {
		av, _ := ta.Get(k)
		if bv, ok := tb.Get(k); ok {
			if !slices.Equal(av, bv) {
				tags = append(tags, tagChange{key: string(k), change: "changed", a: av, b: bv})
			}
		} else {
			tags = append(tags, tagChange{key: string(k), change: "removed", a: av})
		}
	}
	// keys only in b: added.
	for _, k := range tb.Keys() {
		if !ta.Has(k) {
			bv, _ := tb.Get(k)
			tags = append(tags, tagChange{key: string(k), change: "added", b: bv})
		}
	}

	pa, pb := a.Pictures(), b.Pictures()
	ca, cb := a.Chapters(), b.Chapters()
	return diffResult{
		tags:        tags,
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
	if d.identical() {
		fmt.Fprintf(w, "%s and %s: identical metadata\n", a, b)
		return
	}
	fmt.Fprintf(w, "%s -> %s\n", a, b)
	for _, t := range d.tags {
		switch t.change {
		case "removed":
			fmt.Fprintf(w, "  - %s: %s\n", t.key, joinValues(t.a))
		case "added":
			fmt.Fprintf(w, "  + %s: %s\n", t.key, joinValues(t.b))
		case "changed":
			fmt.Fprintf(w, "  ~ %s: %s -> %s\n", t.key, joinValues(t.a), joinValues(t.b))
		}
	}
	renderCountDelta(w, "pictures", d.picsDiffer, d.picsA, d.picsB)
	renderCountDelta(w, "chapters", d.chapsDiffer, d.chapsA, d.chapsB)
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

// joinValues renders a key's values for the text diff, separating multiple values
// with " | " so a value containing a comma is not mistaken for two.
func joinValues(vals []string) string {
	if len(vals) == 0 {
		return "(present, no value)"
	}
	return strings.Join(vals, " | ")
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
		jd.Tags = append(jd.Tags, jsonDiffTag{Key: t.key, Change: t.change, A: t.a, B: t.b})
	}
	if d.picsDiffer {
		jd.Pictures = &jsonDiffCount{A: d.picsA, B: d.picsB}
	}
	if d.chapsDiffer {
		jd.Chapters = &jsonDiffCount{A: d.chapsA, B: d.chapsB}
	}
	return jd
}
