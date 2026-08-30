package main

import (
	"fmt"
	"io"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// newCopyCmd builds the "copy" command, which copies one file's canonical
// metadata onto another, across formats, reporting what carries, downgrades, or is
// lost on the way. The destination is rewritten in place (atomically);
// --dry-run previews the transfer and the write without touching it.
func newCopyCmd() *cobra.Command {
	var (
		preset string
		legacy string
		dryRun bool
		strict bool
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
			// Exit 2, before any parse, so it does not fall through to ErrInvalidData.
			if err := checkEmptyOperands(srcPath, dstPath); err != nil {
				return err
			}
			// copy is file-to-file with no streaming model, so "-" names no real file.
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
			// Human output matches the per-file line dump/verify/set print, rather than the
			// classifier's bare "no such file". JSON still returns to dispatch's envelope.
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
			// A FIFO, directory, or socket is exit 2. acceptsStdin is false so the hint does
			// not suggest "-"; a nonexistent path falls through to parse's not-found.
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
			// The labels distinguish WebM from Matroska, which share one Format.
			srcLabel := transferFormatLabel(srcDoc.Format(), srcDoc.Properties().Container)
			dstLabel := transferFormatLabel(dstDoc.Format(), dstDoc.Properties().Container)
			if err != nil {
				// The report still explains the failure - which fields could not be carried,
				// and why - so it is shown on both surfaces before returning, not just the
				// human one. A refused copy is exactly when a script needs the detail.
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
				// nil pictures: the transfer report above already details the carried ones.
				renderReport(out, dstPath, plan, nil)
			}
			// --strict fails a copy that is not a faithful carry, before any write. Two
			// conditions, because a transfer can lose metadata two ways: the projection can
			// grade an item lossy or dropped (the report), and the destination codec can
			// warn about what the write itself does to a value it accepted (the plan). The
			// second is not a formality - WarnLegacyStripDropped is emitted outside the
			// carried gate, and every codec-emitted warning survives a carry - so it reuses
			// the same gate set and plan reports.
			if strict {
				err := strictTransferError(report)
				if err == nil {
					if gerr := newStrictWarningGate(true).check(plan); gerr != nil {
						err = fmt.Errorf("%s: %w", dstPath, gerr)
					}
				}
				if err != nil {
					// A strict run writes nothing, so this envelope is the user's only account
					// of what to fix. Counts alone would not name the items, which is the same
					// reasoning that keeps set --strict's key list uncapped.
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
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the transfer without modifying the destination")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail (exit 2) instead of writing when the transfer is not lossless or the write would lose metadata")
	return cmd
}

// strictTransferError fails a --strict copy whose projection is not a faithful carry,
// naming what was lost. It reads TransferReport.Lossless, the same predicate the report
// header counts, so the summary the user sees and the refusal cannot disagree.
func strictTransferError(report wl.TransferReport) error {
	if report.Lossless() {
		return nil
	}
	_, lossy, dropped := report.Counts()
	return usagef("transfer is not lossless: %d lossy, %d dropped (omit --strict to write anyway)", lossy, dropped)
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
		// File-derived: an unvalidated field name can carry control bytes or a newline.
		return tag.SanitizeLine(string(it.Key))
	}
}

// renderTransfer prints the cross-format loss report: a carried/lossy/dropped summary,
// then a line per item that does not carry cleanly. srcLabel/dstLabel come from
// transferFormatLabel.
func renderTransfer(w io.Writer, src, dst string, r wl.TransferReport, srcLabel, dstLabel string) {
	carried, lossy, dropped := r.Counts()
	// displayName escapes the paths, so a hostile filename cannot forge a header line.
	fmt.Fprintf(w, "%s -> %s: transfer %s -> %s\n", displayName(src), displayName(dst), srcLabel, dstLabel)
	fmt.Fprintf(w, "  %d carried, %d lossy, %d dropped\n", carried, lossy, dropped)
	// A set kind (pictures, chapters, synced lyrics) that split into a carried
	// part plus a lossy or dropped remainder shows its carried part too, so a
	// split set does not read as items gone missing. Carried fields stay
	// suppressed: the detail block is a loss report, and per-field carried lines
	// would drown it.
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
		// Reason can carry file-derived text, and a newline would forge a report
		// line; a carried item has no reason, so it takes no colon.
		fmt.Fprintf(w, "  %-7s %s", it.Disposition, transferLabel(it))
		if it.Reason != "" {
			fmt.Fprintf(w, ": %s", tag.SanitizeLine(it.Reason))
		}
		fmt.Fprintln(w)
	}
}

// transferFormatLabel is the display name for one side of a transfer header. .mka and
// .webm are both FormatMatroska, so that family shows its container instead; every other
// format keeps its Format string. The JSON sourceFormat/destFormat stay the bare Format:
// "WebM" is a container subtype, the format identity is Matroska.
func transferFormatLabel(f wl.Format, container string) string {
	// Any container that names itself something other than its codec family gets that name:
	// WebM and Matroska share a Format, and so do WAV/RF64/BW64 and AIFF/AIFC. Naming only
	// the family would tell an RF64 user their 64-bit file is a WAV, which is the very
	// distinction Properties.Container exists to draw and which --json already reports.
	if container != "" && container != f.String() {
		return container
	}
	return f.String()
}

// jsonCopy is the machine-readable result of a copy: per-item transfer dispositions plus
// the destination write record. It embeds the jsonReport `set` emits, so the two cannot
// drift. The embedded "file" is the destination.
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

// toJSONCopyError is the envelope for a copy that is refused before the write: the same
// transfer array a successful run emits, plus the error that stopped it. Returning the bare
// error instead would leave a script with a code and no account of which items were lost,
// while the human surface printed the whole report.
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
