package main

import (
	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// newPlanCmd builds the "plan" command, which resolves edits into a write plan
// and reports exactly what saving would do - without touching the file.
func newPlanCmd() *cobra.Command {
	var ef editFlags
	cmd := &cobra.Command{
		Use:   "plan <file>",
		Short: "Show what an edit would write, without writing it",
		Long: "Resolve the given edits into a write plan and print exactly what saving\n" +
			"would do - the operations, the size change, padding, and warnings -\n" +
			"without modifying the file. With no edits it reports that the file is\n" +
			"already up to date. The report is the same one set acts on, so the two\n" +
			"cannot disagree.\n\n" +
			editPrecedenceHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, plan, err := preparePlan(cmd.Context(), args[0], &ef)
			if err != nil {
				return err
			}
			if jsonMode(cmd) {
				return writeJSON(cmd.OutOrStdout(), toJSONReport(args[0], plan))
			}
			renderReport(cmd.OutOrStdout(), args[0], plan)
			return nil
		},
	}
	ef.bind(cmd)
	return cmd
}

// jsonReport is the machine-readable form of a write plan, shared by plan and
// set (set embeds it).
type jsonReport struct {
	SchemaVersion int           `json:"schemaVersion"`
	File          string        `json:"file"`
	NoOp          bool          `json:"noOp"`
	Operations    []string      `json:"operations"`
	BytesBefore   int64         `json:"bytesBefore"`
	BytesAfter    int64         `json:"bytesAfter"`
	PaddingAfter  int64         `json:"paddingAfter"`
	Warnings      []jsonWarning `json:"warnings,omitempty"`
}

func toJSONReport(path string, plan *wl.Plan) jsonReport {
	r := plan.Report()
	jr := jsonReport{
		SchemaVersion: schemaVersion,
		File:          path,
		NoOp:          plan.IsNoOp(),
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
