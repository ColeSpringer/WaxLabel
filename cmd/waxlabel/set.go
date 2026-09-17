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

// newSetCmd builds the "set" command: apply edits and write the result.
// Default: in-place atomic rewrite (no-op writes nothing). With -o: one output file.
// Multiple inputs (or --recursive directories) are edited independently.
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
			// Empty -o matches unset in every `output != ""` check; reject so
			// `set f -o ''` cannot fall through to in-place save.
			if cmd.Flags().Changed("output") && output == "" {
				return usagef("output path (-o) cannot be empty")
			}
			// Validate args before compile reads --add-cover, so "set - --add-cover x"
			// without -o gets the stdin usage error, not a cover read error.
			if err := checkSetStdin(args, output); err != nil {
				return err
			}
			// Reject explicitly-empty --preset/--legacy/--padding before the no-edit check.
			if err := rejectEmptyScalarFlags(cmd); err != nil {
				return err
			}
			// No edits and no -o is almost always a forgotten flag. With -o, verbatim copy is intentional.
			if output == "" && editFlagsEmpty(cmd) {
				return usagef("no edits given (use --set/--add/--clear/--add-cover/--add-chapter/...)")
			}
			// --overwrite only gates -o destination replacement; without -o it is a no-op.
			// Non-fatal advisory on stderr, including under --json.
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

			paths, skipped, leftovers, pathErrors, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			// Input-discovery notes; useful when the walk matches nothing.
			noteSkipped(cmd.ErrOrStderr(), skipped, jsonMode(cmd))
			noteLeftovers(cmd.ErrOrStderr(), leftovers, jsonMode(cmd))
			// Unreadable directory paths are reported but do not count as inputs.
			inputs := namedInputs(paths, pathErrors)
			if output != "" && len(inputs) != 1 {
				return usagef("-o writes a single file, so it takes exactly one input (got %d)", len(inputs))
			}
			// Validate -o before any write.
			if output != "" {
				if err := checkOutputTarget(output, realOf(inputs[0]), overwrite); err != nil {
					return err
				}
			}
			// Defer notes until a path exists; avoids claiming a write on an abort.
			if err := notifyInvocationNotes(cmd.ErrOrStderr(), ce, &ef, realOf, paths, pathErrors, jsonMode(cmd)); err != nil {
				return err
			}
			// Unquoted value (--set TITLE=Two Words) leaves a stray positional; refuse the
			// run so a script cannot treat partial writes as success.
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

// checkOutputTarget validates -o before any write. Rejects directories (rename fails EISDIR).
// Existing entries need --overwrite unless sameWriteTarget says in-place. When inputReal is
// missing, overwrite stays silent so input not-found surfaces first.
func checkOutputTarget(output, inputReal string, overwrite bool) error {
	// "-" is stdin/stdout sentinel, not an output path.
	if output == stdinArg {
		return usagef("-o - is not supported; set writes a named file")
	}
	// Stat follows symlinks; catches directories and symlinks to directories.
	if fi, err := os.Stat(output); err == nil && fi.IsDir() {
		return usagef("-o target %q is a directory, not a file", output)
	}
	// Fail before plan render; checked even under --overwrite (parent cannot be overwritten).
	parent := filepath.Dir(output)
	if fi, err := os.Stat(parent); err != nil {
		// Exit 6 like other missing paths.
		return err
	} else if !fi.IsDir() {
		// Exists but is not a directory.
		return usagef("-o target directory %q is not a directory", parent)
	}
	// Resolve once (writeAtomic's way) for both checks below.
	resolved := wl.ResolveWriteTarget(output)
	// Refuse FIFO/device/socket/dangling symlink; --overwrite means replace a regular file.
	if err := checkOutputRegular(output, resolved); err != nil {
		return err
	}
	// Policy before writability probe so "already exists" wins over unwritable directory.
	if !overwrite {
		// Lstat: dangling symlink still counts as an existing entry.
		if _, err := os.Lstat(output); err == nil {
			// Canonical path, not inode: hardlink shares inode but rename would break it.
			// Missing input falls through to input parse not-found.
			if _, ierr := os.Stat(inputReal); ierr == nil && !sameWriteTarget(output, inputReal) {
				return usagef("-o target %q already exists; pass --overwrite to replace the existing file", output)
			}
		}
	}
	// Same temp create as writeAtomic; unwritable destination fails before plan preview.
	return checkOutputDirWritable(resolved)
}

