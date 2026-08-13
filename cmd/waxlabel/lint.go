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

// errLintFindings is returned when a file's worst finding is a warning. Unclassified, so
// it exits 1, the diff(1) convention for "issues found", and already-rendered so no error
// line prints over the findings. A structural error outranks it and keeps its own class.
var errLintFindings = errors.New("issues found")

// errLintErrorFindings is returned for an error-severity finding with no structural
// error. It wraps ErrInvalidData for exit 4, the class verify gives a no-audio file, so
// "valid but contradictory metadata" is distinct from a mere warning. Through worseError
// it outranks a not-found in a multi-file run, while still losing to canceled.
var errLintErrorFindings = fmt.Errorf("%w: lint found an invalid or contradictory state", waxerr.ErrInvalidData)

// newLintCmd builds the "lint" command, which reports metadata issues (stale
// legacy tags, encoder noise, conflicting families, bad pictures, malformed
// dates, missing audio) and, with --fix, applies the safe non-destructive
// remediations and saves.
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
			"duplicate or unrecognized pictures, malformed dates, and missing audio.\n" +
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
			"Pictures are never dropped automatically; every finding --fix does not\n" +
			"address is reported as \"not auto-fixed\". With\n" +
			"--recursive, directory arguments are walked for audio files. A single\n" +
			"\"-\" reads from standard input (read-only; not valid with --fix).",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, skipped, pathErrors, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			// Matching no files is an error only for the mutating --fix path, so a script
			// cannot read "nothing happened" as success. Before noteNoFiles, so the error
			// prints once rather than doubled by the note.
			if fix && len(paths) == 0 {
				return usagef("no audio files found")
			}
			noteNoFiles(cmd.ErrOrStderr(), paths, jsonMode(cmd))
			noteSkipped(cmd.ErrOrStderr(), skipped, jsonMode(cmd))
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

// lintLoop runs a lint-style per-file command: perFile plus a finding accumulator and
// lint's exit contract, so runLint and runLintFix differ only in their helpers. It keeps
// the most-severe structural error, not the first seen.
//
// The finding severity folds into the SAME worseError comparison as a structural error,
// not gated behind "no structural error": an error-severity finding (exit 4) must outrank
// another file's not-found (exit 6), since a broken file beats a wrong path.
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
			if worseError(worstErr, err) {
				worstErr = err
			}
			if asJSON {
				items = append(items, errorEntry(path, classifyError(err)))
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
	if asJSON {
		if err := emitJSONList(out, items); err != nil {
			return err
		}
	}
	// Folded in alongside any structural error, so the aggregate exit reflects the
	// most-severe class overall.
	var findingErr error
	switch {
	case maxSev >= wl.LintError:
		findingErr = errLintErrorFindings // invalid-data, exit 4
	case maxSev >= wl.LintWarning:
		findingErr = errLintFindings // plain, exit 1
	}
	if findingErr != nil && worseError(worstErr, findingErr) {
		worstErr = findingErr
	}
	return alreadyRendered(worstErr)
}

// worstFinding returns the most-severe severity among a file's findings, LintInfo when
// there are none. lintLoop applies the thresholds itself, so a sub-threshold LintInfo
// finding never makes a run non-clean.
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
		// Message and key can be file-derived, but Finding.String self-sanitizes.
		fmt.Fprintf(w, "  %s\n", f.String())
	}
}

