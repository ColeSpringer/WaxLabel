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
		Long: "Resolve the given edits into a write plan and print exactly what saving\n" +
			"would do - the operations, the field-level changes, the size change,\n" +
			"padding, and warnings - without modifying the file. With no edits it\n" +
			"reports that the file is already up to date. The report is the same one\n" +
			"set acts on, so the two cannot disagree. Multiple files are previewed\n" +
			"independently; with --recursive, directory arguments are walked for audio\n" +
			"files. A single \"-\" reads the file from standard input.\n\n" +
			editPrecedenceHelp,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ce, err := ef.compile()
			if err != nil {
				return err
			}
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), args)
			if err != nil {
				return err
			}
			defer cleanup()
			paths := expandPaths(args, recursive)
			noteNoFiles(cmd.ErrOrStderr(), paths)
			return perFile(cmd, paths,
				func(ctx context.Context, path string) (*wl.Plan, error) {
					_, plan, err := ce.prepare(ctx, realOf(path))
					return plan, err
				},
				func(path string, plan *wl.Plan) any { return toJSONReport(path, plan) },
				func(path string, c classifiedError) any {
					return jsonReport{SchemaVersion: schemaVersion, File: path, Error: &jsonErrBody{c.code, c.message}}
				},
				func(w io.Writer, path string, plan *wl.Plan) { renderReport(w, path, plan) },
			)
		},
	}
	ef.bind(cmd)
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, previewing every audio file found")
	return cmd
}

// jsonReport is the machine-readable form of a write plan, shared by plan and
// set (set embeds it). On a per-file failure in a bulk run only SchemaVersion,
// File, and Error are set; otherwise Error is nil and the plan fields are
// populated.
type jsonReport struct {
	SchemaVersion int           `json:"schemaVersion"`
	File          string        `json:"file"`
	Error         *jsonErrBody  `json:"error,omitempty"`
	NoOp          bool          `json:"noOp"`
	Changes       []jsonChange  `json:"changes,omitempty"`
	Operations    []string      `json:"operations"`
	BytesBefore   int64         `json:"bytesBefore"`
	BytesAfter    int64         `json:"bytesAfter"`
	PaddingAfter  int64         `json:"paddingAfter"`
	Warnings      []jsonWarning `json:"warnings,omitempty"`
}

// jsonChange is one field's change in a write plan: the canonical key, how it
// changed ("added"/"removed"/"changed"), and the before/after values. It mirrors
// the shape of jsonDiffTag, naming the two sides old/new for a before/after edit.
type jsonChange struct {
	Key    string   `json:"key"`
	Change string   `json:"change"`
	Old    []string `json:"old,omitempty"`
	New    []string `json:"new,omitempty"`
}

// toJSONChanges converts a tag-change list to its JSON form. Shared by the write
// report and lint --fix so their change shape cannot drift.
func toJSONChanges(changes []tag.Change) []jsonChange {
	out := make([]jsonChange, 0, len(changes))
	for _, c := range changes {
		out = append(out, jsonChange{Key: string(c.Key), Change: c.Kind.String(), Old: c.Old, New: c.New})
	}
	return out
}

func toJSONReport(path string, plan *wl.Plan) jsonReport {
	r := plan.Report()
	jr := jsonReport{
		SchemaVersion: schemaVersion,
		File:          path,
		NoOp:          plan.IsNoOp(),
		Changes:       toJSONChanges(plan.Changes()),
		Operations:    r.Operations,
		BytesBefore:   r.BytesBefore,
		BytesAfter:    r.BytesAfter,
		PaddingAfter:  r.PaddingAfter,
	}
	if jr.Operations == nil {
		jr.Operations = []string{}
	}
	for _, x := range r.Warnings {
		jr.Warnings = append(jr.Warnings, jsonWarning{Code: x.Code.String(), Message: x.Message})
	}
	return jr
}
