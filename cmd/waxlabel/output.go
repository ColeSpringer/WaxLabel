package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/colespringer/waxlabel/waxerr"
	"github.com/spf13/cobra"
)

// schemaVersion tags JSON output so a consumer can detect shape changes. It stays
// pinned at 1 through the pre-1.0 series: there are no released consumers to keep
// compatible, so the shape is still settling and bumping it would imply a stability
// the format does not yet promise. It starts moving at the v1.0 freeze, when the
// JSON shape becomes a compatibility surface worth versioning.
const schemaVersion = 1

// writeJSON writes v as indented JSON followed by a newline.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// jsonMode reports whether --json was requested, reading it from the command's
// (already-parsed) flags. The persistent flag is shared with the root, so this
// works inside a RunE - by which point the command has resolved and parsed.
func jsonMode(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

// wantsJSON reports whether --json appears in the raw args. dispatch uses it to
// route a terminal error even when cobra aborted before it could bind the
// persistent flag (an unknown command or a bad flag), where jsonMode would
// wrongly read false. It mirrors pflag's bool-flag forms.
func wantsJSON(args []string) bool {
	v := false
	for _, a := range args {
		switch a {
		case "--":
			return v // everything after -- is a positional argument
		case "--json", "--json=true":
			v = true
		case "--json=false":
			v = false
		}
	}
	return v
}

// jsonWarning is the JSON form of a parse/plan warning.
type jsonWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// jsonErrBody is the machine code and message for a failure, used both in the
// terminal error envelope and in per-file error entries.
type jsonErrBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// humanDuration formats a duration as H:MM:SS or M:SS. Sub-minute clips are
// shown in seconds so short fixtures are not flattened to 0:00.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "0:00"
	}
	if d < time.Minute {
		// Round to centiseconds first; a value just under a minute (59.999s) would
		// otherwise print as "60.00s" instead of falling through to "1:00".
		cs := d.Round(10 * time.Millisecond)
		if cs < time.Minute {
			return fmt.Sprintf("%.2fs", cs.Seconds())
		}
		d = cs
	}
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// perFile runs a per-path command (dump, verify): it processes each path
// independently, captures the first error for the exit code, routes per-file
// errors to stderr (text) or into the JSON result, separates text records with a
// blank line only between records actually written, and emits the per-file
// results as a JSON array (always, even for a single path). The returned error is
// alreadyRendered, so dispatch keeps the exit class without re-rendering.
func perFile[P any](
	cmd *cobra.Command,
	paths []string,
	compute func(ctx context.Context, path string) (P, error),
	toJSON func(path string, p P) any,
	errorJSON func(path string, c classifiedError) any,
	render func(w io.Writer, path string, p P),
) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	asJSON := jsonMode(cmd)
	var items []any
	var firstErr error
	rendered := 0
	for _, path := range paths {
		p, err := compute(cmd.Context(), path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if asJSON {
				items = append(items, errorJSON(path, classifyError(err)))
			} else {
				fmt.Fprintf(errOut, "waxlabel: %s: %s\n", path, perFileReason(err))
			}
			continue
		}
		if asJSON {
			items = append(items, toJSON(path, p))
		} else {
			if rendered > 0 {
				fmt.Fprintln(out)
			}
			render(out, path, p)
			rendered++
		}
	}
	if asJSON {
		if err := emitJSONList(out, items); err != nil {
			return err
		}
	}
	return alreadyRendered(firstErr)
}

// emitJSONList writes the per-file items as a JSON array - always, even for a
// single path - so a list command's --json output can be consumed uniformly
// (iterate, or jq '.[]') no matter how many paths were given. An empty result
// (e.g. a --recursive walk that matched no audio files) marshals as [] rather
// than null. diff and copy are single-result commands and do not use this.
func emitJSONList(w io.Writer, items []any) error {
	if items == nil {
		items = []any{}
	}
	return writeJSON(w, items)
}

// noteNoFiles prints a note when a path list is empty - reachable only when a
// --recursive walk matched no audio files (MinimumNArgs(1) guarantees at least
// one argument otherwise) - so editing a typo'd or audio-free directory is not a
// silent success. set and plan share it for an identical message; JSON output is
// unaffected (it still emits []).
func noteNoFiles(w io.Writer, paths []string) {
	if len(paths) == 0 {
		fmt.Fprintln(w, "waxlabel: no audio files found")
	}
}

// usageError marks a bad-arguments failure, which maps to exit code 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// alreadyRenderedError marks a failure whose output a command already wrote.
// dispatch keeps the wrapped cause for the exit code without rendering again.
type alreadyRenderedError struct{ cause error }

func (e *alreadyRenderedError) Error() string { return e.cause.Error() }
func (e *alreadyRenderedError) Unwrap() error { return e.cause }

// alreadyRendered wraps cause so dispatch does not render it again. It returns
// nil for a nil cause, so commands can return it unconditionally.
func alreadyRendered(cause error) error {
	if cause == nil {
		return nil
	}
	return &alreadyRenderedError{cause: cause}
}

// jsonError is the --json terminal error envelope.
type jsonError struct {
	SchemaVersion int         `json:"schemaVersion"`
	Error         jsonErrBody `json:"error"`
}