// sameWriteTarget: -o and input resolve to the same write target (symlink/./ ok).
// Hardlink does not; falls through to "already exists".
//
// Unlike library sameFileTarget (fails closed toward "same" to block clobbering),
// this gate fails closed toward "different" because same skips the --overwrite prompt.
//
// Alias match only where EvalSymlinks canonicalizes. Windows folds case; macOS keeps
// spelling; bind mounts are not collapsed, so write is refused pending --overwrite.
func sameWriteTarget(output, inputReal string) bool {
	return absOrClean(wl.ResolveWriteTarget(output)) == absOrClean(wl.ResolveWriteTarget(inputReal))
}

// absOrClean: absolute path, or cleaned path if Abs cannot read cwd.
// Mirrors library absResolved for degraded-cwd comparisons.
func absOrClean(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// checkOutputRegular refuses -o targets that are not regular files (or symlinks to one).
// writeAtomic renames temp over resolved; special nodes would be replaced, dangling links
// would leave a stray file. Missing target is allowed.
func checkOutputRegular(output, resolved string) error {
	li, err := os.Lstat(output)
	if err != nil {
		return nil // no entry: fresh write
	}
	if li.Mode()&os.ModeSymlink == 0 {
		if !li.Mode().IsRegular() {
			return usagef("-o target %q is not a regular file (a device, FIFO, or socket); choose another path", output)
		}
		return nil
	}
	// Unresolved symlink: ResolveWriteTarget returns literal path unchanged.
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

// checkOutputDirWritable probes the target directory with writeAtomic's temp create.
// Returns wl.NewTempCreateError so up-front and late errors match.
func checkOutputDirWritable(resolved string) error {
	dir := filepath.Dir(resolved)
	f, err := os.CreateTemp(dir, wl.TempFilePrefix+"writecheck-*"+wl.TempFileSuffix)
	if err != nil {
		return wl.NewTempCreateError(dir, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// checkSetStdin: "-" is one input only and requires -o.
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

// runSet applies edits per path, previewing each plan before its write.
// worstError sets exit class; batch continues. quiet hides per-file text on success.
func runSet(cmd *cobra.Command, paths []string, pathErrors map[string]error, realOf func(string) string, ce *compiledEdit, output string, strict, quiet, verify bool) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	asJSON := jsonMode(cmd)
	quiet = quiet && !asJSON // text-mode only; JSON shape is fixed
	// --recursive walk matched nothing: advisory, exit 0 (matches plan).
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
		// Pre-flight expandPaths error: before parse so batch continues and recorded
		// FIFOs are never opened (would block).
		if e := pathErrors[path]; e != nil {
			fail(path, e)
			continue
		}
		doc, plan, err := ce.prepare(cmd.Context(), realOf(path), path)
		if err != nil {
			fail(path, err)
			continue
		}
		// Once per format when padding flag does not apply; skip Capabilities when unused.
		if ce.paddingFlag {
			pnoter.note(doc.Capabilities())
		}
		// --strict: escalating plan warning fails file before write (exit 2).
		// One array element per file so aggregate exit is order-independent.
		if err := gate.check(plan); err != nil {
			fail(path, err)
			continue
		}
		// Verbatim -o copy: suppress "no changes" preview; renderSaveOutcome prints one line.
		previewNoOp := output != "" && plan.IsNoOp() && len(plan.Report().Warnings) == 0
		// Plan before write (help's promised ordering).
		if !asJSON && !quiet && !previewNoOp {
			if rendered > 0 {
				fmt.Fprintln(out)
			}
			renderReport(out, path, plan, ce.addPics)
			rendered++
		}
		dst := wl.SaveBack()
		if output != "" {
			// Extension mismatch advisory; suppressed under --json.
			if !asJSON {
				warnExtensionMismatch(errOut, output, doc.Format())
			}
			dst = wl.SaveAsFile(output)
		}
		_, res, err := plan.Execute(cmd.Context(), dst)
		// Outcome follows res.Committed, not err (post-commit failure != writeFailed).
		if writeFailed(res, err) {
			fail(path, err)
			continue
		}
		// Display path: under -o this is output; path is untouched input.
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
		// No-op writes no temp, so nothing to verify. Computed once for text/JSON parity.
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
		// Blank line before multi-file summary; omitted when quiet.
		if !quiet {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%d changed, %d unchanged, %d failed\n", changed, unchanged, failed)
	}
	return alreadyRendered(worstErr)
}

// warnExtensionMismatch notes extension/format mismatch (no transcoding). Advisory.
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

// renderSaveOutcome reports where bytes went. noOp/discarded only affect -o copy lines.
// discarded uses the same predicate as the plan line above.
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

// jsonSetResult: machine save outcome. Verified is *bool so absent omits, not false.
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
	// Set Verified only when --verify ran on a committed write.
	if verified {
		t := true
		r.Verified = &t
	}
	return r
}
