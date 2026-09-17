package main

import (
	"fmt"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// newCopyCmd builds copy: project source metadata onto dest in place.
// Reports carried/lossy/dropped. --dry-run previews without writing.
func newCopyCmd() *cobra.Command {
	var (
		preset   string
		legacy   string
		id3Multi string
		dryRun   bool
		strict   bool
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
			// Exit 2 before parse; avoids ErrInvalidData.
			if err := checkEmptyOperands(srcPath, dstPath); err != nil {
				return err
			}
			// No streaming; "-" is invalid.
			if srcPath == stdinArg || dstPath == stdinArg {
				return usagef("copy does not read standard input; pass file paths")
			}
			// Empty write-shaping flags are usage errors (same as unset). Undefined flags skipped.
			if err := rejectEmptyScalarFlags(cmd); err != nil {
				return err
			}
			opts, err := resolveWriteFlags(preset, legacy, id3Multi)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			asJSON := jsonMode(cmd)
			// Human mode: per-file error line, not bare classifier. JSON uses dispatch envelope.
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
			// Non-regular paths exit 2. acceptsStdin false (no "-" hint). Missing paths fall through to parse.
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
			// Labels distinguish WebM from Matroska (same Format).
			srcLabel := transferFormatLabel(srcDoc.Format(), srcDoc.Properties().Container)
			dstLabel := transferFormatLabel(dstDoc.Format(), dstDoc.Properties().Container)
			if err != nil {
				// Show transfer report on both surfaces before return; scripts need detail on refusal.
				if !asJSON {
					renderTransfer(out, srcPath, dstPath, report, srcLabel, dstLabel)
					return err
				}
				if jerr := writeJSON(out, toJSONCopyError(srcPath, dstPath, report, err)); jerr != nil {
					return jerr
				}
				return alreadyRendered(err)
			}

			// Preview before touching the destination.
			if !asJSON {
				renderTransfer(out, srcPath, dstPath, report, srcLabel, dstLabel)
				// nil pictures: transfer report already lists them.
				renderReport(out, dstPath, plan, nil)
			}
			// --strict: fail if projection is lossy/dropped or write would warn.
			// WarnLegacyStripDropped and codec warnings bypass the carried gate; reuse plan reports.
			if strict {
				err := strictTransferError(report)
				if err == nil {
					if gerr := newStrictWarningGate(true).check(plan); gerr != nil {
						err = fmt.Errorf("%s: %w", dstPath, gerr)
					}
				}
				if err != nil {
					// Strict refusal writes nothing; JSON must name items (same uncapped key list as set --strict).
					if !asJSON {
						return err
					}
					if jerr := writeJSON(out, toJSONCopyError(srcPath, dstPath, report, err)); jerr != nil {
						return jerr
					}
					return alreadyRendered(err)
				}
			}
			if dryRun {
				if asJSON {
					return writeJSON(out, toJSONCopy(srcPath, dstPath, report, plan, true, false, nil))
				}
				fmt.Fprintf(out, "\nDry run; %s left untouched\n", dstPath)
				return nil
			}

			_, res, err := plan.Execute(ctx, wl.SaveBack())
			// Committed decides the outcome, not err (see writeFailed).
			if writeFailed(res, err) {
				return err
			}
			warnPostCommit(errOut, asJSON, dstPath, err)
			if asJSON {
				return writeJSON(out, toJSONCopy(srcPath, dstPath, report, plan, false, res.Committed, err))
			}
			renderSaveOutcome(out, dstPath, "", res, plan.IsNoOp(), wl.HasDiscardWarning(plan.Report().Warnings))
			return nil
		},
	}
	cmd.Flags().StringVar(&preset, "preset", "", "write policy preset: preserve|compatible|minimal")
	cmd.Flags().StringVar(&legacy, "legacy", "", "legacy-tag policy: preserve|strip. strip removes the DESTINATION's ID3v1/APEv2/stray-ID3 containers unconditionally, warning when one holds the only copy of a value")
	cmd.Flags().StringVar(&id3Multi, "id3-multi", "", "how an ID3v2.3 tag (MP3) stores a multi-valued field: null (NUL-separated, the default, a de-facto extension some readers do not split), repeat (one frame per value), or slash (values joined with a slash). ID3v2.4 tags (WAV/AIFF/AAC) separate values natively and ignore it")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the transfer without modifying the destination")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail (exit 2) instead of writing when the transfer is not lossless or the write would lose metadata")
	return cmd
}

