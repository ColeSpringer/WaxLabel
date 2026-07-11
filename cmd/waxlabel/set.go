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
			// A present-but-empty -o ("") is indistinguishable from cobra's unset
			// default in every downstream `output != ""` check, so a `set f -o ''`
			// would silently fall through to an in-place save-back and overwrite the
			// input. Reject it once here (before checkSetStdin), which guarantees a
			// present -o is non-empty and keeps every later check valid.
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
			// --overwrite only governs the -o gate that replaces an existing destination; with
			// no -o there is nothing to replace, so it is silently a no-op. Note that on stderr
			// (non-fatal, exit stays 0) rather than ignore it. Emitted on stderr even under --json,
			// like the other exit-0 advisories, so the JSON stdout array is untouched.
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
			// Report extension-skipped files up front, before per-file edit notes that
			// require work to do. This is input discovery, useful even when the walk then
			// matches nothing to edit.
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
			// directory or empty-walk checks then abort with nothing written.
			if err := notifyInvocationNotes(cmd.ErrOrStderr(), ce, &ef, realOf, paths, pathErrors, jsonMode(cmd)); err != nil {
				return err
			}
			// An unquoted value with spaces (--set TITLE=Two Words) leaves a stray bare-word
			// positional beside the real input; set would write a truncated tag to each named
			// file before the stray word fails not-found. Refuse the whole run up front (exit
			// 2, nothing written) so a script cannot misread a partial write as success. plan
			// refuses identically via the shared helper, but with the bare hint.
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

// checkOutputTarget validates the -o destination before any write. A directory can
// never be the target (the atomic rename fails EISDIR, and --overwrite cannot fix
// that), so it is rejected up front with a clear message rather than the leaked
// temp-file error a later rename would produce. An existing entry is refused unless
// --overwrite, except when it resolves to the single input itself (set f -o f is
// effectively in-place) - resolution is canonical-path equality (see sameWriteTarget),
// so a symlink or ./alias of the input is exempt, but a hardlink of it is not: the atomic
// rename would break the link and leave the source path's bytes intact, so a hardlink
// target must pass --overwrite like any unrelated file (this matches the library's own
// sameFileTarget definition). inputReal is realOf(paths[0]): when it does not exist the
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
	// Checked even under --overwrite: a missing parent dir cannot be overwritten.
	parent := filepath.Dir(output)
	if fi, err := os.Stat(parent); err != nil {
		// A missing (or otherwise unstattable) -o parent directory classifies as not-found /
		// I/O (exit 6), like every other missing path - so a script branching on exit codes
		// treats "-o typo/out.flac" the same as a missing input file. Returning the raw
		// *fs.PathError yields the clean "<dir>: no such file or directory" not-found message.
		// (It was usagef/exit-2 for the not-exist case before, inconsistent with the rest.)
		return err
	} else if !fi.IsDir() {
		// The parent EXISTS but is a regular file, not a missing path: still a usage error.
		return usagef("-o target directory %q is not a directory", parent)
	}
	// Resolve the symlink the way writeAtomic will, once, and thread it to both the
	// non-regular-target check and the writability probe so they inspect the exact file and
	// directory the write lands on - and cannot drift from writeAtomic's resolution rule.
	resolved := wl.ResolveWriteTarget(output)
	// Refuse a non-regular target (a FIFO, device, socket, or dangling symlink) even with
	// --overwrite: writeAtomic renames its temp over the resolved target, which would
	// silently destroy a special node or, for a dangling link, write a stray new file.
	// --overwrite is meant to replace an existing regular file, not a special path.
	if err := checkOutputRegular(output, resolved); err != nil {
		return err
	}
	// Resolve the existence/--overwrite policy BEFORE probing writability, so the actionable
	// "already exists; pass --overwrite" wins over an unwritable-directory error, and a refused
	// invocation (which writes nothing) does not touch the filesystem with a probe temp file.
	if !overwrite {
		// Lstat (does not follow) so a dangling symlink, which Stat would report absent,
		// still counts as an entry the rename would destroy.
		if _, err := os.Lstat(output); err == nil {
			// An entry exists. It is allowed only when it resolves to the single input (an
			// in-place -o), or the input is missing (the parse fails first and writes
			// nothing); otherwise refuse with the actionable hint. "Resolves to the input" is
			// canonical-path equality (sameWriteTarget), not inode identity: a hardlink of the
			// input shares its inode but has a distinct canonical path, and the atomic rename
			// would break the link and leave the source bytes intact, so it is refused here and
			// must pass --overwrite. The input-exists gate is kept so a missing input still falls
			// through (its parse fails first with the more-relevant not-found error).
			if _, ierr := os.Stat(inputReal); ierr == nil && !sameWriteTarget(output, inputReal) {
				return usagef("-o target %q already exists; pass --overwrite to replace the existing file", output)
			}
		}
	}
	// The write will proceed: probe that the resolved target's directory is writable now - the
	// same temp create writeAtomic performs - so an unwritable destination fails up front
	// rather than after the whole plan is previewed and only the late atomic write errors.
	return checkOutputDirWritable(resolved)
}

