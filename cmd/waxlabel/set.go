package main

import (
	"fmt"
	"io"
	"os"
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
		overwrite     bool
		verify        bool
		preserveMtime bool
		recursive     bool
		quiet         bool
	)
	cmd := &cobra.Command{
		Use:   "set <file>...",
		Short: "Apply tag edits and save the file",
		Example: "  waxlabel set song.flac --set TITLE=\"Hey\" --add ARTIST=A --add-cover front.jpg\n" +
			"  waxlabel set song.flac --strip-encoder -o cleaned.flac",
		Long: "Apply the given edits and write the result. By default it rewrites each\n" +
			"file in place atomically (temp file, fsync, rename); a no-op writes\n" +
			"nothing. With -o it writes a single complete new file instead, leaving\n" +
			"the original untouched (so -o takes exactly one input). Multiple files\n" +
			"are edited independently, each as its own atomic write; with --recursive,\n" +
			"directory arguments are walked for audio files. Because each file commits\n" +
			"on its own, a failure partway through a bulk or --recursive run leaves the\n" +
			"files already saved in place (it is not one transaction) - preview a bulk\n" +
			"edit with 'plan --recursive' first. A single \"-\" reads from standard input\n" +
			"and requires -o (editing standard input in place is meaningless). The plan\n" +
			"is printed before each outcome.\n\n" +
			editPrecedenceHelp,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A present-but-empty -o ("") is indistinguishable from cobra's unset
			// default in every downstream `output != ""` check, so a `set f -o ''`
			// would silently fall through to an in-place save-back and overwrite the
			// input. Reject it once here (before checkSetStdin), which guarantees a
			// present -o is non-empty and keeps every later check valid (B1).
			if cmd.Flags().Changed("output") && output == "" {
				return usagef("output path (-o) cannot be empty")
			}
			// Validate argument shape (pure, no I/O) before compile() reads any
			// --add-cover files, so a misuse like "set - --add-cover x" without -o
			// reports the actionable stdin usage error rather than a cover read error.
			if err := checkSetStdin(args, output); err != nil {
				return err
			}
			// Reject an explicitly-empty --preset/--legacy/--padding first, so `set f
			// --preset ''` reports the precise flag mistake rather than proceeding to a
			// confusing no-op (U4).
			if err := rejectEmptyScalarFlags(cmd); err != nil {
				return err
			}
			// A set with no edit flags and no -o is a no-op rewrite-in-place - almost always
			// a forgotten edit flag - so reject it (exit 2) rather than silently do nothing.
			// With -o it is a deliberate verbatim copy, kept (U1).
			if output == "" && editFlagsEmpty(cmd) {
				return usagef("no edits given (use --set/--add/--clear/--add-cover/--add-chapter/…)")
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

			paths, skipped, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			// Report extension-skipped files up front (before the per-file edit notes,
			// which gate on there being work to do): this is input discovery, useful even
			// when the walk then matches nothing to edit (Codex #9).
			noteSkipped(cmd.ErrOrStderr(), skipped, jsonMode(cmd))
			if output != "" && len(paths) != 1 {
				return usagef("-o writes a single file, so it takes exactly one input (got %d)", len(paths))
			}
			// Validate the -o destination before any write. len(paths)==1 is guaranteed
			// here by the check above, so realOf(paths[0]) is the single input.
			if output != "" {
				if err := checkOutputTarget(output, realOf(paths[0]), overwrite); err != nil {
					return err
				}
			}
			// Invocation-level guardrails and notes run only once there is at least one
			// path to act on, so a note never claims a value was "written" on a run the
			// directory (U1) or empty-walk (U4) checks then abort with nothing written.
			if err := notifyInvocationNotes(cmd.ErrOrStderr(), ce, &ef, realOf, paths, jsonMode(cmd)); err != nil {
				return err
			}
			// An unquoted value with spaces (--set TITLE=Two Words) leaves a stray bare-word
			// positional beside the real input; set would write a truncated tag to each named
			// file before the stray word fails not-found. Refuse the whole run up front (exit
			// 2, nothing written) - the message says so explicitly - so a script cannot misread
			// a partial write as success. plan, which writes nothing, keeps the advisory (#1).
			if hint, ok := quotingHint(&ef, realOf, args); ok {
				return usagef("%s; nothing was written", hint)
			}
			return runSet(cmd, paths, realOf, ce, output, ef.strict, quiet, verify)
		},
	}
	ef.bind(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to this path instead of editing the file in place (one input only)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "allow -o to replace an existing destination file (by default an existing -o target is refused)")
	cmd.Flags().BoolVar(&verify, "verify", false, "after writing, verify the saved file's audio essence matches the source")
	cmd.Flags().BoolVar(&preserveMtime, "preserve-mtime", false, "keep the file's modification time (by default it is updated)")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, editing every audio file found (selected by file extension)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress the per-file plan and outcome (errors and the final summary still print); a single-file set is then silent on success")
	return cmd
}

