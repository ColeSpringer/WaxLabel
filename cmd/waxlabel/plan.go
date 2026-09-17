package main

import (
	"context"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// newPlanCmd builds "plan": resolve edits into a write plan and report what
// saving would do, without touching the file. Multiple files (and directories
// with --recursive) are previewed independently.
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
			// Reject explicitly empty --preset/--legacy like set. No no-edits guard:
			// previewing an unedited file is a valid "up to date?" query.
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
			paths, skipped, leftovers, pathErrors, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			noteNoFiles(cmd.ErrOrStderr(), paths, asJSON)
			noteSkipped(cmd.ErrOrStderr(), skipped, asJSON)
			noteLeftovers(cmd.ErrOrStderr(), leftovers, asJSON)
			if err := notifyInvocationNotes(cmd.ErrOrStderr(), ce, &ef, realOf, paths, pathErrors, asJSON); err != nil {
				return err
			}
			// Unquoted spaces (--set TITLE=Two Words) leave a stray positional; refuse up
			// front (exit 2) like set. writes=false: bare hint, no "; nothing was written".
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
					// Note once per format when a padding flag does not apply.
					// Gated on ce.paddingFlag so Capabilities are not built otherwise.
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

// jsonReport is the machine-readable write plan (shared by plan and set).
// Failures use jsonErrorEntry; Error is kept so a mixed array decodes here.
type jsonReport struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file"`
	Error         *jsonErrBody `json:"error,omitempty"`
	NoOp          bool         `json:"noOp"`
	// Changes: tag-level diff. Operations: structural writes. Empty Changes can
	// still pair with non-empty Operations (native-only fix).
	Changes      []jsonChange  `json:"changes"`
	Operations   []string      `json:"operations"`
	BytesBefore  int64         `json:"bytesBefore"`
	BytesAfter   int64         `json:"bytesAfter"`
	PaddingAfter int64         `json:"paddingAfter"`
	Warnings     []jsonWarning `json:"warnings"`
}

// jsonChange is one field change: key, kind, and old/new. Picture/chapter
// set-count changes use Count (integer) and omit old/new.
type jsonChange struct {
	Key    string   `json:"key"`
	Change string   `json:"change"`
	Count  *int     `json:"count,omitempty"`
	Old    []string `json:"old,omitempty"`
	New    []string `json:"new,omitempty"`
}

// toJSONChanges converts tag changes to JSON. Shared by write report and lint --fix.
func toJSONChanges(changes []tag.Change) []jsonChange {
	out := make([]jsonChange, 0, len(changes))
	for _, c := range changes {
		jc := jsonChange{Key: string(c.Key), Change: c.Kind.String()}
		if isCountChange(c.Key) {
			// Integer count, not stringified Old/New (text render shows picture detail).
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
	// No-op plans stamp Operations with a "no changes" sentinel for the human line;
	// JSON operations must be [] (structural write list), not that sentinel.
	operations := r.Operations
	if plan.IsNoOp() {
		operations = nil
	}
	// nonNil so collections serialize as [] (never null) for clean plans too.
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
