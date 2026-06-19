package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/spf13/cobra"
)

// newSetCmd builds the "set" command, which applies edits and writes the result.
// By default it rewrites each file in place atomically (a no-op writes nothing);
// with -o it writes a single new file and leaves the original untouched. Multiple
// files (and directories, with --recursive) are edited independently.
func newSetCmd() *cobra.Command {
	var (
		ef            editFlags
		output        string
		verify        bool
		preserveMtime bool
		recursive     bool
	)
	cmd := &cobra.Command{
		Use:   "set <file>...",
		Short: "Apply tag edits and save the file",
		Long: "Apply the given edits and write the result. By default it rewrites each\n" +
			"file in place atomically (temp file, fsync, rename); a no-op writes\n" +
			"nothing. With -o it writes a single complete new file instead, leaving\n" +
			"the original untouched (so -o takes exactly one input). Multiple files\n" +
			"are edited independently; with --recursive, directory arguments are\n" +
			"walked for audio files. A single \"-\" reads from standard input and\n" +
			"requires -o (editing standard input in place is meaningless). The plan is\n" +
			"printed before each outcome.\n\n" +
			editPrecedenceHelp,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate argument shape (pure, no I/O) before compile() reads any
			// --add-cover files, so a misuse like "set - --add-cover x" without -o
			// reports the actionable stdin usage error rather than a cover read error.
			if err := checkSetStdin(args, output); err != nil {
				return err
			}
			var extra []wl.WriteOption
			if verify {
				extra = append(extra, wl.WithVerifyEssence())
			}
			if preserveMtime {
				extra = append(extra, wl.WithPreserveModTime())
			}
			ce, err := ef.compile(extra...)
			if err != nil {
				return err
			}
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), args)
			if err != nil {
				return err
			}
			defer cleanup()

			paths, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			if output != "" && len(paths) != 1 {
				return usagef("-o writes a single file, so it takes exactly one input (got %d)", len(paths))
			}
			// Invocation-level guardrails and notes run only once there is at least one
			// file to act on, so a note never claims a value was "written" on a run the
			// directory (U1) or empty-walk (U4) checks then abort with nothing written.
			if len(paths) > 0 {
				if err := notifyUnknownKeys(cmd.ErrOrStderr(), ce, ef.strict, jsonMode(cmd)); err != nil {
					return err
				}
				notifyValueNotes(cmd.ErrOrStderr(), &ef, jsonMode(cmd))
			}
			return runSet(cmd, paths, realOf, ce, output, ef.strict)
		},
	}
	ef.bind(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to this path instead of editing the file in place (one input only)")
	cmd.Flags().BoolVar(&verify, "verify", false, "after writing, verify the saved file's audio essence matches the source")
	cmd.Flags().BoolVar(&preserveMtime, "preserve-mtime", false, "keep the file's modification time (by default it is updated)")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, editing every audio file found")
	return cmd
}

// checkSetStdin enforces that standard input ("-") is used only as a single input
// written elsewhere via -o. Editing standard input in place is meaningless, and
// there is one stdin to consume, so it cannot be combined with other inputs.
func checkSetStdin(args []string, output string) error {
	if !slices.Contains(args, stdinArg) {
		return nil
	}
	if len(args) != 1 {
		return usagef("standard input (%q) cannot be combined with other inputs", stdinArg)
	}
	if output == "" {
		return usagef("cannot edit standard input in place; use -o to write the result to a file")
	}
	return nil
}

