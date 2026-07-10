package main

import (
	"context"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// newPlanCmd builds the "plan" command, which resolves edits into a write plan
// and reports exactly what saving would do - without touching the file. It
// accepts multiple files (and directories with --recursive), previewing each.
func newPlanCmd() *cobra.Command {
	var ef editFlags
	var recursive bool
	cmd := &cobra.Command{
		Use:   "plan <file>...",
		Short: "Show what an edit would write, without writing it",
		Example: "  waxlabel plan song.flac --set TITLE=\"Hey Jude\" --add ARTIST=Beatles\n" +
			"  waxlabel plan song.flac --clear COMMENT",
		Long: "Resolve the given edits into a write plan and print exactly what saving\n" +
			"would do - the operations, the field-level changes, the size change,\n" +
			"padding, and warnings - without modifying the file. With no edits it\n" +
			"reports that the file is already up to date. The report is the same one\n" +
			"set acts on, so the two cannot disagree. Warnings describe the write plan:\n" +
			"what the write changes, downgrades, or drops. Run 'lint' on a saved file\n" +
			"to check post-write metadata cleanliness. Multiple files\n" +
			"are previewed independently; with --recursive, directory arguments are\n" +
			"walked for audio files. A single \"-\" reads the file from standard input.\n\n" +
			editPrecedenceHelp,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Reject an explicitly-empty --preset/--legacy, matching set and the unknown-
			// value rejection. plan has no no-edits guard: previewing an unedited file
			// is a valid "is it up to date" query.
			if err := rejectEmptyScalarFlags(cmd); err != nil {
				return err
			}
			ce, err := ef.compile()
			if err != nil {
				return err
			}
			asJSON := jsonMode(cmd)
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), maxSizeFlag(cmd), args)
			if err != nil {
				return err
			}
			defer cleanup()
			paths, skipped, pathErrors, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			noteNoFiles(cmd.ErrOrStderr(), paths, asJSON)
			noteSkipped(cmd.ErrOrStderr(), skipped, asJSON)
			if err := notifyInvocationNotes(cmd.ErrOrStderr(), ce, &ef, realOf, paths, pathErrors, asJSON); err != nil {
				return err
			}
			// An unquoted value with spaces (--set TITLE=Two Words) leaves a stray bare-word
			// positional that the preview would misattribute - printing a truncated change
			// (TITLE -> Two) then failing the stray word as not-found (exit 6). Since the plan
			// preview is meant to be authoritative, refuse up front (exit 2) like set does, via
			// the same shared helper. writes=false: plan never writes, so it uses the bare hint
			// without set's "; nothing was written" suffix (which would be false here).
			if err := refuseUnquotedValue(&ef, realOf, args, false); err != nil {
				return err
			}
			gate := newStrictWarningGate(ef.strict)
			pnoter := newPaddingNoter(asJSON, cmd.ErrOrStderr())
			return perFile(cmd, paths,
				guardPathErrors(pathErrors, func(ctx context.Context, path string) (*wl.Plan, error) {
					doc, plan, err := ce.prepare(ctx, realOf(path), path)
					if err != nil {
						return nil, err
					}
					// Note once per format when a padding flag does not apply to it. Gated on
					// ce.paddingFlag so the Capabilities are not built when no flag was given.
					if ce.paddingFlag {
						pnoter.note(doc.Capabilities())
					}
					if err := gate.check(plan); err != nil {
						return nil, err
					}
					return plan, nil
				}),
				func(path string, plan *wl.Plan) any { return toJSONReport(path, plan) },
				func(w io.Writer, path string, plan *wl.Plan) { renderReport(w, path, plan, ce.addPics) },
				false,
			)
		},
	}
	ef.bind(cmd)
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, previewing every audio file found (selected by file extension)")
	return markListCommand(cmd)
}

// jsonReport is the machine-readable form of a write plan, shared by plan and
// set (set embeds it). A failed element in a bulk run is emitted as the shared
// jsonErrorEntry; this struct keeps a matching Error field so a consumer can decode
// every array element into it (Error set, plan fields absent on failure; Error nil
// and plan fields populated on success). See jsonErrorEntry.
type jsonReport struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file"`
	Error         *jsonErrBody `json:"error,omitempty"`
	NoOp          bool         `json:"noOp"`
	// Changes is the canonical tag-level diff: keys added, removed, or replaced.
	// Operations is the structural write list, such as an ID3v2 frame rewrite,
	// encoder-stamp strip, or chapter-track rewrite. A fix can touch only native
	// structure, so empty Changes can still be paired with non-empty Operations.
	Changes      []jsonChange  `json:"changes"`
	Operations   []string      `json:"operations"`
	BytesBefore  int64         `json:"bytesBefore"`
	BytesAfter   int64         `json:"bytesAfter"`
	PaddingAfter int64         `json:"paddingAfter"`
	Warnings     []jsonWarning `json:"warnings"`
}

// jsonChange is one field's change in a write plan: the canonical key, how it
// changed ("added"/"removed"/"changed"), and the before/after values. It mirrors
// the shape of jsonDiffTag, naming the two sides old/new for a before/after edit. A
// picture/chapter set-count change instead carries a real integer count (and omits
// old/new), so a consumer reads the count as a number, not a stringified value.
type jsonChange struct {
	Key    string   `json:"key"`
	Change string   `json:"change"`
	Count  *int     `json:"count,omitempty"`
	Old    []string `json:"old,omitempty"`
	New    []string `json:"new,omitempty"`
}

// toJSONChanges converts a tag-change list to its JSON form. Shared by the write
// report and lint --fix so their change shape cannot drift.
func toJSONChanges(changes []tag.Change) []jsonChange {
	out := make([]jsonChange, 0, len(changes))
	for _, c := range changes {
		jc := jsonChange{Key: string(c.Key), Change: c.Kind.String()}
		if isCountChange(c.Key) {
			// A picture/chapter set-count change: emit the real integer count instead of
			// the stringified Old/New the text render uses, dropping the bogus old/new for
			// this kind (the human "changes:" block shows the per-picture detail separately).
			n := c.Count
			jc.Count = &n
		} else {
			jc.Old, jc.New = c.Old, c.New
		}
		out = append(out, jc)
	}
	return out
}

func toJSONReport(path string, plan *wl.Plan) jsonReport {
	r := plan.Report()
	var warnings []jsonWarning
	for _, x := range r.Warnings {
		warnings = append(warnings, jsonWarning{Code: x.Code.String(), Message: x.Message})
	}
	// A no-op plan stamps Operations with the shared "no changes" sentinel (core.NoOpPlan) that
	// drives the human "no changes" line; the JSON operations array is defined as the structural
	// write list (README), so a no-op writes nothing and it must serialize as [] rather than leak
	// the sentinel. Mirror the same normalization lint --fix applies.
	operations := r.Operations
	if plan.IsNoOp() {
		operations = nil
	}
	// nonNil on each collection so it serializes as [] (never null/omitted) - a
	// consumer iterating.operations[]/.warnings[] works on a clean plan too.
	return jsonReport{
		SchemaVersion: schemaVersion,
		File:          jsonFileName(path),
		NoOp:          plan.IsNoOp(),
		Changes:       toJSONChanges(plan.Changes()),
		Operations:    nonNil(operations),
		BytesBefore:   r.BytesBefore,
		BytesAfter:    r.BytesAfter,
		PaddingAfter:  r.PaddingAfter,
		Warnings:      nonNil(warnings),
	}
}
