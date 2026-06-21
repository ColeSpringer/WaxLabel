package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/spf13/cobra"
)

// errLintFindings is the sentinel lint returns when a file has findings at
// warning severity or above but no structural error. It is plain (unclassified),
// so it maps to exit code 1 - the linter / diff(1) convention of "1 = issues
// found" - and is returned already-rendered so no error line prints over the
// findings. A structural parse/IO error outranks it and keeps its own exit class
// (so a script can tell an exit-4 parse failure from exit-1 "issues found").
var errLintFindings = errors.New("issues found")

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
			"Exit code 0 means clean, 1 means issues were found, and 2 or more is a\n" +
			"structural error (which outranks findings in a multi-file run).\n\n" +
			"With --fix, apply only the safe, non-destructive remediations - clearing\n" +
			"the encoder stamp and stripping legacy containers - then save in place,\n" +
			"reporting what changed. Pictures are never dropped automatically; every\n" +
			"finding --fix does not address is reported as \"not auto-fixed\". With\n" +
			"--recursive, directory arguments are walked for audio files. A single\n" +
			"\"-\" reads from standard input (read-only; not valid with --fix).",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := expandPaths(args, recursive)
			if err != nil {
				return err
			}
			// A --recursive walk that matched no audio files is an error for the
			// mutating --fix path - so a script cannot read "nothing happened" as
			// success - but a harmless empty read otherwise (noteNoFiles + exit 0).
			// This guard is scoped to --fix and runs before noteNoFiles so the
			// returned error is printed once, not doubled by the note.
			if fix && len(paths) == 0 {
				return usagef("no audio files found")
			}
			noteNoFiles(cmd.ErrOrStderr(), paths)
			if fix {
				if slices.Contains(paths, stdinArg) {
					return usagef("cannot fix standard input; --fix writes changes back to a file")
				}
				return runLintFix(cmd, paths)
			}
			return runLint(cmd, paths)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply the safe, non-destructive fixes and save in place")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recurse into directory arguments, linting every audio file found (selected by file extension)")
	return cmd
}

// lintLoop runs a lint-style per-file command: it processes each path, captures
// the first structural error, accumulates whether any file had an issue, and
// emits per-file text or JSON. It is perFile with a finding accumulator and
// lint's exit contract - a structural error (its own exit class) outranks issues
// (exit 1), which outrank a clean run (exit 0) - so runLint and runLintFix differ
// only in their compute/issue/render helpers, not in the loop.
func lintLoop[T any](
	cmd *cobra.Command,
	paths []string,
	compute func(ctx context.Context, path string) (T, error),
	hasIssue func(T) bool,
	errItem func(path string, c classifiedError) any,
	jsonItem func(path string, t T) any,
	render func(w io.Writer, path string, t T),
) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	asJSON := jsonMode(cmd)
	var items []any
	var firstErr error
	issues := false
	rendered := 0
	for _, path := range paths {
		t, err := compute(cmd.Context(), path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if asJSON {
				items = append(items, errItem(path, classifyError(err)))
			} else {
				perFileError(errOut, path, err)
			}
			continue
		}
		if hasIssue(t) {
			issues = true
		}
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
	if firstErr != nil {
		return alreadyRendered(firstErr)
	}
	if issues {
		return alreadyRendered(errLintFindings)
	}
	return nil
}

// anyAtWarning reports whether any finding is at warning severity or above (the
// threshold that makes a lint run "have issues").
func anyAtWarning(findings []wl.Finding) bool {
	for _, f := range findings {
		if f.Severity >= wl.LintWarning {
			return true
		}
	}
	return false
}

// runLint reports findings per file.
func runLint(cmd *cobra.Command, paths []string) error {
	realOf, cleanup, err := readInputs(cmd.InOrStdin(), paths)
	if err != nil {
		return err
	}
	defer cleanup()
	return lintLoop(cmd, paths,
		func(ctx context.Context, path string) ([]wl.Finding, error) {
			doc, err := wl.ParseFile(ctx, realOf(path))
			if err != nil {
				return nil, err
			}
			return doc.Lint(), nil
		},
		anyAtWarning,
		func(path string, c classifiedError) any {
			return jsonLint{SchemaVersion: schemaVersion, File: path, Error: &jsonErrBody{c.code, c.message}}
		},
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
		// A finding's message and key can be file-derived (e.g. the encoder-noise
		// message carries the raw inherited stamp; a custom-key finding carries the raw
		// field name), but Finding.String now self-sanitizes, so it is safe to print
		// directly (the output boundary is a second backstop).
		fmt.Fprintf(w, "  %s\n", f.String())
	}
}

// runLintFix applies the safe remediations to each file and saves, reporting the
// field-level changes (the shared write-plan preview) and the findings that still
// remain afterward. A remaining warning-or-worse finding still yields exit 1.
func runLintFix(cmd *cobra.Command, paths []string) error {
	return lintLoop(cmd, paths,
		lintFixOne,
		func(o fixOutcome) bool { return anyAtWarning(o.remaining) },
		func(path string, c classifiedError) any {
			return jsonLintFix{SchemaVersion: schemaVersion, File: path, Error: &jsonErrBody{c.code, c.message}}
		},
		func(path string, o fixOutcome) any { return toJSONLintFix(o) },
		func(w io.Writer, path string, o fixOutcome) { renderLintFix(w, o) },
	)
}

