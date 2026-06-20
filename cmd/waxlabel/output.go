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
	"unicode/utf8"

	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
	"github.com/spf13/cobra"
)

// schemaVersion tags JSON output so a consumer can detect shape changes. It stays
// pinned at 1 through the pre-1.0 series: there are no released consumers to keep
// compatible, so the shape is still settling and bumping it would imply a stability
// the format does not yet promise. It starts moving at the v1.0 freeze, when the
// JSON shape becomes a compatibility surface worth versioning.
const schemaVersion = 1

// writeJSON writes v as indented JSON followed by a newline. JSON is the machine
// contract - scripts read the exact bytes - so it bypasses the human sanitizing
// boundary: when w is a sanitizingWriter it unwraps to the raw underlying stream.
// (json.Encoder already escapes C0 controls but emits DEL and the C1 controls
// raw; sanitizing its output would corrupt those values and emit invalid JSON.)
// All JSON output - the list/single records, the caps report, and the error
// envelope - flows through here, so this one unwrap exempts every JSON path.
func writeJSON(w io.Writer, v any) error {
	if sw, ok := w.(*sanitizingWriter); ok {
		w = sw.Raw()
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// sanitizingWriter is the human-output boundary. Every byte written through it is
// run through [tag.SanitizeText], so a terminal-control sequence carried by
// untrusted file bytes - a tag value, a native block label, a parse-warning
// snippet, even a hostile filename printed in a record header or an error line -
// is neutralized regardless of which renderer produced it. dispatch wraps stdout
// and stderr in one once, so a renderer added later that forgets to escape cannot
// leak a terminal-control sequence. The boundary deliberately keeps '\n' (it
// separates output lines and carries multi-line values), so it does not stop the
// lower-severity newline line-forgery class - that relies on the per-field
// [tag.SanitizeLine] sweep at single-line sites. The JSON paths unwrap via
// [sanitizingWriter.Raw] so the machine contract keeps the exact bytes.
//
// SanitizeText is idempotent over its own (printable-ASCII) output, so the
// boundary composes with the per-field and String() escapes already in place with
// no double-escaping.
//
// It assumes writes are serialized - cobra drives one command's renderers
// sequentially, so Write is never called concurrently - and is therefore not safe
// for concurrent use: Write mutates the partial-rune buffer without locking. A
// future concurrent renderer would need a mutex here.
type sanitizingWriter struct {
	w io.Writer
	// buf holds a trailing incomplete UTF-8 sequence (< utf8.UTFMax bytes) carried
	// between Writes, so a rune split across two writes is not escaped as if its
	// lead byte were invalid. Every human write today is a single fmt.Fprint* call
	// (a complete UTF-8 unit), so buf stays empty in practice; it is hardening for a
	// future caller that streams raw bytes. Close flushes any remainder.
	buf []byte
}

func newSanitizingWriter(w io.Writer) *sanitizingWriter { return &sanitizingWriter{w: w} }

// Raw returns the underlying writer so the JSON path can emit exact bytes.
func (s *sanitizingWriter) Raw() io.Writer { return s.w }

// Write sanitizes p and writes it to the underlying stream, holding back a
// trailing incomplete UTF-8 sequence for the next call. On success it reports the
// whole of p consumed (the held-back tail is buffered, not rejected). On an
// underlying write error it commits nothing: the prior held tail is left intact
// (no buffered byte is lost) and it reports 0 of p consumed, so a retry recomposes
// the identical write.
func (s *sanitizingWriter) Write(p []byte) (int, error) {
	data := p
	if len(s.buf) > 0 {
		data = append(s.buf, p...)
	}
	hold := incompleteSuffix(data)
	// string(...) copies the prefix out now, so the s.buf reslice below cannot alias it.
	clean := tag.SanitizeText(string(data[:len(data)-hold]))
	if _, err := io.WriteString(s.w, clean); err != nil {
		// Nothing committed: leave s.buf (the prior held tail) intact so no buffered
		// byte is lost, and report 0 of p consumed so a retry re-composes this write.
		return 0, err
	}
	// Committed: replace the held tail with this write's incomplete remainder.
	s.buf = append(s.buf[:0], data[len(data)-hold:]...)
	return len(p), nil
}

// Close flushes a held-back trailing partial sequence. Since it never completed,
// SanitizeText escapes it as the invalid UTF-8 it is, so even the flush path
// emits no raw byte. dispatch calls it before returning the exit code.
func (s *sanitizingWriter) Close() error {
	if len(s.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(s.w, tag.SanitizeText(string(s.buf)))
	s.buf = nil
	return err
}

// incompleteSuffix reports the length of a trailing run of bytes that forms an
// incomplete UTF-8 sequence - a multi-byte rune missing its final bytes, which a
// following write could still complete. It returns 0 when the data ends on a rune
// boundary, including when the final byte is genuinely invalid UTF-8 (which the
// sanitizer should escape now, not hold for a completion that cannot come).
func incompleteSuffix(p []byte) int {
	if len(p) == 0 {
		return 0
	}
	// Step back over continuation bytes (10xxxxxx) to the lead byte of the final
	// sequence - at most utf8.UTFMax-1, since a UTF-8 rune is at most UTFMax bytes.
	start := len(p) - 1
	for start > 0 && len(p)-start < utf8.UTFMax && p[start]&0xC0 == 0x80 {
		start--
	}
	if utf8.FullRune(p[start:]) {
		return 0
	}
	return len(p) - start
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
				fmt.Fprintf(errOut, "waxlabel: %s: %s\n", displayName(path), perFileReason(err))
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
	// The human message is one line and can embed a file-derived path (e.g.
	// "no such file: <path>" from a hostile glob/walk arg), so escape it for the
	// single-line render. The JSON branch above keeps c.message raw - the machine
	// contract - so this human-only escape does not touch it.
	fmt.Fprintf(w, "waxlabel: %s\n", tag.SanitizeLine(c.message))
	if c.hint != "" {
		fmt.Fprintf(w, "  hint: %s\n", tag.SanitizeLine(c.hint))
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