// renderError writes the terminal error as JSON or as a human-readable line,
// using one shared classification for both.
func renderError(w io.Writer, jsonMode bool, err error) {
	if err == nil {
		return
	}
	c := classifyError(err)
	if jsonMode {
		_ = writeJSON(w, jsonError{
			SchemaVersion: schemaVersion,
			Error:         jsonErrBody{Code: c.code, Message: c.message},
		})
		return
	}
	fmt.Fprintf(w, "waxlabel: %s\n", c.message)
	if c.hint != "" {
		fmt.Fprintf(w, "  hint: %s\n", c.hint)
	}
}

// cleanMessage defensively strips a leading "waxlabel: " so the CLI's own prefix
// is never doubled. The library sentinels no longer embed it, so today this is a
// no-op; it stays as forward-insurance against a future error that reintroduces
// the prefix.
func cleanMessage(msg string) string { return strings.TrimPrefix(msg, "waxlabel: ") }

// perFileReason renders a per-file error for a command that already prints the
// path itself (dump, verify). A bare *fs.PathError restates the path ("open
// /x: no such file or directory"), so it is reduced to its underlying reason to
// keep the path from appearing twice on the line; every other error keeps its
// (prefix-cleaned) message.
func perFileReason(err error) string {
	if pe, ok := err.(*fs.PathError); ok {
		return pe.Err.Error()
	}
	return cleanMessage(err.Error())
}

// classifiedError is every user-visible representation of a terminal error.
type classifiedError struct {
	exitCode int
	code     string
	message  string
	hint     string
}

// classifyError maps a terminal error to a stable exit code and machine code so
// scripts can branch on the failure class without parsing messages. The library
// sentinels take precedence over the structural (filesystem) fallback. Keep this
// table in sync with the exit-code list in README.md.
func classifyError(err error) classifiedError {
	if err == nil {
		return classifiedError{}
	}
	c := classifiedError{message: cleanMessage(err.Error()), exitCode: 1, code: "error"}
	switch {
	case errors.Is(err, context.Canceled):
		c.exitCode, c.code, c.message = 130, "canceled", "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		c.exitCode, c.code, c.message = 130, "timeout", "operation timed out"
	case isUsageError(err):
		c.exitCode, c.code = 2, "usage"
	case errors.Is(err, waxerr.ErrInvalidKey):
		c.exitCode, c.code = 2, "invalid-key"
	case errors.Is(err, waxerr.ErrUnsupportedFormat):
		c.exitCode, c.code = 3, "unsupported-format"
	case errors.Is(err, waxerr.ErrUnsupportedTag):
		c.exitCode, c.code = 3, "unsupported-tag"
	case errors.Is(err, waxerr.ErrSourceChanged):
		c.exitCode, c.code, c.hint = 5, "source-changed",
			"the file changed since it was read; re-run to pick up the new contents"
	case errors.Is(err, waxerr.ErrInvalidData),
		errors.Is(err, waxerr.ErrSizeTooLarge),
		errors.Is(err, waxerr.ErrTooDeep),
		errors.Is(err, waxerr.ErrPictureTooLarge):
		c.exitCode, c.code = 4, "invalid-data"
	case errors.Is(err, waxerr.ErrNoTags):
		c.exitCode, c.code = 4, "no-tags"
	case isNotFoundPathError(err):
		pe, _ := err.(*fs.PathError) // guaranteed by isNotFoundPathError
		c.exitCode, c.code, c.message = 6, "not-found", "no such file: "+pe.Path
	case isLocalIOError(err):
		c.exitCode, c.code = 6, "io"
	}
	return c
}

func isUsageError(err error) bool {
	_, ok := waxerr.AsType[*usageError](err)
	return ok
}

// isNotFoundPathError reports whether err is, at the top level, a "file does not
// exist" *fs.PathError - the one shape the CLI restates as a clean
// "no such file: <path>". The assertion is deliberately direct (not errors.As):
//   - an error a caller already wrapped with context (a temp-file create, a
//     cover read) keeps that message and classifies as the generic I/O class;
//   - a *os.LinkError/*os.SyscallError that os.IsNotExist would also accept
//     (e.g. a rename race) stays in the I/O class too, since "not-found" promises
//     the clean path-only message we can produce only for a *fs.PathError.
func isNotFoundPathError(err error) bool {
	pe, ok := err.(*fs.PathError)
	return ok && os.IsNotExist(pe)
}

// isLocalIOError reports whether err is a local filesystem failure. The kinds
// the write/read paths actually produce: os.Open and fsync return *fs.PathError;
// the atomic save-back's os.Rename returns *os.LinkError; lower-level syscall
// wrappers return *os.SyscallError. All map to the "io" exit class.
func isLocalIOError(err error) bool {
	if _, ok := waxerr.AsType[*fs.PathError](err); ok {
		return true
	}
	if _, ok := waxerr.AsType[*os.LinkError](err); ok {
		return true
	}
	if _, ok := waxerr.AsType[*os.SyscallError](err); ok {
		return true
	}
	return false
}

// exitCodeFor maps an error to its process exit code.
func exitCodeFor(err error) int { return classifyError(err).exitCode }

// normalizeExecuteError converts cobra's untyped unknown-command/flag errors
// into usage errors so they map to exit code 2.
func normalizeExecuteError(err error) error {
	if err == nil || isUsageError(err) {
		return err
	}
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown subcommand", "unknown flag", "unknown shorthand"} {
		if strings.HasPrefix(msg, p) {
			return &usageError{msg: msg}
		}
	}
	return err
}