// strictTransferError fails --strict when projection is not lossless.
// Uses TransferReport.Lossless (same predicate as header counts).
func strictTransferError(report wl.TransferReport) error {
	if report.Lossless() {
		return nil
	}
	_, lossy, dropped := report.Counts()
	return usagef("transfer is not lossless: %d lossy, %d dropped (omit --strict to write anyway)", lossy, dropped)
}

// transferLabel: field key, or counted noun for picture/chapter/synced-lyrics sets.
func transferLabel(it wl.TransferItem) string {
	switch it.Kind {
	case wl.TransferPicture:
		return fmt.Sprintf("pictures (%d)", it.Count)
	case wl.TransferChapter:
		return fmt.Sprintf("chapters (%d)", it.Count)
	case wl.TransferSyncedLyric:
		return fmt.Sprintf("synced lyrics (%d)", it.Count)
	default:
		// Sanitize unvalidated field names (control bytes, newlines).
		return tag.SanitizeLine(string(it.Key))
	}
}

// renderTransfer: carried/lossy/dropped summary, then per-item lines. Labels from transferFormatLabel.
func renderTransfer(w io.Writer, src, dst string, r wl.TransferReport, srcLabel, dstLabel string) {
	carried, lossy, dropped := r.Counts()
	// displayName escapes paths against forged header lines.
	fmt.Fprintf(w, "%s -> %s: transfer %s -> %s\n", displayName(src), displayName(dst), srcLabel, dstLabel)
	fmt.Fprintf(w, "  %d carried, %d lossy, %d dropped\n", carried, lossy, dropped)
	// Split set kinds (partial carry + loss) show carried part too; pure carried fields stay suppressed.
	splitKinds := map[wl.TransferKind]bool{}
	for _, it := range r.Items {
		if it.Kind != wl.TransferField && it.Disposition != wl.Carried {
			splitKinds[it.Kind] = true
		}
	}
	for _, it := range r.Items {
		if it.Disposition == wl.Carried && !splitKinds[it.Kind] {
			continue
		}
		// Sanitize reason (file-derived); carried items have no reason/colon.
		fmt.Fprintf(w, "  %-7s %s", it.Disposition, transferLabel(it))
		if it.Reason != "" {
			fmt.Fprintf(w, ": %s", tag.SanitizeLine(it.Reason))
		}
		fmt.Fprintln(w)
	}
}

// Display label for one transfer side. Matroska family shows container (WebM vs MKA).
// JSON sourceFormat/destFormat stay bare Format (WebM is container, not format identity).
func transferFormatLabel(f wl.Format, container string) string {
	// Show container when it differs from codec family (WebM/Matroska, WAV/RF64, AIFF/AIFC).
	if container != "" && container != f.String() {
		return container
	}
	return f.String()
}

// jsonCopy: machine-readable copy result. Embeds jsonReport from set so shapes stay aligned. file is dest.
type jsonCopy struct {
	jsonReport
	Source       string `json:"source"`
	SourceFormat string `json:"sourceFormat"`
	DestFormat   string `json:"destFormat"`
	jsonPostWrite
	Transfer  []jsonTransferItem `json:"transfer"`
	DryRun    bool               `json:"dryRun"`
	Committed bool               `json:"committed"`
}

type jsonTransferItem struct {
	Kind        string `json:"kind"`
	Key         string `json:"key,omitempty"`
	Count       int    `json:"count"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason,omitempty"`
}

// Refusal envelope: transfer array plus error. Bare error would leave scripts with code only.
func toJSONCopyError(src, dst string, r wl.TransferReport, err error) jsonCopy {
	jc := toJSONCopy(src, dst, r, nil, false, false, nil)
	c := classifyError(err)
	jc.Error = &jsonErrBody{Code: c.code, Message: perFileReason(err), Hint: c.hint}
	return jc
}

func toJSONCopy(src, dst string, r wl.TransferReport, plan *wl.Plan, dryRun, committed bool, postWrite error) jsonCopy {
	jc := jsonCopy{
		jsonReport:   toJSONReport(dst, plan),
		Source:       src,
		SourceFormat: r.Source.String(),
		DestFormat:   r.Dest.String(),
		Transfer:     []jsonTransferItem{},
		DryRun:       dryRun,
		Committed:    committed,
	}
	jc.setPostWrite(postWrite)
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
