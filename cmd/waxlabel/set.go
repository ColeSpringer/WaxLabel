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
			"nothing. With -o it writes a single complete new file, leaving the\n" +
			"original untouched (so -o takes exactly one input) - unless -o names the\n" +
			"input file itself, which is a deliberate in-place write that overwrites\n" +
			"it (no --overwrite needed for that self-target case). Because the write\n" +
			"is atomic (a temp file in -o's directory, then a rename onto it), -o must\n" +
			"name a regular file in a writable directory; it is not a discard sink, so\n" +
			"-o /dev/null fails - omit -o or use 'plan' to write nothing. Multiple files\n" +
			"are edited independently, each as its own atomic write; with --recursive,\n" +
			"directory arguments are walked for audio files. Because each file commits\n" +
			"on its own, a failure partway through a bulk or --recursive run leaves the\n" +
			"files already saved in place (it is not one transaction) - preview a bulk\n" +
			"edit with 'plan --recursive' first. A single \"-\" reads from standard input\n" +
			"and requires -o (editing standard input in place is meaningless). The plan\n" +
			"is printed before each outcome. Its warnings describe the write plan: what\n" +
			"the write changes, downgrades, or drops. Run 'lint' on the saved file to\n" +
			"check post-write metadata cleanliness. A 'set' with no edit flags is a usage\n" +
			"error (exit 2), since it is almost always a forgotten flag; to preview an\n" +
			"unedited file without writing, use 'plan <file>', which needs no edits.\n\n" +
			editPrecedenceHelp,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A present-but-empty -o is indistinguishable from cobra's unset default in
			// every downstream `output != ""` check, so `set f -o ''` would fall through
			// to an in-place save. Rejected once here, which keeps every later check valid.
			if cmd.Flags().Changed("output") && output == "" {
				return usagef("output path (-o) cannot be empty")
			}
			// Validate argument shape (pure, no I/O) before compile() reads any
			// --add-cover files, so a misuse like "set - --add-cover x" without -o
			// reports the actionable stdin usage error rather than a cover read error.
			if err := checkSetStdin(args, output); err != nil {
				return err
			}
			// Reject an explicitly-empty --preset/--legacy/--padding before the
			// no-edit check, so `set f --preset ''` reports the flag mistake directly.
			if err := rejectEmptyScalarFlags(cmd); err != nil {
				return err
			}
			// A set with no edit flags and no -o is almost always a forgotten edit flag, so
			// reject it instead of silently doing nothing. With -o it is a deliberate
			// verbatim copy.
			if output == "" && editFlagsEmpty(cmd) {
				return usagef("no edits given (use --set/--add/--clear/--add-cover/--add-chapter/...)")
			}
			// --overwrite only governs the -o gate that replaces an existing destination, so
			// with no -o it is a no-op. Noted rather than ignored, non-fatal, and on stderr
			// even under --json like the other exit-0 advisories.
			if overwrite && output == "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: --overwrite has no effect without -o")
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
			realOf, cleanup, err := readInputs(cmd.InOrStdin(), maxSizeFlag(cmd), args)
			if err != nil {
				return err
			}
			defer cleanup()

			paths, skipped, pathErrors, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			// Before the per-file edit notes: this is input discovery, useful even when the
			// walk then matches nothing to edit.
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
			// Only once there is a path to act on, so a note never claims a value was
			// "written" on a run the directory or empty-walk checks then abort.
			if err := notifyInvocationNotes(cmd.ErrOrStderr(), ce, &ef, realOf, paths, pathErrors, jsonMode(cmd)); err != nil {
				return err
			}
			// An unquoted value (--set TITLE=Two Words) leaves a stray positional beside the
			// real input, and set would write a truncated tag to each named file before the
			// stray word fails not-found. Refuse the whole run so a script cannot misread a
			// partial write as success.
			if err := refuseUnquotedValue(&ef, realOf, args, true); err != nil {
				return err
			}
			return runSet(cmd, paths, pathErrors, realOf, ce, output, ef.strict, quiet, verify)
		},
	}
	ef.bind(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to this path instead of editing the file in place (one input only); must name a regular file in a writable directory - it is not a discard sink (use 'plan' to preview without writing)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "allow -o to replace an existing destination file (by default an existing -o target is refused)")
	cmd.Flags().BoolVar(&verify, "verify", false, "after writing, verify the saved file's audio essence matches the source")
	cmd.Flags().BoolVar(&preserveMtime, "preserve-mtime", false, "keep the file's modification time (by default it is updated)")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, editing every audio file found (selected by file extension)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress the per-file plan and outcome (errors and the final summary still print); a single-file set is then silent on success")
	return markListCommand(cmd)
}