// fixOutcome is one file's lint --fix result: the field-level changes applied, the
// structural operations performed (e.g. stripping a legacy ID3v1 trailer), the
// findings that still remain afterward, and whether the save committed new bytes.
type fixOutcome struct {
	path       string
	changes    []tag.Change
	operations []string
	remaining  []wl.Finding
	committed  bool
}

// lintFixOne parses path, applies the safe remediation, saves in place, then
// re-lints the saved file. Re-linting (rather than trusting the fixer's intent)
// keeps the report honest: the canonical fix cannot reach every source of a
// finding - a transcoder stamp held in a native vendor string survives a
// Clear(ENCODER) - so "remaining" is whatever a fresh lint would now show.
func lintFixOne(ctx context.Context, path string) (fixOutcome, error) {
	doc, err := wl.ParseFile(ctx, path)
	if err != nil {
		return fixOutcome{}, err
	}
	fix := doc.PlanLintFix()
	plan, err := doc.Edit().Apply(fix.Patch).Prepare(fix.Options...)
	if err != nil {
		return fixOutcome{}, err
	}
	_, res, err := plan.Execute(ctx, wl.SaveBack())
	if err != nil {
		return fixOutcome{}, err
	}
	// Determine the findings that remain after the fix. When the save committed new
	// bytes, re-parse to see the true post-fix state; when it wrote nothing the
	// file is byte-identical to what we parsed, so doc.Lint() still holds - avoid
	// re-parsing (which would re-hash every embedded picture) in that case.
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
	return fixOutcome{
		path:       path,
		changes:    plan.Changes(),
		operations: plan.Report().Operations,
		remaining:  remaining,
		committed:  res.Committed,
	}, nil
}

// renderLintFix prints what --fix did to one file: the fields it changed (or
// "nothing to fix"), the findings it left for the user, and the save outcome.
func renderLintFix(w io.Writer, o fixOutcome) {
	// --fix rejects "-" (stdin) up front (see newLintCmd), so o.path is always a real
	// file - no "<stdin>" relabel is needed, unlike the other record headers - but it
	// is still escaped for the single-line header and the "saved" line below, so a
	// hostile filename from a --recursive walk cannot forge a line (e.g. a fake
	// "saved /etc/passwd").
	name := tag.SanitizeLine(o.path)
	fmt.Fprintf(w, "%s\n", name)
	// A legacy-container strip is a structural operation with no field change, so
	// "nothing to fix" holds only when both the changes and the operations are empty
	// - otherwise the strip would be invisible (the README promises it is reported).
	if len(o.changes) == 0 && len(o.operations) == 0 {
		fmt.Fprintln(w, "  nothing to fix")
	} else {
		fmt.Fprintln(w, "  fixed:")
		for _, c := range o.changes {
			renderChangeLine(w, "    ", c)
		}
		for _, op := range o.operations {
			fmt.Fprintf(w, "    - %s\n", op)
		}
	}
	for _, f := range o.remaining {
		// Finding.String self-sanitizes the file-derived text (see renderLint).
		fmt.Fprintf(w, "  not auto-fixed: %s\n", f.String())
	}
	if o.committed {
		fmt.Fprintf(w, "  saved %s\n", name)
	} else {
		// No bytes written: nothing was auto-fixable. Any remaining findings are
		// already listed above, so don't claim the file is "clean" here.
		fmt.Fprintf(w, "  left unchanged\n")
	}
}

// jsonLint is the machine-readable lint result for one file. On a parse failure
// only SchemaVersion, File, and Error are set.
type jsonLint struct {
	SchemaVersion int           `json:"schemaVersion"`
	File          string        `json:"file"`
	Error         *jsonErrBody  `json:"error,omitempty"`
	Findings      []jsonFinding `json:"findings,omitempty"`
}

type jsonFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Key      string `json:"key,omitempty"`
}

// jsonLintFix is the machine-readable lint --fix result for one file. Remaining
// holds the findings a fresh lint of the saved file still reports (what --fix
// could not safely resolve).
type jsonLintFix struct {
	SchemaVersion int           `json:"schemaVersion"`
	File          string        `json:"file"`
	Error         *jsonErrBody  `json:"error,omitempty"`
	Changes       []jsonChange  `json:"changes,omitempty"`
	Operations    []string      `json:"operations,omitempty"`
	Remaining     []jsonFinding `json:"remaining,omitempty"`
	Committed     bool          `json:"committed"`
}

func toJSONLint(path string, findings []wl.Finding) jsonLint {
	return jsonLint{
		SchemaVersion: schemaVersion,
		File:          path,
		Findings:      toJSONFindings(findings),
	}
}

func toJSONLintFix(o fixOutcome) jsonLintFix {
	return jsonLintFix{
		SchemaVersion: schemaVersion,
		File:          o.path,
		Changes:       toJSONChanges(o.changes),
		Operations:    o.operations,
		Remaining:     toJSONFindings(o.remaining),
		Committed:     o.committed,
	}
}

// toJSONFindings converts a finding list to its JSON form, shared by lint and
// lint --fix so the finding shape cannot drift.
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
