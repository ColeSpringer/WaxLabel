package main

import (
	"fmt"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// newCopyCmd builds the "copy" command, which copies one file's canonical
// metadata onto another - across formats - and reports what carries, downgrades,
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
		Example: "  waxlabel copy source.flac dest.mp3\n" +
			"  waxlabel copy --dry-run source.flac dest.m4a",
		Long: "Read <source>, project its canonical tags, pictures, chapters, and synced\n" +
			"lyrics onto <dest>, and rewrite <dest> in place. The two files need not share a\n" +
			"format: each value is carried, downgraded, or dropped according to what\n" +
			"<dest>'s format can store, and that loss report is printed before the\n" +
			"write. The copy overlays the source onto the destination - keys present\n" +
			"only in <dest> are kept. With --dry-run nothing is written.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			srcPath, dstPath := args[0], args[1]
			// An empty operand is a usage error (exit 2), caught before any parse so it does
			// not reach the library's ErrInvalidData (exit 4) fallback.
			if err := checkEmptyOperands(srcPath, dstPath); err != nil {
				return err
			}
			// copy is a file-to-file operation with no streaming model (that is the
			// library's WriteTo), so "-" names no real file here. Reject it up front as a
			// usage error rather than try to open a file literally named "-".
			if srcPath == stdinArg || dstPath == stdinArg {
				return usagef("copy does not read standard input; pass file paths")
			}
			opts, err := resolveWriteFlags(preset, legacy)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			asJSON := jsonMode(cmd)
			// On a parse failure, match the per-file "waxlabel: <path>: <reason>" line
			// dump/verify/set print (human), rather than the classifier's bare "no such
			// file: <path>". JSON output is unchanged: the error returns to dispatch,
			// which emits the not-found envelope (the machine contract scripts read).
			parse := func(path string) (*wl.Document, error) {
				doc, err := wl.ParseFile(ctx, path)
				if err != nil {
					if asJSON {
						return nil, err
					}
					perFileError(errOut, path, err)
					return nil, alreadyRendered(err)
				}
				return doc, nil
			}
			// Reject a non-regular operand (FIFO/directory/socket) up front as a usage error
			// (exit 2), matching the other file commands; copy takes no "-", so the paths are
			// used as-is and the hint must not suggest "-" (acceptsStdin false). A nonexistent
			// path passes through to parse's own not-found.
			if err := checkRegularInputs(func(p string) string { return p }, false, srcPath, dstPath); err != nil {
				return err
			}
			srcDoc, err := parse(srcPath)
			if err != nil {
				return err
			}
			dstDoc, err := parse(dstPath)
			if err != nil {
				return err
			}

			plan, report, err := srcDoc.PrepareTransfer(dstDoc, opts...)
			// Header labels distinguish WebM from Matroska, which share one Format; the
			// container subtype comes from each parsed doc.
			srcLabel := transferFormatLabel(srcDoc.Format(), srcDoc.Properties().Container)
			dstLabel := transferFormatLabel(dstDoc.Format(), dstDoc.Properties().Container)
			if err != nil {
				// The report still explains the failure (e.g. a read-only destination
				// drops everything), so surface it before returning the error.
				if !asJSON {
					renderTransfer(out, srcPath, dstPath, report, srcLabel, dstLabel)
				}
				return err
			}

			// Preview the transfer (and, for a dry run, the would-be write) before
			// touching the destination.
			if !asJSON {
				renderTransfer(out, srcPath, dstPath, report, srcLabel, dstLabel)
				// copy adds no pictures of its own (the transfer report above already
				// details the carried pictures), so no added-picture detail here.
				renderReport(out, dstPath, plan, nil)
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
			renderSaveOutcome(out, dstPath, "", res, plan.IsNoOp())
			return nil
		},
	}
	cmd.Flags().StringVar(&preset, "preset", "", "write policy preset: preserve|compatible|minimal")
	cmd.Flags().StringVar(&legacy, "legacy", "", "legacy-tag policy: preserve|strip")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the transfer without modifying the destination")
	return cmd
}

// transferLabel names a transfer item for display: the key for a field, or a
// counted noun for the picture, chapter, and synced-lyrics sets.
func transferLabel(it wl.TransferItem) string {
	switch it.Kind {
	case wl.TransferPicture:
		return fmt.Sprintf("pictures (%d)", it.Count)
	case wl.TransferChapter:
		return fmt.Sprintf("chapters (%d)", it.Count)
	case wl.TransferSyncedLyric:
		return fmt.Sprintf("synced lyrics (%d)", it.Count)
	default:
		// it.Key is file-derived: an unvalidated Vorbis/MP4 field name from parse can
		// carry control bytes or a newline, and this prints on a single line, so escape it.
		return tag.SanitizeLine(string(it.Key))
	}
}

// renderTransfer prints the cross-format loss report: a carried/lossy/dropped
// summary followed by a line for every item that does not carry cleanly (the
// losses are what the user needs to see). srcLabel/dstLabel are the display names
// for each side's format (see transferFormatLabel): they distinguish WebM from
// Matroska, which share one Format.
func renderTransfer(w io.Writer, src, dst string, r wl.TransferReport, srcLabel, dstLabel string) {
	carried, lossy, dropped := r.Counts()
	// Escape and stdin-relabel the paths for the single-line header (consistent with
	// the other record headers), so a hostile filename cannot forge a line.
	fmt.Fprintf(w, "%s -> %s: transfer %s -> %s\n", displayName(src), displayName(dst), srcLabel, dstLabel)
	fmt.Fprintf(w, "  %d carried, %d lossy, %d dropped\n", carried, lossy, dropped)
	for _, it := range r.Items {
		if it.Disposition == wl.Carried {
			continue
		}
		// it.Reason can include file-derived text, such as an unrepresentable cover MIME.
		// This report is line-based, so escape it before printing; tag.SanitizeText keeps
		// real newlines for multi-line values, but here a newline would forge a report line.
		fmt.Fprintf(w, "  %-7s %s: %s\n", it.Disposition, transferLabel(it), tag.SanitizeLine(it.Reason))
	}
}

// transferFormatLabel is the display name for one side of a transfer header. The
// WebM/Matroska distinction lives only in the container label (both.mka and
// .webm are FormatMatroska), so for the Matroska family it uses the container
// ("WebM" / "Matroska"); every other format keeps its Format string, so e.g. AAC
// stays "AAC (ADTS)". The JSON sourceFormat/destFormat deliberately stay the bare
// Format string: "WebM" is a container subtype, the format identity is Matroska.
func transferFormatLabel(f wl.Format, container string) string {
	if f == wl.FormatMatroska && container != "" {
		return container
	}
	return f.String()
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