// checkOutputTarget validates the -o destination before any write. A directory can
// never be the target (the atomic rename fails EISDIR, and --overwrite cannot fix
// that), so it is rejected up front with a clear message rather than the leaked
// temp-file error a later rename would produce. An existing entry is refused unless
// --overwrite, except when it resolves to the single input itself (set f -o f is
// effectively in-place). inputReal is realOf(paths[0]): when it does not exist the
// overwrite guard stays silent, since the parse will then fail and write nothing -
// so the target is safe, and the more-relevant not-found error should surface
// instead of an "already exists" pointing at the wrong operand.
func checkOutputTarget(output, inputReal string, overwrite bool) error {
	// "-" is the stdin/stdout sentinel, not a filename. set is an atomic file-replace
	// command (streaming to stdout is the library's WriteTo, a different model), so
	// reject it up front rather than write a literal file named "-" in the cwd.
	if output == stdinArg {
		return usagef("-o - is not supported; set writes a named file")
	}
	// Stat (follows symlinks) so a directory - or a symlink to one - is caught.
	if fi, err := os.Stat(output); err == nil && fi.IsDir() {
		return usagef("-o target %q is a directory, not a file", output)
	}
	// Verify the destination directory exists now (before the plan renders), so a
	// mistyped -o path fails up front rather than only at the atomic-write temp create
	// - which would otherwise print the whole plan first and then a late I/O error.
	// Checked even under --overwrite: a missing parent dir cannot be overwritten. (#6)
	parent := filepath.Dir(output)
	if fi, err := os.Stat(parent); err != nil {
		if os.IsNotExist(err) {
			return usagef("-o target directory %q does not exist", parent)
		}
		return err
	} else if !fi.IsDir() {
		return usagef("-o target directory %q is not a directory", parent)
	}
	if overwrite {
		return nil
	}
	// Lstat (does not follow) so a dangling symlink, which Stat would report absent,
	// still counts as an entry the rename would destroy.
	if _, err := os.Lstat(output); err != nil {
		return nil // no entry at the target: a fresh write
	}
	inFi, ierr := os.Stat(inputReal)
	if ierr != nil {
		return nil // input missing/unstattable: the parse fails first, nothing written
	}
	if outFi, err := os.Stat(output); err == nil && os.SameFile(outFi, inFi) {
		return nil // the target resolves to the input: effectively in-place
	}
	return usagef("-o target %q already exists; pass --overwrite to replace it", output)
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
// the most-severe error class sets the exit code (worseError) while the remaining
// files still process.
// JSON output is always an array, one element per input; a multi-file text run
// ends with a one-line summary. With quiet (text mode only), the per-file plan and
// outcome are suppressed while errors and the summary remain, so a single-file
// `set -q` is silent on success. The returned error is alreadyRendered, preserving
// the exit class without rendering a second time.
func runSet(cmd *cobra.Command, paths []string, realOf func(string) string, ce *compiledEdit, output string, strict, quiet, verify bool) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	asJSON := jsonMode(cmd)
	// quiet is a text-mode presentation choice; under --json the stream shape is
	// fixed, so it has no effect there.
	quiet = quiet && !asJSON
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
	notifier := newSingleValuedNotifier(strict, asJSON)
	pnoter := newPaddingNoter(asJSON, errOut)
	var items []any
	var worstErr error
	changed, unchanged, failed, rendered := 0, 0, 0, 0

	fail := func(path string, err error) {
		if worseError(worstErr, err) {
			worstErr = err
		}
		failed++
		if asJSON {
			items = append(items, errorEntry(path, classifyError(err)))
		} else {
			perFileError(errOut, path, err)
		}
	}

	for _, path := range paths {
		doc, plan, err := ce.prepare(cmd.Context(), realOf(path), path)
		if err != nil {
			fail(path, err)
			continue
		}
		// Note once per format when a padding flag does not apply to it. Gated on
		// ce.paddingFlag so the Capabilities are not built when no flag was given.
		if ce.paddingFlag {
			pnoter.note(doc.Capabilities())
		}
		// Under --strict, a single-valued key given multiple values fails the file
		// before any write (a per-file usage error, exit 2); otherwise the write proceeds
		// and the plan report carries the single-valued-multi warning (the notifier no
		// longer prints a stderr note - the library attaches the signal to the report).
		// The strict failure is one array element so a multi-file run's aggregate exit
		// code stays order-independent (worseError), like every other per-file error - the
		// invocation-level unknown-key guardrail, which is file-independent, aborts up
		// front instead (notifyInvocationNotes).
		if err := notifier.check(plan); err != nil {
			fail(path, err)
			continue
		}
		// A verbatim -o copy of an unchanged file has no change plan worth previewing:
		// renderReport would print "no changes (already up to date)" only for the next
		// line to report it wrote a file. Suppress the preview and let renderSaveOutcome
		// print one honest line instead (L1). -o takes exactly one input, so this is
		// never mid-list.
		previewNoOp := output != "" && plan.IsNoOp()
		// Print the plan before the write so the preview is shown even if the write
		// then fails (the help promises this ordering); JSON aggregates instead, and
		// quiet suppresses the preview entirely.
		if !asJSON && !quiet && !previewNoOp {
			if rendered > 0 {
				fmt.Fprintln(out)
			}
			renderReport(out, path, plan, ce.addPics)
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
		// The essence is only re-read on a committed write (a no-op writes no temp), so
		// nothing is verified otherwise. Computed once and shared by the human and JSON
		// paths so they cannot disagree (#4).
		verified := verify && res.Committed
		if asJSON {
			items = append(items, toJSONSetResult(path, output, plan, res, verified))
		} else if !quiet {
			renderSaveOutcome(out, path, output, res, plan.IsNoOp())
			if verified {
				fmt.Fprintln(out, "Audio essence verified")
			}
		}
	}

	if asJSON {
		if err := emitJSONList(out, items); err != nil {
			return err
		}
	} else if len(paths) > 1 {
		// The blank line separates the summary from the per-file output above it;
		// under quiet there is none, so drop the separator to avoid a leading blank.
		if !quiet {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%d changed, %d unchanged, %d failed\n", changed, unchanged, failed)
	}
	return alreadyRendered(worstErr)
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
// or nothing for a no-op save-back. noOp is the plan's no-op status, used only for
// the -o path: a verbatim copy of an unchanged file prints one honest line (its
// change preview was suppressed upstream), with no leading blank since -o takes a
// single input and is never mid-list.
func renderSaveOutcome(w io.Writer, path, output string, res wl.SaveResult, noOp bool) {
	switch {
	case output != "" && noOp:
		fmt.Fprintf(w, "No metadata changes; wrote a verbatim copy to %s (%s)\n", output, wl.HumanBytes(res.Dest.Size))
	case output != "":
		fmt.Fprintf(w, "\nWrote %s (%s)\n", output, wl.HumanBytes(res.Dest.Size))
	case !res.Committed:
		fmt.Fprintf(w, "\nNo changes; %s left untouched\n", path)
	default:
		fmt.Fprintf(w, "\nSaved %s (%s)\n", path, wl.HumanBytes(res.Dest.Size))
	}
}

// jsonSetResult is the machine-readable outcome of a save: the plan plus where
// the bytes landed and whether they were committed. Verified is a pointer so it is
// present only when --verify was given and the write actually committed (so the
// audio essence was re-read and matched); a normal run omits it entirely rather
// than emit "verified": false, which would read like a failed check (#4).
type jsonSetResult struct {
	jsonReport
	Committed bool   `json:"committed"`
	Verified  *bool  `json:"verified,omitempty"`
	Output    string `json:"output,omitempty"`
	Size      int64  `json:"size"`
}

func toJSONSetResult(path, output string, plan *wl.Plan, res wl.SaveResult, verified bool) jsonSetResult {
	r := jsonSetResult{
		jsonReport: toJSONReport(path, plan),
		Committed:  res.Committed,
		Output:     output,
		Size:       res.Dest.Size,
	}
	// Emit verified only when verification actually ran (a committed --verify write),
	// so a normal run omits the field rather than showing "verified": false.
	if verified {
		t := true
		r.Verified = &t
	}
	return r
}