// sameWriteTarget reports whether the -o output and the single input resolve to the same file
// the atomic write would land on: canonical-path equality after write-target symlink
// resolution. A symlink to the input, or a relative/absolute alias of it, compares equal (a
// genuine in-place -o); a hardlink of the input - same inode but a distinct canonical path -
// does not, so it falls through to the "already exists; pass --overwrite" gate. inputReal is not
// pre-canonicalized, so the resolution here (via wl.ResolveWriteTarget, writeAtomic's own rule)
// is what makes ./f, an absolute/relative mix, and a symlink-to-input all compare equal.
//
// It is deliberately not the library's sameFileTarget, despite asking a near-identical question:
// that guard fails closed toward "same" so an unreliable compare refuses a source-clobbering
// write (protective for it), whereas this gate reads the answer inverted (same -> skip the
// --overwrite prompt), so it must fail closed toward "different" to still require --overwrite. On
// an Abs failure (a removed or inaccessible cwd) it therefore compares the cleaned paths -
// absResolved's own best effort - rather than calling the two equal: `set f -o f` still matches
// (identical cleaned path), but `set input -o existing` on two distinct paths no longer skips the
// gate to silently overwrite existing.
//
// The compare is on the canonical path string, so on a case-insensitive filesystem (macOS,
// Windows) or across a bind mount the same underlying file spelled two ways ("Foo.flac" vs
// "foo.flac", or /mnt/a/f vs a bind-mounted /mnt/b/f) resolves to two distinct strings and does
// not read as in-place: the write is refused pending --overwrite rather than silently allowed.
// That fails safe (no clobber, just an extra flag) and matches the library's sameFileTarget, which
// compares the same way. The prior os.SameFile inode check accepted those aliases, but it could
// not tell a case-fold alias from a hardlink - the distinction this gate exists to draw - so the
// string compare is the right trade.
func sameWriteTarget(output, inputReal string) bool {
	return absOrClean(wl.ResolveWriteTarget(output)) == absOrClean(wl.ResolveWriteTarget(inputReal))
}

