package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
	"github.com/spf13/cobra"
)

// errLintFindings: worst finding is warning-level. Exit 1 (diff convention).
// alreadyRendered; structural errors outrank and keep their class.
var errLintFindings = errors.New("issues found")

// errLintErrorFindings: error-severity finding, no structural error. Wraps ErrInvalidData
// (exit 4). Outranks not-found in multi-file runs; loses to canceled.
var errLintErrorFindings = fmt.Errorf("%w: lint found an invalid or contradictory state", waxerr.ErrInvalidData)

// newLintCmd builds the "lint" command: report metadata issues; with --fix, apply
// safe non-destructive remediations and save.
func newLintCmd() *cobra.Command {
	var fix bool
	var recursive bool
	cmd := &cobra.Command{
		Use:   "lint <file>...",
		Short: "Report metadata issues (and optionally fix the safe ones)",
		Example: "  waxlabel lint song.mp3\n" +
			"  waxlabel lint --fix --recursive album/",
		Long: "Inspect each file for issues a tagger would want to surface: stale legacy\n" +
			"tag containers, inherited encoder stamps, conflicting source values,\n" +
			"duplicate or unrecognized pictures, chapters that collide or start past the\n" +
			"audio, a chained Ogg stream, malformed dates, a tag entry no reader can\n" +
			"interpret, a chunk whose size the container leaves unknown, and missing\n" +
			"audio.\n" +
			"Exit code 0 means clean and 1 means warning-level issues were found. An\n" +
			"error-severity finding - missing audio, a duplicate tag block, multiple\n" +
			"Vorbis comment blocks, or a duplicate picture icon - exits 4 (invalid-data),\n" +
			"the same class a corrupt or unparseable file gives, since the metadata is in\n" +
			"a contradictory state; it outranks a wrong path in a multi-file run. A\n" +
			"structural parse/IO error keeps its own (higher) exit class.\n\n" +
			"With --fix, apply only the safe, non-destructive remediations - clearing\n" +
			"the encoder stamp and stripping legacy containers that are fully redundant\n" +
			"with the canonical tags - then save in place, reporting what changed. A\n" +
			"legacy container holding a value or content that lives nowhere else is kept.\n" +
			"A rewrite still cannot carry a region no parser could read, so anything it\n" +
			"destroys on the way is reported as lost in the rewrite.\n" +
			"Pictures are never dropped automatically; every finding --fix does not\n" +
			"address is reported as \"not auto-fixed\". With\n" +
			"--recursive, directory arguments are walked for audio files. A single\n" +
			"\"-\" reads from standard input (read-only; not valid with --fix).",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, skipped, leftovers, pathErrors, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			// --fix with no files is usage error (before noteNoFiles avoids double message).
			if fix && len(paths) == 0 {
				return usagef("no audio files found")
			}
			noteNoFiles(cmd.ErrOrStderr(), paths, jsonMode(cmd))
			noteSkipped(cmd.ErrOrStderr(), skipped, jsonMode(cmd))
			noteLeftovers(cmd.ErrOrStderr(), leftovers, jsonMode(cmd))
			if fix {
				if slices.Contains(paths, stdinArg) {
					return usagef("cannot fix standard input; --fix writes changes back to a file")
				}
				return runLintFix(cmd, paths, pathErrors)
			}
			return runLint(cmd, paths, pathErrors)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply the safe, non-destructive fixes and save in place")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, linting every audio file found (selected by file extension)")
	return markListCommand(cmd)
}

// lintLoop: shared per-file loop for runLint and runLintFix.
// Keeps most-severe structural error. Finding severity folds into worseError so exit 4
// outranks another file's not-found (exit 6).
func lintLoop[T any](
	cmd *cobra.Command,
	paths []string,
	compute func(ctx context.Context, path string) (T, error),
	severity func(T) wl.LintSeverity,
	jsonItem func(path string, t T) any,
	render func(w io.Writer, path string, t T),
) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	asJSON := jsonMode(cmd)
	var items []any
	var worstErr error
	var maxSev wl.LintSeverity
	rendered := 0
	for _, path := range paths {
		t, err := compute(cmd.Context(), path)
		if err != nil {
			// Broken pipe: stop silently. isPipeClose gates so a real file error still records.
			if errors.Is(context.Cause(cmd.Context()), errBrokenPipe) && isPipeClose(err) {
				break
			}
			if worseError(worstErr, err) {
				worstErr = err
			}
			if asJSON {
				items = append(items, errorEntry(path, err))
			} else {
				perFileError(errOut, path, err)
			}
			continue
		}
		maxSev = max(maxSev, severity(t))
		if asJSON {
			items = append(items, jsonItem(path, t))
		} else {
			if rendered > 0 {
				fmt.Fprintln(out)
			}
			render(out, path, t)
			rendered++
		}
	}
	// Fold into worstErr before JSON write so pipe close cannot drop finding exit class.
	var findingErr error
	switch {
	case maxSev >= wl.LintError:
		findingErr = errLintErrorFindings // exit 4
	case maxSev >= wl.LintWarning:
		findingErr = errLintFindings // exit 1
	}
	if findingErr != nil && worseError(worstErr, findingErr) {
		worstErr = findingErr
	}
	if asJSON {
		if werr := emitJSONList(out, items); werr != nil {
			// Preserve worstErr over emit failure (e.g. broken pipe vs exit 4).
			if worstErr != nil {
				return alreadyRendered(worstErr)
			}
			return werr
		}
	}
	return alreadyRendered(worstErr)
}