// checkOutputTarget validates the -o destination before any write. A directory is rejected up
// front, since the rename would fail EISDIR and leak a temp-file error --overwrite cannot fix.
// An existing entry needs --overwrite unless it resolves to the input itself (see
// sameWriteTarget). When inputReal does not exist the overwrite guard stays silent, so the
// more relevant not-found surfaces instead of an "already exists" naming the wrong operand.
func checkOutputTarget(output, inputReal string, overwrite bool) error {
	// "-" is the stdin/stdout sentinel, not a filename; streaming to stdout is the
	// library's WriteTo, a different model.
	if output == stdinArg {
		return usagef("-o - is not supported; set writes a named file")
	}
	// Stat follows symlinks, so a directory or a symlink to one is caught.
	if fi, err := os.Stat(output); err == nil && fi.IsDir() {
		return usagef("-o target %q is a directory, not a file", output)
	}
	// Before the plan renders, so a mistyped -o path fails up front instead of printing the
	// whole plan and then a late temp-create error. Checked even under --overwrite: a
	// missing parent dir cannot be overwritten.
	parent := filepath.Dir(output)
	if fi, err := os.Stat(parent); err != nil {
		// Exit 6, like every other missing path. The raw *fs.PathError gives the clean
		// not-found message.
		return err
	} else if !fi.IsDir() {
		// Exists but is a regular file, so a usage error rather than not-found.
		return usagef("-o target directory %q is not a directory", parent)
	}
	// Resolved once, writeAtomic's way, and threaded to both checks below so neither can
	// drift from the file and directory the write really lands on.
	resolved := wl.ResolveWriteTarget(output)
	// A FIFO, device, socket, or dangling symlink is refused even with --overwrite, which
	// means "replace an existing regular file", not "destroy a special node".
	if err := checkOutputRegular(output, resolved); err != nil {
		return err
	}
	// Policy before the writability probe, so the actionable "already exists" wins over an
	// unwritable-directory error and a refused invocation writes no probe temp file.
	if !overwrite {
		// Lstat does not follow, so a dangling symlink still counts as an entry the rename
		// would destroy.
		if _, err := os.Lstat(output); err == nil {
			// By canonical path rather than inode: a hardlink shares the inode but the
			// rename would break the link and leave the source bytes intact. A missing input
			// falls through so its parse reports the more relevant not-found.
			if _, ierr := os.Stat(inputReal); ierr == nil && !sameWriteTarget(output, inputReal) {
				return usagef("-o target %q already exists; pass --overwrite to replace the existing file", output)
			}
		}
	}
	// The same temp create writeAtomic performs, so an unwritable destination fails before
	// the plan is previewed rather than after.
	return checkOutputDirWritable(resolved)
}

// sameWriteTarget reports whether the -o output and the single input resolve to the file the
// atomic write would land on. A symlink or ./alias of the input compares equal, a genuine
// in-place -o; a hardlink does not, so it falls through to the "already exists" gate.
//
// Not the library's sameFileTarget: that one fails closed toward "same" to refuse a
// source-clobbering write, while this gate reads inverted (same skips the --overwrite prompt)
// and so must fail closed toward "different".
//
// An alias reads as in-place only as far as EvalSymlinks canonicalizes. Windows normalizes
// each component, so a case-fold alias works; macOS keeps the caller's spelling, and no
// platform collapses a bind mount, so there the write is refused pending --overwrite.
func sameWriteTarget(output, inputReal string) bool {
	return absOrClean(wl.ResolveWriteTarget(output)) == absOrClean(wl.ResolveWriteTarget(inputReal))
}