// absOrClean returns path made absolute, or - when filepath.Abs cannot read the working
// directory - the cleaned (possibly still relative) path. It mirrors the library's absResolved so
// a degraded cwd yields a best-effort comparison instead of giving up.
func absOrClean(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// checkOutputRegular refuses an -o target that is not a regular file or a symlink that
// resolves to one: a FIFO, device, socket, or a dangling symlink. writeAtomic renames its
// temp over resolved (the wl.ResolveWriteTarget result the caller threads in), so a special
// node would be silently replaced by a regular file and a dangling link would leave a stray
// new file at its (non-existent) target. This is refused even under --overwrite, which
// replaces an existing regular file, not a special path. A target that does not exist (a
// fresh write) is allowed; the parent directory was already validated.
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
	// A symlink: it must resolve to a regular file. ResolveWriteTarget returns the literal
	// output path when the link cannot be resolved (a dangling link or a loop), so an unchanged
	// path signals an unresolvable symlink the write would turn into a stray file.
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

// checkOutputDirWritable probes that the resolved -o target's directory accepts a write, by
// creating and removing a temp file there - the operation writeAtomic performs to land its
// atomic rename. Running it here surfaces a read-only filesystem or a permission failure
// before the plan is previewed, instead of as a late I/O error after the whole preview
// printed. resolved is the wl.ResolveWriteTarget path, so the probe inspects the same
// directory writeAtomic places its temp in. On failure it returns the library's own
// wl.NewTempCreateError, so the up-front and late errors read identically.
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

// runSet applies the compiled edit to each path and saves it. Each file's plan is
// previewed before its write, so a failed write still shows what was attempted;
// the most-severe error class sets the exit code (worseError) while the remaining
// files still process.
// JSON output is always an array, one element per input; a multi-file text run
// ends with a one-line summary. With quiet (text mode only), the per-file plan and
// outcome are suppressed while errors and the summary remain, so a single-file
// `set -q` is silent on success. The returned error is alreadyRendered, preserving
// the exit class without rendering a second time.
func runSet(cmd *cobra.Command, paths []string, pathErrors map[string]error, realOf func(string) string, ce *compiledEdit, output string, strict, quiet, verify bool) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	asJSON := jsonMode(cmd)
	// quiet is a text-mode presentation choice; under --json the stream shape is
	// fixed, so it has no effect there.
	quiet = quiet && !asJSON
	// An empty path list is only reachable when a --recursive walk matched no audio
	// files: cobra requires >=1 argument, the -o path already rejects len != 1, and a
	// passed-through nonexistent file fails per-file with exit 6. Align with plan (the
	// dry-run twin): a "nothing to do" advisory and exit 0, with [] under --json, rather
	// than a usage error - so `plan DIR -r` and `set DIR -r` agree on the empty-walk
	// outcome. A directory arg WITHOUT --recursive is still a pre-flight usage error
	// (walk.go), so this is reached only for a genuine empty walk, never a misuse.
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
			items = append(items, errorEntry(path, classifyError(err)))
		} else {
			perFileError(errOut, path, err)
		}
	}

	for _, path := range paths {
		// A path expandPaths recorded as a pre-flight failure (a directory without
		// --recursive, or a directly-named FIFO/device) is this file's per-element error,
		// checked before any parse or write - so the rest of the batch still saves and a
		// recorded FIFO is never opened (its read would block). This mirrors the read
		// commands' guardPathErrors; set has its own write loop, so it checks inline.
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
		// Under --strict, an escalating plan warning (a single-valued key given multiple
		// values, or a value the format would drop) fails the file before any write (a
		// per-file usage error, exit 2); otherwise the write proceeds and the plan report
		// carries the warning for the human and JSON output (the gate prints no stderr note -
		// the library attaches the signal to the report). The strict failure is one array
		// element so a multi-file run's aggregate exit code stays order-independent
		// (worseError), like every other per-file error - the invocation-level unknown-key
		// guardrail, which is file-independent, aborts up front instead (notifyInvocationNotes).
		if err := gate.check(plan); err != nil {
			fail(path, err)
			continue
		}
		// A verbatim -o copy of an unchanged file has no change plan worth previewing:
		// renderReport would print "no changes (already up to date)" only for the next
		// line to report it wrote a file. Suppress that preview and let renderSaveOutcome
		// print one honest line instead - UNLESS the no-op carries a warning (a
		// value-dropped edit whose value the format could not store), which is the only
		// signal the edit was rejected and so must still be shown. -o takes exactly one
		// input, so this is never mid-list.
		previewNoOp := output != "" && plan.IsNoOp() && len(plan.Report().Warnings) == 0
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
			// misnamed file; warn (on stderr, non-fatally) but still write. Gated on the
			// non-JSON path: under --json the sibling stderr notes are suppressed too, so this
			// one must be as well, keeping the machine run's stderr clean.
			if !asJSON {
				warnExtensionMismatch(errOut, output, doc.Format())
			}
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
		// paths so they cannot disagree.
		verified := verify && res.Committed
		if asJSON {
			items = append(items, toJSONSetResult(path, output, plan, res, verified))
		} else if !quiet {
			renderSaveOutcome(out, path, output, res, plan.IsNoOp())
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
// than emit "verified": false, which would read like a failed check.
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
