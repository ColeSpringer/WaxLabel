package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/tag"
	"github.com/colespringer/waxlabel/waxerr"
	"github.com/spf13/cobra"
)

// schemaVersion tags JSON output so a consumer can detect shape changes. It
// stays pinned at 1 until the command's JSON shape changes in a way consumers
// need to distinguish.
const schemaVersion = 1

// subformatOf returns the top-level JSON "subformat" value: the exact container
// subtype when known, otherwise the codec family string already used for "format".
// dump and caps share it so plain formats consistently report subformat == format.
func subformatOf(container, format string) string {
	if container != "" {
		return container
	}
	return format
}

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

// nonNil returns s, or an empty (non-nil) slice when s is nil, so a JSON collection
// field assigned from it always marshals as [] rather than null. It is the single
// idiom for the "every iterable field is always an array" invariant on the
// report structs when the source slice can be nil; a field built by append is
// instead initialized to []T{} at its struct literal, same effect.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
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
// for concurrent use: Write mutates the partial-rune buffer without locking.
type sanitizingWriter struct {
	w io.Writer
	// buf holds a trailing incomplete UTF-8 sequence (< utf8.UTFMax bytes) carried
	// between Writes, so a rune split across two writes is not escaped as if its
	// lead byte were invalid. Every human write today is a single fmt.Fprint* call
	// (a complete UTF-8 unit), so buf stays empty in practice. Close flushes any
	// remainder.
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
// wrongly read false. It mirrors pflag's bool-flag forms: --json sets true,
// --json=value uses strconv.ParseBool, and an unparseable value leaves the prior
// state unchanged.
func wantsJSON(args []string) bool {
	v := false
	for _, a := range args {
		switch {
		case a == "--":
			return v // everything after -- is a positional argument
		case a == "--json":
			v = true
		case strings.HasPrefix(a, "--json="):
			// pflag leaves the previous value unchanged when bool parsing fails.
			if b, err := strconv.ParseBool(a[len("--json="):]); err == nil {
				v = b
			}
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
// terminal error envelope and in per-file error entries. Hint carries the same
// actionable guidance the human render shows (the leading-dash "use --" pointer on a
// usage error, the "re-run" pointer on source-changed), single-sourced from
// classifiedError.hint so the two surfaces cannot drift; it is omitted when empty.
type jsonErrBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// jsonErrorEntry is the per-file error element shared by every list command's
// --json output (dump, verify, lint, caps, plan, set). On a per-file failure the
// element carries only the schema version, the file, and the classified error -
// none of the command-specific result fields, which would otherwise leak as zero
// values (plan's null operations array, set's phantom committed flag and echoed
// output path) and let a scripted consumer mistake a failed file for a partial
// success. Centralizing the shape here is what keeps the commands from drifting:
// a per-file error is this, and only this. Every command result struct (jsonDocument,
// jsonReport, ...) keeps a matching Error field with the same JSON shape, so a
// consumer can unmarshal every element of a mixed success/failure array into the one
// command type and branch on whether Error is set - the production path emits this
// type, the consumer decodes into theirs.
type jsonErrorEntry struct {
	SchemaVersion int         `json:"schemaVersion"`
	File          string      `json:"file"`
	Error         jsonErrBody `json:"error"`
}

// errorEntry builds the shared per-file error element from a classified error,
// carrying its hint (e.g. source-changed's "re-run" pointer) into the JSON.
func errorEntry(path string, c classifiedError) jsonErrorEntry {
	return jsonErrorEntry{SchemaVersion: schemaVersion, File: jsonFileName(path), Error: jsonErrBody{Code: c.code, Message: c.message, Hint: c.hint}}
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

// perFile runs a per-path command (dump, verify, plan, caps): it processes each path
// independently, captures the most-severe error class for the exit code (worseError,
// not the first error seen), routes per-file errors to stderr (text) or into the
// JSON result as the shared jsonErrorEntry, separates text records with a blank line
// only between records actually written, and emits the per-file results as a JSON
// array (always, even for a single path). noSeparator drops that inter-record blank
// line, for a one-line-per-file (TSV) renderer - verify --quiet - whose output pipes
// into sort/uniq where a blank line would be spurious. A per-file failure - including
// plan's --strict single-valued-multi guardrail - is one array element so the
// aggregate exit code stays order-independent (worseError); the invocation-level
// unknown-key guardrail, which is file-independent, still aborts up front. The
// returned error is alreadyRendered, so dispatch keeps the exit class without
// re-rendering.
func perFile[P any](
	cmd *cobra.Command,
	paths []string,
	compute func(ctx context.Context, path string) (P, error),
	toJSON func(path string, p P) any,
	render func(w io.Writer, path string, p P),
	noSeparator bool,
) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	asJSON := jsonMode(cmd)
	var items []any
	var worstErr error
	rendered := 0
	for _, path := range paths {
		p, err := compute(cmd.Context(), path)
		if err != nil {
			// A closed output pipe cancelled the shared context; the files not yet reached are
			// not our failure to report. Stop silently (dispatch turns the run's broken-pipe cause
			// into exit 0) rather than emitting a "canceled" line per un-dumped file. Gate on
			// isPipeClose too, matching dispatch, so a genuine file error that merely races the
			// pipe-close is still recorded and rendered, not swallowed by the break. Any error seen
			// before the break stays in worstErr and still sets the exit code.
			if errors.Is(context.Cause(cmd.Context()), errBrokenPipe) && isPipeClose(err) {
				break
			}
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
		if asJSON {
			items = append(items, toJSON(path, p))
		} else {
			if rendered > 0 && !noSeparator {
				fmt.Fprintln(out)
			}
			render(out, path, p)
			rendered++
		}
	}
	if asJSON {
		if werr := emitJSONList(out, items); werr != nil {
			// The terminal array write failed - almost always a closed output pipe (EPIPE). A
			// genuine per-file error already recorded outranks a broken pipe (errClassRank), so
			// surface worstErr when present so its exit class still stands; otherwise return the
			// write error, which dispatch maps to broken-pipe (exit 0). Returning werr
			// unconditionally would drop an accumulated exit-4 to broken-pipe/io.
			if worstErr != nil {
				return alreadyRendered(worstErr)
			}
			return werr
		}
	}
	return alreadyRendered(worstErr)
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

// listCommandAnnotation marks a command whose --json output is a JSON array (one
// element per input, via perFile/emitJSONList above). dispatch reads it to wrap a
// pre-flight error (a bad flag, missing args, or a dir without --recursive) in the
// same single-element array shape, so `dump --json... | jq '.[]'` keeps working even
// when the command fails before its per-file loop. It travels with the command
// at construction (markListCommand), so adding a list command later sets this in its
// own constructor rather than in a second hand-maintained list the renderer would
// have to track.
const listCommandAnnotation = "waxlabel/emitsJSONList"

// markListCommand tags cmd as a list command (see listCommandAnnotation) and returns
// it, so a constructor can wrap its command in one expression.
func markListCommand(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[listCommandAnnotation] = "true"
	return cmd
}

// emitsJSONList reports whether the command args resolve to emits its --json output as
// a list (one element per input), so dispatch wraps a pre-flight error in the same
// single-element array shape. It resolves the command with cobra's own Find -
// which already handles "--", a flag before the subcommand, and the --format value -
// rather than re-scanning raw args, then reads the list-command annotation. caps is a
// list command over files but answers a single object under --format (a format query,
// not a per-file list), so that one case stays an object; an unknown command resolves
// to the root (no annotation) and stays an object too.
func emitsJSONList(root *cobra.Command, args []string) bool {
	cmd, _, err := root.Find(args)
	if err != nil || cmd == nil || cmd.Annotations[listCommandAnnotation] != "true" {
		return false
	}
	if cmd.Name() == "caps" && hasFlag(args, "format") {
		return false
	}
	return true
}

// hasFlag reports whether the long flag --name appears in args, in either the
// "--name value" or "--name=value" spelling. It stops at the POSIX "--" terminator,
// so a positional that merely looks like the flag (a file literally named --format,
// passed after --) is not mistaken for it.
func hasFlag(args []string, name string) bool {
	long := "--" + name
	for _, a := range args {
		if a == "--" {
			break // everything after -- is a positional, not a flag
		}
		if a == long || strings.HasPrefix(a, long+"=") {
			return true
		}
	}
	return false
}

// noteNoFiles prints a note when a path list is empty - reachable only when a
// --recursive walk matched no audio files (MinimumNArgs(1) guarantees at least
// one argument otherwise) - so editing a typo'd or audio-free directory is not a
// silent success. set and plan share it for an identical message; JSON output is
// unaffected (it still emits []).
func noteNoFiles(w io.Writer, paths []string) {
	if len(paths) == 0 {
		// A "note:" prefix (not "waxlabel:") so this exit-0 advisory does not read as a
		// failure line - the run succeeded, there was simply nothing to do.
		fmt.Fprintln(w, "note: no audio files found")
	}
}

// noteSkipped reports how many regular files a --recursive walk passed over for not
// matching a known audio extension, so a directory of unrecognized files is not a
// silent near-no-op (it pairs with noteNoFiles: when nothing matched, this explains
// why). It is a text-mode diagnostic suppressed under --json (whose stdout shape is
// fixed) and when nothing was skipped, and uses the same "note:" prefix as the other
// exit-0 advisories.
func noteSkipped(w io.Writer, skipped int, asJSON bool) {
	if asJSON || skipped == 0 {
		return
	}
	fmt.Fprintf(w, "note: %d file(s) skipped (not recognized by extension)\n", skipped)
}

// usageError marks a bad-arguments failure, which maps to exit code 2. The extra
// fields are set only at the cobra-origin sites that dead-end with no guidance (an
// unknown flag, a bad arg count, an unknown command): cmd is the resolved command
// path for the help hint (empty falls back to "waxlabel"), wantsHint requests the
// "run '<cmd> --help' for usage" pointer, multiline marks the message as
// trusted multi-line cobra text whose newlines/tabs must be preserved on render,
// and hint carries an explicit hint line that overrides the wantsHint pointer (for
// example, leading-dash path guidance on an unknown flag/shorthand). The hand-written usagef messages leave all
// four zero: they are single-line and already self-document.
type usageError struct {
	msg       string
	cmd       string
	wantsHint bool
	multiline bool
	hint      string
}

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// checkArgText reclassifies the library's writable-text rejection (a NUL byte or invalid UTF-8)
// as a CLI usage error (exit 2): a bad command-line value is a bad invocation, not a corrupt
// media file. It reads the bare reason phrase from the library's WritableTextReason - the same
// rule the library backstop enforces - so the CLI boundary and the backstop cannot drift, and the
// message needs no error-string surgery. The four argv inputs cannot carry a NUL (argv is
// NUL-terminated), but --synced-lyrics-file content can, so the shared validator is what closes
// that path. what names the offending input for the message.
func checkArgText(value, what string) error {
	if reason := wl.WritableTextReason(value); reason != "" {
		return usagef("%s %s", what, reason)
	}
	return nil
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
// using one shared classification for both. emitList wraps the JSON envelope in a
// single-element array for a list command, so its pre-flight failures keep the
// documented array shape that `jq '.[]'` relies on; a non-list command (and the
// human path) is unaffected.
func renderError(w io.Writer, jsonMode, emitList bool, err error) {
	if err == nil {
		return
	}
	c := classifyError(err)
	// A broken output pipe is silent: the downstream reader closed the pipe deliberately, so
	// printing an error (to a possibly-still-open stderr) would be noise, and there may be no
	// stdout to write a JSON envelope to anyway. exitCodeFor still returns its 0.
	if c.code == "broken-pipe" {
		return
	}
	if jsonMode {
		env := jsonError{
			SchemaVersion: schemaVersion,
			Error:         jsonErrBody{Code: c.code, Message: c.message, Hint: c.hint},
		}
		if emitList {
			_ = emitJSONList(w, []any{env})
		} else {
			_ = writeJSON(w, env)
		}
		return
	}
	// Pick the sanitizer by message shape. A single-line message can embed a
	// file-derived path (e.g. "<path>: no such file or directory" from a hostile
	// glob/walk arg), so SanitizeLine escapes \n/\t too, blocking line-forgery. A multiline message
	// is trusted cobra text (the "unknown command... Did you mean this?" block), so
	// SanitizeText preserves its real newlines/tabs while still escaping ESC/CSI/BEL/
	// CR - otherwise the suggestion shows literal \x0a/\x09. The output boundary
	// backstops control bytes either way; the JSON branch above keeps c.message raw.
	sanitize := tag.SanitizeLine
	if c.multiline {
		sanitize = tag.SanitizeText
	}
	fmt.Fprintf(w, "waxlabel: %s\n", sanitize(c.message))
	if c.hint != "" {
		fmt.Fprintf(w, "  hint: %s\n", tag.SanitizeLine(c.hint))
	}
}

// cleanMessage strips a leading "waxlabel: " so the CLI's own prefix is never
// doubled. Library sentinels do not embed it, but direct errors may still carry one.
func cleanMessage(msg string) string { return strings.TrimPrefix(msg, "waxlabel: ") }

// perFileError writes the standard per-file failure line - "waxlabel: <path>:
// <reason>" - to w, single-sourcing the format and the displayName/perFileReason
// pair every per-file command shares (dump/verify via perFile, lint, set, and copy,
// which cannot use the perFile generic because it has two named operands).
func perFileError(w io.Writer, path string, err error) {
	fmt.Fprintf(w, "waxlabel: %s: %s\n", displayName(path), perFileReason(err))
}

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
// multiline marks a message whose embedded newlines/tabs are trusted and must be
// preserved on the human render (a cobra command suggestion block), so renderError
// picks the newline-preserving sanitizer for it.
type classifiedError struct {
	exitCode  int
	code      string
	message   string
	hint      string
	multiline bool
}

// errBrokenPipe is the cancel cause the SIGPIPE goroutine (main) uses so a closed output pipe
// is distinguishable from a real interrupt: both leave a canceled op returning context.Canceled,
// but only a broken pipe carries this cause. dispatch re-tags the terminal error with it, and
// classifyError maps it to a silent exit 0 - the Unix convention for `... | head`.
var errBrokenPipe = errors.New("broken output pipe")

// isPipeClose reports whether err is the symptom of a closed output pipe: the shared context's
// cancellation, or an EPIPE from a write that raced the reader's close. perFile's loop pairs it
// with the errBrokenPipe cancel cause to stop silently on a broken pipe, so a genuine file error
// that merely coincided with the pipe closing is still recorded rather than swallowed.
func isPipeClose(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, syscall.EPIPE)
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
	case errors.Is(err, errBrokenPipe):
		// A closed output pipe (`... | head`) is benign: exit 0 with no message, so the
		// pipeline's own exit status stands. Placed first because it is the most specific.
		c.exitCode, c.code, c.message = 0, "broken-pipe", ""
	case errors.Is(err, context.Canceled):
		c.exitCode, c.code, c.message = 130, "canceled", "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		c.exitCode, c.code, c.message = 130, "timeout", "operation timed out"
	case isUsageError(err):
		c.exitCode, c.code = 2, "usage"
		if ue, ok := errors.AsType[*usageError](err); ok {
			c.multiline = ue.multiline
			switch {
			case ue.hint != "":
				// An explicit hint (the leading-dash "use --" guidance) wins over the
				// generic --help pointer, which would be the less useful of the two here.
				c.hint = ue.hint
			case ue.wantsHint:
				name := ue.cmd
				if name == "" {
					name = "waxlabel"
				}
				c.hint = fmt.Sprintf("run '%s --help' for usage", name)
			}
		}
	case errors.Is(err, waxerr.ErrInvalidKey):
		c.exitCode, c.code = 2, "invalid-key"
	case errors.Is(err, waxerr.ErrNeedsFile):
		// A path-less SaveBack: the format is supported, so this is a usage error
		// (exit 2), not unsupported-format. The CLI always has a path, so it surfaces
		// only for a direct library caller via the JSON/text error envelope.
		c.exitCode, c.code = 2, "needs-file"
	case errors.Is(err, waxerr.ErrUnsupportedFormat):
		c.exitCode, c.code = 3, "unsupported-format"
	case errors.Is(err, waxerr.ErrUnsupportedTag):
		c.exitCode, c.code = 3, "unsupported-tag"
	case errors.Is(err, waxerr.ErrChainedStream):
		// A chained/multiplexed Ogg stream: a distinct write-refusal, not collapsed into
		// unsupported-tag - two different refusal reasons reading as one code loses signal.
		c.exitCode, c.code = 3, "unsupported-stream"
	case errors.Is(err, waxerr.ErrUnalignedStream):
		// A well-formed but non-page-aligned Ogg stream: a write refusal (exit 3), not a
		// corrupt file (exit 4) - the stream reads fine, it just cannot be rewritten safely.
		c.exitCode, c.code = 3, "unsupported-alignment"
	case errors.Is(err, waxerr.ErrSourceChanged):
		c.exitCode, c.code, c.hint = 5, "source-changed",
			"the file changed since it was read; re-run to pick up the new contents"
	case errors.Is(err, waxerr.ErrInvalidData),
		errors.Is(err, waxerr.ErrSizeTooLarge),
		errors.Is(err, waxerr.ErrTooDeep),
		errors.Is(err, waxerr.ErrPictureTooLarge):
		c.exitCode, c.code = 4, "invalid-data"
	case isNotFoundPathError(err):
		pe, _ := err.(*fs.PathError) // guaranteed by isNotFoundPathError
		// Per-file "<path>: no such file or directory", matching the line dump/set/verify
		// already print, so the human and --json not-found phrasing agree across commands
		//.
		c.exitCode, c.code, c.message = 6, "not-found", pe.Path+": no such file or directory"
	case isLocalIOError(err):
		c.exitCode, c.code = 6, "io"
	}
	return c
}

func isUsageError(err error) bool {
	_, ok := errors.AsType[*usageError](err)
	return ok
}

// errClassRank orders per-file error classes by severity for a multi-file run's
// aggregate exit code, most-severe first. The aggregate reports the worst class
// seen, not merely the first file's error: a genuinely broken file (invalid-data)
// must outrank a wrong path (not-found), which must outrank a bad invocation
// (usage). The numeric exit code deliberately does not follow this order; numeric-max would
// let not-found/io (6) outrank invalid-data (4), i.e. "you typed a bad filename"
// would beat "this file is corrupt". Keyed off classifyError(err).code so it tracks
// the vocabulary the exit-code table documents; an unrecognized code ranks lowest
// (0) and never masks a known class above it. Keep in sync with the precedence list
// in README.md; TestErrClassRankCoversEveryErrorClass pins that every code
// classifyError can produce is ranked here, so a new class cannot silently fall to 0.
var errClassRank = map[string]int{
	"canceled":              100, // exit 130: an interrupted run dominates
	"timeout":               100, // exit 130
	"source-changed":        90,  // exit 5
	"invalid-data":          80,  // exit 4: a corrupt file
	"unsupported-format":    70,  // exit 3
	"unsupported-tag":       65,  // exit 3
	"unsupported-stream":    64,  // exit 3: a chained/multiplexed Ogg stream
	"unsupported-alignment": 63,  // exit 3: a non-page-aligned Ogg stream
	"io":                    60,  // exit 6
	"not-found":             55,  // exit 6: a wrong path
	"usage":                 20,  // exit 2: a bad invocation
	"invalid-key":           20,  // exit 2
	"needs-file":            20,  // exit 2: a path-less SaveBack (library callers)
	"error":                 10,  // exit 1: the unclassified fallback
	"broken-pipe":           5,   // exit 0: a closed output pipe, below every real failure so one still wins
}

// worseError reports whether candidate is a more-severe aggregate error than
// current for a multi-file run, by errClassRank - so the run's exit code reflects
// the most-severe failure rather than the first one encountered. A nil current is
// always replaced. Equal-rank errors keep the incumbent (the first seen), which is
// harmless since equal rank means an equal exit code. Used by the per-file loops
// (dump/verify/plan/caps via perFile, set, lint).
func worseError(current, candidate error) bool {
	if current == nil {
		return true
	}
	return errClassRank[classifyError(candidate).code] > errClassRank[classifyError(current).code]
}

// isNotFoundPathError reports whether err is, at the top level, a "file does not
// exist" *fs.PathError - the one shape the CLI restates as a clean
// "<path>: no such file or directory". The assertion is deliberately direct (not errors.As):
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
	if _, ok := errors.AsType[*fs.PathError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*os.LinkError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*os.SyscallError](err); ok {
		return true
	}
	return false
}

// exitCodeFor maps an error to its process exit code.
func exitCodeFor(err error) int { return classifyError(err).exitCode }

// dashPathHint guides a user who passed a leading-dash file path (which cobra reads
// as an unknown flag) to the "--" end-of-flags marker. Phrased conditionally ("if
// this was a file path") so it never misleads even when shown. Shared by the
// flag-error path (wrapUsageErrors' FlagErrorFunc, the route cobra actually takes for
// a flag-parse failure) and normalizeExecuteError's backstop, so both read alike.
const dashPathHint = "if this was a file path beginning with '-', put '--' before it (e.g. waxlabel dump -- -track.flac)"

// looksLikePath reports whether s has the shape of a file path rather than a bare
// flag/word token: it carries a path separator or a known audio extension. A dotted
// token like "log.level=debug" is not a path (a bare dot is not enough). It is the
// single path-shape test shared by looksLikePathFlag and looksLikeBareWord, so the two
// cannot drift.
func looksLikePath(s string) bool {
	return strings.ContainsAny(s, "/\\") || isAudioExtension(filepath.Ext(s))
}

// looksLikePathFlag reports whether an unknown-flag error message's offending token
// looks like a file path, which is when the leading-dash "--" hint is more useful than
// the generic --help pointer. A genuine flag typo (including a dotted "--log.level=debug")
// keeps the --help hint. The token is the last space-separated word of cobra's message
// ("unknown flag: --x" / "unknown shorthand flag: 'x' in -x"); the fixed prefix carries
// no path bytes.
func looksLikePathFlag(msg string) bool {
	return looksLikePath(msg[strings.LastIndexByte(msg, ' ')+1:])
}

// looksLikeBareWord reports whether s is a plain word (not path-shaped) - what an
// unquoted value fragment looks like (the "Words" left over from `--set TITLE=Two
// Words`) rather than a real file path. It backs the quoting hint.
func looksLikeBareWord(s string) bool {
	return !looksLikePath(s)
}

// normalizeExecuteError converts cobra's untyped unknown-command/flag errors
// into usage errors so they map to exit code 2.
func normalizeExecuteError(err error) error {
	if err == nil || isUsageError(err) {
		return err
	}
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown subcommand", "unknown flag", "unknown shorthand"} {
		if strings.HasPrefix(msg, p) {
			ue := &usageError{msg: msg}
			switch p {
			case "unknown command", "unknown subcommand":
				// Cobra's trusted multi-line "Did you mean this?" block: preserve its
				// newlines/tabs and point at the command list. cmd stays empty so
				// the hint falls back to "waxlabel" - an unknown command should list the
				// commands, not a subcommand's flags.
				ue.multiline, ue.wantsHint = true, true
			case "unknown flag", "unknown shorthand":
				// Backstop for a flag error that reaches here untyped; in practice cobra
				// routes flag-parse failures through FlagErrorFunc (wrapUsageErrors), which
				// attaches dashPathHint the same way. Only when the token looks like a
				// path - else a genuine typo is left to the help hint.
				if looksLikePathFlag(msg) {
					ue.hint = dashPathHint
				}
			}
			return ue
		}
	}
	return err
}
