package main

import (
	"fmt"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// newSetCmd builds the "set" command, which applies edits and writes the result.
// By default it rewrites the file in place atomically (a no-op writes nothing);
// with -o it writes a new file and leaves the original untouched.
func newSetCmd() *cobra.Command {
	var (
		ef            editFlags
		output        string
		verify        bool
		preserveMtime bool
	)
	cmd := &cobra.Command{
		Use:   "set <file>",
		Short: "Apply tag edits and save the file",
		Long: "Apply the given edits and write the result. By default it rewrites the\n" +
			"file in place atomically (temp file, fsync, rename); a no-op writes\n" +
			"nothing. With -o it writes a complete new file instead, leaving the\n" +
			"original untouched. The plan is printed before the outcome.\n\n" +
			editPrecedenceHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var extra []wl.WriteOption
			if verify {
				extra = append(extra, wl.WithVerifyEssence())
			}
			if preserveMtime {
				extra = append(extra, wl.WithPreserveModTime())
			}
			_, plan, err := preparePlan(cmd.Context(), args[0], &ef, extra...)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			asJSON := jsonMode(cmd)
			// Print the plan before attempting the write, so the preview is shown
			// even if the write then fails (the help promises this ordering). JSON
			// mode emits a single combined object on success instead.
			if !asJSON {
				renderReport(out, args[0], plan)
			}

			dst := wl.SaveBack()
			if output != "" {
				dst = wl.SaveAsFile(output)
			}
			_, res, err := plan.Execute(cmd.Context(), dst)
			if err != nil {
				return err
			}

			if asJSON {
				return writeJSON(out, toJSONSetResult(args[0], output, plan, res))
			}
			renderSaveOutcome(out, args[0], output, res)
			return nil
		},
	}
	ef.bind(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to this path instead of editing the file in place")
	cmd.Flags().BoolVar(&verify, "verify", false, "after writing, verify the saved file's audio essence matches the source")
	cmd.Flags().BoolVar(&preserveMtime, "preserve-mtime", false, "keep the file's modification time (by default it is updated)")
	return cmd
}

// renderSaveOutcome reports where the bytes went: a new file, an in-place save,
// or nothing for a no-op save-back.
func renderSaveOutcome(w io.Writer, path, output string, res wl.SaveResult) {
	switch {
	case output != "":
		fmt.Fprintf(w, "\nWrote %s (%s)\n", output, humanBytes(res.Dest.Size))
	case !res.Committed:
		fmt.Fprintf(w, "\nNo changes; %s left untouched\n", path)
	default:
		fmt.Fprintf(w, "\nSaved %s (%s)\n", path, humanBytes(res.Dest.Size))
	}
}

// jsonSetResult is the machine-readable outcome of a save: the plan plus where
// the bytes landed and whether they were committed.
type jsonSetResult struct {
	jsonReport
	Committed bool   `json:"committed"`
	Output    string `json:"output,omitempty"`
	Size      int64  `json:"size"`
}

func toJSONSetResult(path, output string, plan *wl.Plan, res wl.SaveResult) jsonSetResult {
	return jsonSetResult{
		jsonReport: toJSONReport(path, plan),
		Committed:  res.Committed,
		Output:     output,
		Size:       res.Dest.Size,
	}
}