// runLintFix applies the safe remediations to each file and saves, reporting the changes
// and what still remains. A remaining warning-or-worse finding still yields exit 1.
func runLintFix(cmd *cobra.Command, paths []string, pathErrors map[string]error) error {
	errOut, asJSON := cmd.ErrOrStderr(), jsonMode(cmd)
	// A post-commit failure rides the outcome rather than the error return, so the note
	// is emitted here, where stderr is in scope.
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

// fixOutcome is one file's lint --fix result: the changes applied, the structural
// operations performed, what still remains, and whether the save committed.
type fixOutcome struct {
	path       string
	changes    []tag.Change
	operations []string
	remaining  []wl.Finding
	committed  bool
	// postWrite is a step that failed after the bytes were committed, so it travels as a
	// note on a successful outcome rather than an error.
	postWrite error
}

// lintFixOne parses path, applies the safe remediation, saves in place, then re-lints.
// Re-linting rather than trusting the fixer's intent keeps the report honest: a
// transcoder stamp in a native vendor string survives Clear(ENCODER), so "remaining" is
// whatever a fresh lint would now show.
func lintFixOne(ctx context.Context, path string) (fixOutcome, error) {
	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		return fixOutcome{}, err
	}
	// Prepare refuses every no-audio write with an opaque ErrInvalidData, which would
	// surface as an error envelope instead of the finding. Short-circuit so no-audio
	// routes into the same graceful "not auto-fixed" path as every other unfixable
	// finding. Gated on the warning rather than an empty fix plan, which would miss a
	// no-audio file that also carries a fixable finding.
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
	// Committed decides the outcome, not err (see writeFailed).
	if writeFailed(res, err) {
		return fixOutcome{}, err
	}
	postWrite := err // named: the struct literal below is past several other err assignments
	// A committed save needs a re-parse for the true post-fix state. An uncommitted one
	// left the file byte-identical, so doc.Lint() still holds and re-parsing would
	// needlessly re-hash every embedded picture.
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
	// A no-op plan stamps operations with core.NoOpPlan, a sentinel rather than a real
	// step, which would suppress renderLintFix's "nothing to fix" branch and leak "no
	// changes" into the JSON array. A committed legacy-strip is not a no-op, so its real
	// operations survive (README: non-empty operations means bytes were written).
	operations := plan.Report().Operations
	if plan.IsNoOp() {
		operations = nil
	}
	return fixOutcome{
		path:       path,
		changes:    plan.Changes(),
		operations: operations,
		remaining:  remaining,
		committed:  res.Committed,
		postWrite:  postWrite,
	}, nil
}

// renderLintFix prints what --fix did to one file: the fields it changed (or
// "nothing to fix"), the findings it left for the user, and the save outcome.
func renderLintFix(w io.Writer, o fixOutcome) {
	// --fix rejects "-", so o.path is always a real file and needs no "<stdin>" relabel.
	// Still escaped, so a hostile filename cannot forge a fake "saved /etc/passwd" line.
	name := tag.SanitizeLine(o.path)
	fmt.Fprintf(w, "%s\n", name)
	// A legacy-container strip is a structural operation with no field change, so both
	// lists must be empty or the strip would go unreported.
	if len(o.changes) == 0 && len(o.operations) == 0 {
		fmt.Fprintln(w, "  nothing to fix")
	} else {
		fmt.Fprintln(w, "  fixed:")
		for _, c := range o.changes {
			renderChangeLine(w, "    ", c)
		}
		// Glyph-free: these sit below the change lines, where "- KEY" means a removed key,
		// so a dash here would read as another removal rather than a structural step.
		for _, op := range o.operations {
			fmt.Fprintf(w, "    %s\n", op)
		}
	}
	for _, f := range o.remaining {
		// Finding.String self-sanitizes the file-derived text (see renderLint).
		fmt.Fprintf(w, "  not auto-fixed: %s\n", f.String())
	}
	if o.committed {
		fmt.Fprintf(w, "  saved %s\n", name)
	} else {
		// Not "clean": any remaining findings are already listed above.
		fmt.Fprintf(w, "  left unchanged\n")
	}
}

// jsonLint is the machine-readable lint result for one file. The Error field matches
// jsonErrorEntry, so a consumer can decode every array element into this one struct.
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
}

// jsonLintFix is the machine-readable lint --fix result for one file. Remaining holds
// what a fresh lint of the saved file still reports. The Error field matches
// jsonErrorEntry, as in jsonLint.
type jsonLintFix struct {
	SchemaVersion int          `json:"schemaVersion"`
	File          string       `json:"file"`
	Error         *jsonErrBody `json:"error,omitempty"`
	// Changes is the tag-level diff. Operations is the structural write list; see
	// jsonReport for the shared contract.
	Changes    []jsonChange  `json:"changes"`
	Operations []string      `json:"operations"`
	Remaining  []jsonFinding `json:"remaining"`
	Committed  bool          `json:"committed"`
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
	// nonNil so operations serializes as "[]", never null, matching the other lists.
	j := jsonLintFix{
		SchemaVersion: schemaVersion,
		File:          jsonFileName(o.path),
		Changes:       toJSONChanges(o.changes),
		Operations:    nonNil(o.operations),
		Remaining:     toJSONFindings(o.remaining),
		Committed:     o.committed,
	}
	j.setPostWrite(o.postWrite)
	return j
}

// toJSONFindings is shared by lint and lint --fix so the finding shape cannot drift.
func toJSONFindings(findings []wl.Finding) []jsonFinding {
	out := make([]jsonFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, jsonFinding{
			Severity: f.Severity.String(),
			Code:     f.Code,
			Message:  f.Message,
			Key:      string(f.Key),
		})
	}
	return out
}