// worstFinding: max severity among findings, or LintInfo when none.
func worstFinding(findings []wl.Finding) wl.LintSeverity {
	var worst wl.LintSeverity
	for _, f := range findings {
		worst = max(worst, f.Severity)
	}
	return worst
}

// runLint reports findings per file.
func runLint(cmd *cobra.Command, paths []string, pathErrors map[string]error) error {
	realOf, cleanup, err := readInputs(cmd.InOrStdin(), maxSizeFlag(cmd), paths)
	if err != nil {
		return err
	}
	defer cleanup()
	return lintLoop(cmd, paths,
		guardPathErrors(pathErrors, func(ctx context.Context, path string) ([]wl.Finding, error) {
			doc, err := parseInput(ctx, realOf(path), path)
			if err != nil {
				return nil, err
			}
			return doc.Lint(), nil
		}),
		worstFinding,
		func(path string, findings []wl.Finding) any { return toJSONLint(path, findings) },
		renderLint,
	)
}

// renderLint prints one file's findings, one per line, or "no issues" when clean.
func renderLint(w io.Writer, path string, findings []wl.Finding) {
	fmt.Fprintf(w, "%s\n", displayName(path))
	if len(findings) == 0 {
		fmt.Fprintln(w, "  no issues")
		return
	}
	for _, f := range findings {
		// Finding.String self-sanitizes file-derived text.
		fmt.Fprintf(w, "  %s\n", f.String())
	}
}

// runLintFix: apply safe remediations per file and save. Remaining warnings still exit 1.
func runLintFix(cmd *cobra.Command, paths []string, pathErrors map[string]error) error {
	errOut, asJSON := cmd.ErrOrStderr(), jsonMode(cmd)
	// Post-commit failure is a note on the outcome, not the error return.
	fixOne := func(ctx context.Context, path string) (fixOutcome, error) {
		o, err := lintFixOne(ctx, path)
		warnPostCommit(errOut, asJSON, path, o.postWrite)
		return o, err
	}
	return lintLoop(cmd, paths,
		guardPathErrors(pathErrors, fixOne),
		func(o fixOutcome) wl.LintSeverity { return worstFinding(o.remaining) },
		func(path string, o fixOutcome) any { return toJSONLintFix(o) },
		func(w io.Writer, path string, o fixOutcome) { renderLintFix(w, o) },
	)
}

// fixOutcome is one file's lint --fix result.
type fixOutcome struct {
	path       string
	changes    []tag.Change
	operations []string
	remaining  []wl.Finding
	committed  bool
	// lost: write-loss warnings from the fix plan. Only on committed save.
	lost []wl.Warning
	// postWrite: step failed after commit; note on success, not error return.
	postWrite error
}

// lintFixOne parses, applies safe remediation, saves, re-lints.
// Re-lint keeps "remaining" honest (e.g. encoder stamp in vendor string survives Clear).
func lintFixOne(ctx context.Context, path string) (fixOutcome, error) {
	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		return fixOutcome{}, err
	}
	// No-audio: Prepare returns opaque ErrInvalidData; short-circuit to "not auto-fixed".
	// Gate on WarnNoAudioFrames, not empty fix plan (file may be fixable too).
	for _, w := range doc.Warnings() {
		if w.Code == wl.WarnNoAudioFrames {
			return fixOutcome{path: path, remaining: doc.Lint(), committed: false}, nil
		}
	}
	fix := doc.PlanLintFix()
	plan, err := doc.Edit().Apply(fix.Patch).Prepare(fix.Options...)
	if err != nil {
		return fixOutcome{}, err
	}
	_, res, err := plan.Execute(ctx, wl.SaveBack())
	// Outcome follows res.Committed, not err (see writeFailed).
	if writeFailed(res, err) {
		return fixOutcome{}, err
	}
	postWrite := err // named: struct literal below is past other err assignments
	// Committed: re-parse for post-fix state. Uncommitted: doc.Lint() still valid.
	var remaining []wl.Finding
	if res.Committed {
		after, err := wl.ParseFile(ctx, path)
		if err != nil {
			return fixOutcome{}, err
		}
		remaining = after.Lint()
	} else {
		remaining = doc.Lint()
	}
	// No-op plan carries NoOpPlan sentinel in operations; clear so JSON/render stay honest.
	operations := plan.Report().Operations
	if plan.IsNoOp() {
		operations = nil
	}
	// Rewrite may destroy unreadable bytes re-lint cannot see; surface plan write-loss
	// warnings (strictEscalatingCodes).
	var lost []wl.Warning
	if res.Committed {
		for _, w := range plan.Report().Warnings {
			if strictEscalatingCodes[w.Code] {
				lost = append(lost, w)
			}
		}
	}
	return fixOutcome{
		path:       path,
		changes:    plan.Changes(),
		operations: operations,
		remaining:  remaining,
		committed:  res.Committed,
		lost:       lost,
		postWrite:  postWrite,
	}, nil
}

