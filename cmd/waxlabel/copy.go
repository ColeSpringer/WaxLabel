package main

import (
	"fmt"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// newCopyCmd builds the "copy" command, which copies one file's canonical
// metadata onto another — across formats — and reports what carries, downgrades,
// or is lost on the way. The destination is rewritten in place (atomically);
// --dry-run previews the transfer and the write without touching it.
func newCopyCmd() *cobra.Command {
	var (
		preset string
		legacy string
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "copy <source> <dest>",
		Short: "Copy metadata from one file onto another (cross-format)",
		Long: "Read <source>, project its canonical tags, pictures, and chapters onto\n" +
			"<dest>, and rewrite <dest> in place. The two files need not share a\n" +
			"format: each value is carried, downgraded, or dropped according to what\n" +
			"<dest>'s format can store, and that loss report is printed before the\n" +
			"write. The copy overlays the source onto the destination — keys present\n" +
			"only in <dest> are kept. With --dry-run nothing is written.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			srcPath, dstPath := args[0], args[1]
			opts, err := resolveWriteFlags(preset, legacy)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			srcDoc, err := wl.ParseFile(ctx, srcPath)
			if err != nil {
				return err
			}
			dstDoc, err := wl.ParseFile(ctx, dstPath)
			if err != nil {
				return err
			}

			plan, report, err := srcDoc.PrepareTransfer(dstDoc, opts...)
			out := cmd.OutOrStdout()
			asJSON := jsonMode(cmd)
			if err != nil {
				// The report still explains the failure (e.g. a read-only destination
				// drops everything), so surface it before returning the error.
				if !asJSON {
					renderTransfer(out, srcPath, dstPath, report)
				}
				return err
			}

			// Preview the transfer (and, for a dry run, the would-be write) before
			// touching the destination.
			if !asJSON {
				renderTransfer(out, srcPath, dstPath, report)
				renderReport(out, dstPath, plan)
			}
			if dryRun {
				if asJSON {
					return writeJSON(out, toJSONCopy(srcPath, dstPath, report, plan, true, false))
				}
				fmt.Fprintf(out, "\nDry run; %s left untouched\n", dstPath)
				return nil
			}

			_, res, err := plan.Execute(ctx, wl.SaveBack())
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(out, toJSONCopy(srcPath, dstPath, report, plan, false, res.Committed))
			}
			renderSaveOutcome(out, dstPath, "", res)
			return nil
		},
	}
	cmd.Flags().StringVar(&preset, "preset", "", "write policy preset: preserve|compatible|canonical|minimal")
	cmd.Flags().StringVar(&legacy, "legacy", "", "legacy-tag policy: preserve|strip|reconcile|update-existing")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the transfer and write without modifying the destination")
	return cmd
}

// transferLabel names a transfer item for display: the key for a field, or a
// counted noun for the picture and chapter sets.
func transferLabel(it wl.TransferItem) string {
	switch it.Kind {
	case wl.TransferPicture:
		return fmt.Sprintf("pictures (%d)", it.Count)
	case wl.TransferChapter:
		return fmt.Sprintf("chapters (%d)", it.Count)
	default:
		return string(it.Key)
	}
}

// renderTransfer prints the cross-format loss report: a carried/lossy/dropped
// summary followed by a line for every item that does not carry cleanly (the
// losses are what the user needs to see).
func renderTransfer(w io.Writer, src, dst string, r wl.TransferReport) {
	carried, lossy, dropped := r.Counts()
	fmt.Fprintf(w, "%s -> %s: transfer %s -> %s\n", src, dst, r.Source, r.Dest)
	fmt.Fprintf(w, "  %d carried, %d lossy, %d dropped\n", carried, lossy, dropped)
	for _, it := range r.Items {
		if it.Disposition == wl.Carried {
			continue
		}
		fmt.Fprintf(w, "  %-7s %s: %s\n", it.Disposition, transferLabel(it), it.Reason)
	}
}

// jsonCopy is the machine-readable result of a copy: the per-item transfer
// disposition plus the full destination write record. It embeds the same
// jsonReport that `set` emits (operations, padding, warnings, byte sizes, no-op),
// so copy's --json carries the same write detail and cannot drift from set's. The
// embedded "file" is the destination being written.
type jsonCopy struct {
	jsonReport
	Source       string             `json:"source"`
	SourceFormat string             `json:"sourceFormat"`
	DestFormat   string             `json:"destFormat"`
	Transfer     []jsonTransferItem `json:"transfer"`
	DryRun       bool               `json:"dryRun"`
	Committed    bool               `json:"committed"`
}

type jsonTransferItem struct {
	Kind        string `json:"kind"`
	Key         string `json:"key,omitempty"`
	Count       int    `json:"count"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason,omitempty"`
}

func toJSONCopy(src, dst string, r wl.TransferReport, plan *wl.Plan, dryRun, committed bool) jsonCopy {
	jc := jsonCopy{
		jsonReport:   toJSONReport(dst, plan),
		Source:       src,
		SourceFormat: r.Source.String(),
		DestFormat:   r.Dest.String(),
		Transfer:     []jsonTransferItem{},
		DryRun:       dryRun,
		Committed:    committed,
	}
	for _, it := range r.Items {
		jc.Transfer = append(jc.Transfer, jsonTransferItem{
			Kind:        it.Kind.String(),
			Key:         string(it.Key),
			Count:       it.Count,
			Disposition: it.Disposition.String(),
			Reason:      it.Reason,
		})
	}
	return jc
}