// runSet applies the compiled edit to each path and saves it. Each file's plan is
// previewed before its write, so a failed write still shows what was attempted;
// the first error sets the exit class while the remaining files still process.
// JSON output is always an array, one element per input; a multi-file text run
// ends with a one-line summary. The returned error is alreadyRendered, preserving
// the exit class without rendering a second time.
func runSet(cmd *cobra.Command, paths []string, realOf func(string) string, ce *compiledEdit, output string, strict bool) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	asJSON := jsonMode(cmd)
	// An empty path list is only reachable when a --recursive walk matched no audio
	// files: cobra requires >=1 argument, the -o path already rejects len != 1, and a
	// passed-through nonexistent file fails per-file with exit 6. For a mutating
	// command that is an error, not a silent success - so `set "$DIR" --recursive ...
	// && echo done` cannot falsely report success. Exit 2 (usage), consistent with
	// U1's directory case. Returning here, before any note, lets the dispatcher print
	// the message exactly once (a noteNoFiles call would print it a second time).
	if len(paths) == 0 {
		return usagef("no audio files found")
	}
	notifier := newSingleValuedNotifier(strict, asJSON, errOut)
	var items []any
	var firstErr error
	changed, unchanged, failed, rendered := 0, 0, 0, 0

	fail := func(path string, err error) {
		if firstErr == nil {
			firstErr = err
		}
		failed++
		if asJSON {
			items = append(items, errorSetResult(path, output, classifyError(err)))
		} else {
			fmt.Fprintf(errOut, "waxlabel: %s: %s\n", path, perFileReason(err))
		}
	}

	for _, path := range paths {
		doc, plan, err := ce.prepare(cmd.Context(), realOf(path))
		if err != nil {
			fail(path, err)
			continue
		}
		// Under --strict, a single-valued key given multiple values fails the file
		// before any write; otherwise it is noted (once per key) and the write
		// proceeds, since the writer stores the values faithfully.
		if err := notifier.check(plan); err != nil {
			fail(path, err)
			continue
		}
		// Print the plan before the write so the preview is shown even if the write
		// then fails (the help promises this ordering); JSON aggregates instead.
		if !asJSON {
			if rendered > 0 {
				fmt.Fprintln(out)
			}
			renderReport(out, path, plan)
			rendered++
		}
		dst := wl.SaveBack()
		if output != "" {
			// WaxLabel never transcodes, so a mismatched output extension means a
			// misnamed file; warn (on stderr, non-fatally) but still write.
			warnExtensionMismatch(errOut, output, doc.Format())
			dst = wl.SaveAsFile(output)
		}
		_, res, err := plan.Execute(cmd.Context(), dst)
		if err != nil {
			fail(path, err)
			continue
		}
		if res.Committed {
			changed++
		} else {
			unchanged++
		}
		if asJSON {
			items = append(items, toJSONSetResult(path, output, plan, res))
		} else {
			renderSaveOutcome(out, path, output, res)
		}
	}

	if asJSON {
		if err := emitJSONList(out, items); err != nil {
			return err
		}
	} else if len(paths) > 1 {
		fmt.Fprintf(out, "\n%d changed, %d unchanged, %d failed\n", changed, unchanged, failed)
	}
	return alreadyRendered(firstErr)
}

// warnExtensionMismatch prints a non-fatal note when the output path's extension
// does not match the source format. WaxLabel rewrites metadata without
// transcoding, so writing e.g. FLAC bytes to "out.mp3" produces a misnamed file;
// the extension is advisory, so the write still proceeds. A path with no
// extension, or a format whose extensions are unknown, is left alone.
func warnExtensionMismatch(w io.Writer, output string, f wl.Format) {
	ext := strings.ToLower(filepath.Ext(output))
	if ext == "" {
		return
	}
	exts := wl.ExtensionsFor(f)
	if len(exts) == 0 || slices.Contains(exts, ext) {
		return
	}
	fmt.Fprintf(w, "waxlabel: warning: writing %s data to %s; WaxLabel does not transcode\n", f, output)
}

// renderSaveOutcome reports where the bytes went: a new file, an in-place save,
// or nothing for a no-op save-back.
func renderSaveOutcome(w io.Writer, path, output string, res wl.SaveResult) {
	switch {
	case output != "":
		fmt.Fprintf(w, "\nWrote %s (%s)\n", output, wl.HumanBytes(res.Dest.Size))
	case !res.Committed:
		fmt.Fprintf(w, "\nNo changes; %s left untouched\n", path)
	default:
		fmt.Fprintf(w, "\nSaved %s (%s)\n", path, wl.HumanBytes(res.Dest.Size))
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

// errorSetResult is the per-file JSON entry for a file that failed in a bulk set
// run: the report carries only the file and the classified error, with the
// (unwritten) output echoed back.
func errorSetResult(path, output string, c classifiedError) jsonSetResult {
	return jsonSetResult{
		jsonReport: jsonReport{SchemaVersion: schemaVersion, File: path, Error: &jsonErrBody{c.code, c.message}},
		Output:     output,
	}
}