// renderLintFix prints one file's --fix result.
func renderLintFix(w io.Writer, o fixOutcome) {
	// --fix rejects "-"; SanitizeLine blocks hostile path forgery.
	name := tag.SanitizeLine(o.path)
	fmt.Fprintf(w, "%s\n", name)
	// Legacy strip is structural with no field change; both lists empty means nothing to fix.
	if len(o.changes) == 0 && len(o.operations) == 0 {
		fmt.Fprintln(w, "  nothing to fix")
	} else {
		fmt.Fprintln(w, "  fixed:")
		for _, c := range o.changes {
			renderChangeLine(w, "    ", c)
		}
		// No leading dash: "- KEY" below means removed key.
		for _, op := range o.operations {
			fmt.Fprintf(w, "    %s\n", op)
		}
	}
	for _, f := range o.remaining {
		// Finding.String self-sanitizes (see renderLint).
		fmt.Fprintf(w, "  not auto-fixed: %s\n", f.String())
	}
	for _, x := range o.lost {
		// Warning.String self-sanitizes.
		fmt.Fprintf(w, "  lost in the rewrite: %s\n", x.String())
	}
	if o.committed {
		fmt.Fprintf(w, "  saved %s\n", name)
	} else {
		// Remaining findings listed above.
		fmt.Fprintf(w, "  left unchanged\n")
	}
}

// jsonLint: machine lint result. Error matches jsonErrorEntry for uniform decode.
type jsonLint struct {
	SchemaVersion int           `json:"schemaVersion"`
	File          string        `json:"file"`
	Error         *jsonErrBody  `json:"error,omitempty"`
	Findings      []jsonFinding `json:"findings"`
}

type jsonFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Key      string `json:"key,omitempty"`
	// Fixable: lint --fix handles this finding.
	Fixable bool `json:"fixable"`
}

// jsonLintFix: machine lint --fix result. Remaining is post-fix lint. Error matches jsonLint.
type jsonLintFix struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file"`
	Error         *jsonErrBody `json:"error,omitempty"`
	// Changes: tag diff. Operations: structural write list (see jsonReport).
	Changes    []jsonChange  `json:"changes"`
	Operations []string      `json:"operations"`
	Remaining  []jsonFinding `json:"remaining"`
	// Lost: rewrite destroyed on the way (strictEscalatingCodes). Usually empty.
	Lost      []jsonWarning `json:"lost"`
	Committed bool          `json:"committed"`
	jsonPostWrite
}

func toJSONLint(path string, findings []wl.Finding) jsonLint {
	return jsonLint{
		SchemaVersion: schemaVersion,
		File:          jsonFileName(path),
		Findings:      toJSONFindings(findings),
	}
}

func toJSONLintFix(o fixOutcome) jsonLintFix {
	// nonNil: operations serializes as [], not null.
	j := jsonLintFix{
		SchemaVersion: schemaVersion,
		File:          jsonFileName(o.path),
		Changes:       toJSONChanges(o.changes),
		Operations:    nonNil(o.operations),
		Remaining:     toJSONFindings(o.remaining),
		Lost:          toJSONWarnings(o.lost),
		Committed:     o.committed,
	}
	j.setPostWrite(o.postWrite)
	return j
}

// toJSONWarnings: never nil; serializes as [].
func toJSONWarnings(ws []wl.Warning) []jsonWarning {
	out := make([]jsonWarning, 0, len(ws))
	for _, w := range ws {
		out = append(out, jsonWarning{Code: w.Code.String(), Message: w.Message})
	}
	return out
}

// toJSONFindings: shared by lint and lint --fix.
func toJSONFindings(findings []wl.Finding) []jsonFinding {
	out := make([]jsonFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, jsonFinding{
			Severity: f.Severity.String(),
			Code:     f.Code,
			Message:  f.Message,
			Key:      string(f.Key),
			Fixable:  f.Fixable,
		})
	}
	return out
}