// absOrClean returns path made absolute, or the cleaned path when filepath.Abs cannot read
// the working directory. Mirrors the library's absResolved, so a degraded cwd still yields
// a best-effort comparison.
func absOrClean(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// checkOutputRegular refuses an -o target that is not a regular file or a symlink to one.
// writeAtomic renames its temp over resolved, so a special node would be silently replaced
// and a dangling link would leave a stray file at its non-existent target. A nonexistent
// target is a fresh write and allowed.
func checkOutputRegular(output, resolved string) error {
	li, err := os.Lstat(output)
	if err != nil {
		return nil // no entry at the literal path: a fresh write
	}
	if li.Mode()&os.ModeSymlink == 0 {
		if !li.Mode().IsRegular() {
			return usagef("-o target %q is not a regular file (a device, FIFO, or socket); choose another path", output)
		}
		return nil
	}
	// ResolveWriteTarget returns the literal path when a link cannot be resolved, so an
	// unchanged path here means a dangling link or a loop.
	if resolved == output {
		return usagef("-o target %q is a dangling symlink; point it at a regular file or choose another path", output)
	}
	if fi, serr := os.Stat(resolved); serr != nil {
		return usagef("-o target %q cannot be resolved: %v", output, serr)
	} else if !fi.Mode().IsRegular() {
		return usagef("-o target %q resolves to a non-regular file (a device, FIFO, or socket); choose another path", output)
	}
	return nil
}

// checkOutputDirWritable probes the resolved target's directory with the same temp create
// writeAtomic performs, so a read-only filesystem fails before the plan is previewed rather
// than as a late I/O error. It returns wl.NewTempCreateError, so the up-front and late
// errors read identically.
func checkOutputDirWritable(resolved string) error {
	dir := filepath.Dir(resolved)
	f, err := os.CreateTemp(dir, ".waxlabel-writecheck-*.tmp")
	if err != nil {
		return wl.NewTempCreateError(dir, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
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

// runSet applies the compiled edit to each path and saves it, previewing each plan before
// its write so a failure still shows what was attempted. The most-severe error class sets
// the exit code while the remaining files process. JSON is always an array; a multi-file
// text run ends with a summary. quiet suppresses the per-file plan and outcome, so a
// single-file `set -q` is silent on success.
func runSet(cmd *cobra.Command, paths []string, pathErrors map[string]error, realOf func(string) string, ce *compiledEdit, output string, strict, quiet, verify bool) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	asJSON := jsonMode(cmd)
	quiet = quiet && !asJSON // a text-mode choice; the JSON stream shape is fixed
	// Only reachable from a --recursive walk that matched nothing: cobra requires an
	// argument, -o rejects len != 1, and a nonexistent file fails per-file. Matches plan,
	// the dry-run twin: an advisory and exit 0 rather than a usage error.
	if len(paths) == 0 {
		noteNoFiles(errOut, paths, asJSON)
		if asJSON {
			return emitJSONList(out, nil)
		}
		return nil
	}
	gate := newStrictWarningGate(strict)
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
			items = append(items, errorEntry(path, err))
		} else {
			perFileError(errOut, path, err)
		}
	}

	for _, path := range paths {
		// A pre-flight failure expandPaths recorded, checked before any parse so the rest of
		// the batch still saves and a recorded FIFO is never opened, since its read would
		// block. The read commands use guardPathErrors; set has its own write loop.
		if e := pathErrors[path]; e != nil {
			fail(path, e)
			continue
		}
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
		// Under --strict an escalating plan warning fails the file before any write (exit 2);
		// otherwise the write proceeds and the report carries the warning. One array element,
		// like every other per-file error, so the aggregate exit stays order-independent.
		if err := gate.check(plan); err != nil {
			fail(path, err)
			continue
		}
		// A verbatim -o copy would print "no changes (already up to date)" only for the next
		// line to report it wrote a file, so suppress that preview and let renderSaveOutcome
		// print one honest line. A warning still shows, being the only signal the edit was
		// rejected. -o takes one input, so this is never mid-list.
		previewNoOp := output != "" && plan.IsNoOp() && len(plan.Report().Warnings) == 0
		// Before the write, so the help's promised ordering holds even when it then fails.
		if !asJSON && !quiet && !previewNoOp {
			if rendered > 0 {
				fmt.Fprintln(out)
			}
			renderReport(out, path, plan, ce.addPics)
			rendered++
		}
		dst := wl.SaveBack()
		if output != "" {
			// No transcoding, so a mismatched extension means a misnamed file. Non-fatal,
			// and suppressed under --json like every sibling stderr note.
			if !asJSON {
				warnExtensionMismatch(errOut, output, doc.Format())
			}
			dst = wl.SaveAsFile(output)
		}
		_, res, err := plan.Execute(cmd.Context(), dst)
		// Committed decides the outcome, not err: a post-commit failure leaves a changed
		// file, not a failed one (see writeFailed).
		if writeFailed(res, err) {
			fail(path, err)
			continue
		}
		// The file WRITTEN: under -o that is the output, while path is the untouched input.
		written := path
		if output != "" {
			written = output
		}
		warnPostCommit(errOut, asJSON, written, err)
		if res.Committed {
			changed++
		} else {
			unchanged++
		}
		// A no-op writes no temp, so nothing is verified. Computed once, so the human and
		// JSON paths cannot disagree.
		verified := verify && res.Committed
		if asJSON {
			items = append(items, toJSONSetResult(path, output, plan, res, verified, err))
		} else if !quiet {
			renderSaveOutcome(out, path, output, res, plan.IsNoOp(), wl.HasDiscardWarning(plan.Report().Warnings))
			if verified {
				fmt.Fprintln(out, "Output verified (audio essence + structure)")
			}
		}
	}

	if asJSON {
		if err := emitJSONList(out, items); err != nil {
			return err
		}
	} else if len(paths) > 1 {
		// Separates the summary from the per-file output; under quiet there is none.
		if !quiet {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%d changed, %d unchanged, %d failed\n", changed, unchanged, failed)
	}
	return alreadyRendered(worstErr)
}

// warnExtensionMismatch notes an output extension that does not match the source format,
// which without transcoding means a misnamed file. Advisory, so the write proceeds. A path
// with no extension, or an unknown format, is left alone.
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

// renderSaveOutcome reports where the bytes went: a new file, an in-place save, or nothing
// for a no-op save-back. noOp matters only for -o, where a verbatim copy prints one line with
// no leading blank, since -o takes a single input and is never mid-list.
//
// discarded says the plan wrote nothing because the edit was thrown away, not because the
// file already held it. It reads the same library predicate the plan line above does, so the
// two cannot say different things about one run.
func renderSaveOutcome(w io.Writer, path, output string, res wl.SaveResult, noOp, discarded bool) {
	switch {
	case output != "" && noOp && discarded:
		fmt.Fprintf(w, "Edit discarded; wrote a verbatim copy to %s (%s)\n", output, wl.HumanBytes(res.Dest.Size))
	case output != "" && noOp:
		fmt.Fprintf(w, "No metadata changes; wrote a verbatim copy to %s (%s)\n", output, wl.HumanBytes(res.Dest.Size))
	case output != "":
		fmt.Fprintf(w, "\nWrote %s (%s)\n", output, wl.HumanBytes(res.Dest.Size))
	case !res.Committed && discarded:
		fmt.Fprintf(w, "\nEdit discarded; %s left untouched\n", path)
	case !res.Committed:
		fmt.Fprintf(w, "\nNo changes; %s left untouched\n", path)
	default:
		fmt.Fprintf(w, "\nSaved %s (%s)\n", path, wl.HumanBytes(res.Dest.Size))
	}
}

// jsonSetResult is the machine-readable outcome of a save: the plan, where the bytes
// landed, and whether they committed. Verified is a pointer so a normal run omits it
// rather than emitting "verified": false, which would read like a failed check.
type jsonSetResult struct {
	jsonReport
	jsonPostWrite
	Committed bool   `json:"committed"`
	Verified  *bool  `json:"verified,omitempty"`
	Output    string `json:"output,omitempty"`
	Size      int64  `json:"size"`
}

func toJSONSetResult(path, output string, plan *wl.Plan, res wl.SaveResult, verified bool, postWrite error) jsonSetResult {
	r := jsonSetResult{
		jsonReport: toJSONReport(path, plan),
		Committed:  res.Committed,
		Output:     output,
		Size:       res.Dest.Size,
	}
	r.setPostWrite(postWrite)
	// Only when verification actually ran (a committed --verify write), so a normal run
	// omits the field rather than showing "verified": false.
	if verified {
		t := true
		r.Verified = &t
	}
	return r
}
