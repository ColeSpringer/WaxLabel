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

// writeJSON writes v as indented JSON. Scripts read the exact bytes, so it unwraps a
// sanitizingWriter to the raw stream: json.Encoder escapes C0 controls but emits DEL and
// C1 raw, and sanitizing those would emit invalid JSON. Every JSON path flows through
// here, so this one unwrap exempts them all.
func writeJSON(w io.Writer, v any) error {
	if sw, ok := w.(*sanitizingWriter); ok {
		w = sw.Raw()
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// nonNil returns s, or an empty non-nil slice, so a JSON collection field marshals as []
// rather than null. A field built by append is initialized to []T{} instead, same effect.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// sanitizingWriter is the human-output boundary: every byte runs through
// [tag.SanitizeText], so a terminal-control sequence from untrusted file bytes is
// neutralized whichever renderer produced it. dispatch wraps stdout and stderr once, so a
// renderer added later that forgets to escape still cannot leak one. SanitizeText is
// idempotent, so this composes with the per-field escapes already in place.
//
// It keeps '\n' deliberately, since that separates lines and carries multi-line values, so
// newline line-forgery is left to the per-field [tag.SanitizeLine] sweep. JSON unwraps via
// [sanitizingWriter.Raw].
//
// Not safe for concurrent use: Write mutates buf without locking. Cobra drives one
// command's renderers sequentially, so writes are serialized.
type sanitizingWriter struct {
	w io.Writer
	// buf holds a trailing incomplete UTF-8 sequence carried between Writes, so a rune
	// split across two is not escaped as if its lead byte were invalid. Every human write
	// today is one complete fmt.Fprint* call, so it stays empty in practice.
	buf []byte
}

func newSanitizingWriter(w io.Writer) *sanitizingWriter { return &sanitizingWriter{w: w} }

// Raw returns the underlying writer so the JSON path can emit exact bytes.
func (s *sanitizingWriter) Raw() io.Writer { return s.w }

// Write sanitizes p, holding back a trailing incomplete UTF-8 sequence for the next call.
// Success reports all of p consumed, since the held tail is buffered rather than rejected.
// An underlying error commits nothing and reports 0, leaving the prior tail intact so a
// retry recomposes the identical write.
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

// incompleteSuffix reports the length of a trailing multi-byte rune missing its final
// bytes, which a following write could still complete. It returns 0 when the data ends on
// a rune boundary, including on a genuinely invalid final byte, which the sanitizer should
// escape now rather than hold for a completion that cannot come.
func incompleteSuffix(p []byte) int {
	if len(p) == 0 {
		return 0
	}
	// Step back over continuation bytes to the final sequence's lead byte.
	start := len(p) - 1
	for start > 0 && len(p)-start < utf8.UTFMax && p[start]&0xC0 == 0x80 {
		start--
	}
	if utf8.FullRune(p[start:]) {
		return 0
	}
	return len(p) - start
}

// jsonMode reads --json from the command's already-parsed flags, so it works inside a
// RunE. The persistent flag is shared with the root.
func jsonMode(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

// wantsJSON reports whether --json appears in the raw args, so dispatch can route a
// terminal error even when cobra aborted before binding the persistent flag and jsonMode
// would read false. Mirrors pflag's bool forms.
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

// jsonErrBody is the machine code and message for a failure, in both the terminal envelope
// and per-file entries. Hint is the same guidance the human render shows, single-sourced
// from classifiedError.hint so the two cannot drift.
type jsonErrBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// jsonErrorEntry is the per-file error element every list command's --json output shares.
// It carries only the schema version, file, and classified error: the command-specific
// fields would leak as zero values (a null operations array, a phantom committed flag) and
// let a consumer mistake a failed file for a partial success.
//
// Every command result struct keeps a matching Error field with the same shape, so a
// consumer can unmarshal a mixed array into its own type and branch on Error being set.
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
		// Centiseconds first: 59.999s would otherwise print "60.00s" instead of "1:00".
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

// perFile runs a per-path command (dump, verify, plan, caps): each path independently,
// keeping the most-severe error class rather than the first, with per-file errors on
// stderr or in the JSON array. Text records are separated by a blank line only between
// records actually written; noSeparator drops it for a one-line-per-file renderer whose
// output pipes into sort/uniq. A per-file failure is one array element, so the aggregate
// exit stays order-independent; the file-independent unknown-key guardrail aborts up front.
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
			// A closed output pipe cancelled the context, so the files not yet reached are
			// not ours to report; stop silently rather than print "canceled" per file. Also
			// gated on isPipeClose so a genuine file error racing the close is still
			// recorded. Anything already in worstErr still sets the exit code.
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
			// Almost always a closed output pipe. A recorded per-file error outranks that,
			// so surface worstErr when present; returning werr unconditionally would drop an
			// accumulated exit 4 to broken-pipe.
			if worstErr != nil {
				return alreadyRendered(worstErr)
			}
			return werr
		}
	}
	return alreadyRendered(worstErr)
}

// emitJSONList writes the per-file items as a JSON array, always, even for a single path,
// so a list command's --json output is consumed the same way however many paths were
// given. An empty result marshals as [] rather than null. diff and copy are
// single-result commands and do not use this.
func emitJSONList(w io.Writer, items []any) error {
	if items == nil {
		items = []any{}
	}
	return writeJSON(w, items)
}

// listCommandAnnotation marks a command whose --json output is an array. dispatch reads it
// to wrap a pre-flight error in the same single-element shape, so `dump --json | jq '.[]'`
// keeps working when the command fails before its per-file loop. Set at construction, so a
// new list command cannot be forgotten in a second hand-maintained list.
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

// emitsJSONList reports whether args resolve to a command whose --json output is a list.
// It resolves via cobra's own Find, which already handles "--", a flag before the
// subcommand, and the --format value. caps is a list command but answers a single object
// under --format; an unknown command resolves to the root and stays an object too.
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

// hasFlag reports whether --name appears in args, in either spelling. It stops at "--", so
// a positional that merely looks like the flag is not mistaken for it.
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

// noteNoFiles prints a note when a path list is empty, reachable only from a --recursive
// walk that matched nothing, so a typo'd or audio-free directory is not a silent success.
// Text-mode only: the JSON stdout shape is fixed and still emits [].
func noteNoFiles(w io.Writer, paths []string, asJSON bool) {
	if asJSON || len(paths) != 0 {
		return
	}
	// "note:" rather than "waxlabel:", so an exit-0 advisory does not read as a failure.
	fmt.Fprintln(w, "note: no audio files found")
}

// noteSkipped reports how many files a --recursive walk passed over by extension, so a
// directory of unrecognized files is not a silent near-no-op. Pairs with noteNoFiles,
// which it explains when nothing matched.
func noteSkipped(w io.Writer, skipped int, asJSON bool) {
	if asJSON || skipped == 0 {
		return
	}
	fmt.Fprintf(w, "note: %d file(s) skipped (not recognized by extension)\n", skipped)
}

// usageError marks a bad-arguments failure, exit 2. The extra fields are set only at the
// cobra-origin sites that dead-end with no guidance: cmd names the command for the help
// hint, wantsHint asks for the "run --help" pointer, multiline marks trusted cobra text
// whose newlines must survive rendering, and hint overrides the wantsHint pointer. The
// hand-written usagef messages leave all four zero, being single-line and self-documenting.
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

// checkArgText reclassifies the library's writable-text rejection as exit 2: a bad
// command-line value is a bad invocation, not a corrupt media file. The reason phrase comes
// from WritableTextReason, the same rule the library backstop enforces, so the two cannot
// drift. argv cannot carry a NUL, but --synced-lyrics-file content can.
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

// renderError writes the terminal error as JSON or a human line, from one shared
// classification. emitList wraps the JSON envelope in a single-element array for a list
// command, so its pre-flight failures keep the array shape `jq '.[]'` relies on.
func renderError(w io.Writer, jsonMode, emitList bool, err error) {
	if err == nil {
		return
	}
	c := classifyError(err)
	// Silent: the reader closed the pipe deliberately, and there may be no stdout left to
	// write an envelope to. exitCodeFor still returns 0.
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
	// A single-line message can embed a file-derived path, so SanitizeLine escapes \n and
	// \t too, blocking line-forgery. A multiline one is trusted cobra text, where
	// SanitizeText keeps the real newlines while still escaping ESC/CSI/BEL/CR; otherwise
	// the suggestion block would show literal \x0a.
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

// perFileError writes the standard "waxlabel: <path>: <reason>" failure line, so every
// per-file command shares one format and one displayName/perFileReason pair.
func perFileError(w io.Writer, path string, err error) {
	fmt.Fprintf(w, "waxlabel: %s: %s\n", displayName(path), perFileReason(err))
}

// perFileReason renders a per-file error for a command that already prints the path, so a
// bare *fs.PathError is reduced to its underlying reason rather than naming the path twice.
//
// A not-found is normalized to classifyError's wording, so the human line and the --json
// envelope agree on every platform. Only not-found: a permission failure still reads
// "Access is denied." on Windows and "permission denied" on Unix. See deferred-work.md.
func perFileReason(err error) string {
	if pe, ok := err.(*fs.PathError); ok {
		if errors.Is(pe.Err, fs.ErrNotExist) {
			return notFoundReason
		}
		return pe.Err.Error()
	}
	return cleanMessage(err.Error())
}

// notFoundReason is the canonical not-found wording, shared by the human per-file
// line and the --json envelope. POSIX phrasing, because that is what already shipped.
const notFoundReason = "no such file or directory"

// writeFailed reports whether a [wl.Plan.Execute] result is a genuine per-file failure.
// The question is "did the bytes land", not "was there an error": an error after a
// committed write leaves the edit applied and the plan spent, so calling it a failure
// would state the opposite of what happened. Such a write counts as changed, with
// warnPostCommit naming the step. set, copy, and lint --fix all branch here.
func writeFailed(res wl.SaveResult, err error) bool { return err != nil && !res.Committed }

// warnPostCommit notes a committed write whose post-commit step failed (see writeFailed).
// path must be the file WRITTEN, which under set -o is the output, not the input. Nil
// errors and --json runs are no-ops: there the note rides jsonPostWrite instead.
func warnPostCommit(w io.Writer, asJSON bool, path string, err error) {
	if err == nil || asJSON {
		return
	}
	fmt.Fprintf(w, "waxlabel: %s: written, but a step after the write failed: %s\n",
		displayName(path), postWriteReason(err))
}

// jsonPostWrite carries a post-commit failure in the writing commands' payloads. A
// warning, not an error envelope: committed stays true. Kept out of the report's warnings
// array, which carries library plan warnings.
type jsonPostWrite struct {
	PostWriteWarning string `json:"postWriteWarning,omitempty"`
}

func (j *jsonPostWrite) setPostWrite(err error) {
	if err != nil {
		j.PostWriteWarning = postWriteReason(err)
	}
}

// postWriteReason renders a post-commit failure for the stderr note and the JSON
// field. Not perFileReason: that drops a *fs.PathError's path as a duplicate of the
// one its caller printed, but a post-commit step names the directory, not the file.
func postWriteReason(err error) string { return cleanMessage(err.Error()) }

// classifiedError is every user-visible representation of a terminal error. multiline
// marks trusted cobra text whose newlines must survive the human render.
type classifiedError struct {
	exitCode  int
	code      string
	message   string
	hint      string
	multiline bool
}

// errBrokenPipe is the SIGPIPE goroutine's cancel cause, the only thing distinguishing a
// closed pipe from a real interrupt: both leave a canceled op returning context.Canceled.
// classifyError maps it to a silent exit 0, the Unix convention for `... | head`.
var errBrokenPipe = errors.New("broken output pipe")

// isPipeClose reports whether err is the symptom of a closed output pipe: the context's
// cancellation, or a broken-pipe errno from a write that raced the reader's close.
// perFile pairs it with the errBrokenPipe cause, so a genuine file error that merely
// coincided with the close is still recorded.
func isPipeClose(err error) bool {
	return errors.Is(err, context.Canceled) || isBrokenPipe(err)
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
		// Its own code, not folded into unsupported-tag: two refusal reasons reading as
		// one lose signal.
		c.exitCode, c.code = 3, "unsupported-stream"
	case errors.Is(err, waxerr.ErrUnalignedStream):
		// Reads fine, just cannot be rewritten safely, so a refusal and not a corrupt file.
		c.exitCode, c.code = 3, "unsupported-alignment"
	case errors.Is(err, waxerr.ErrFragmented):
		// Another well-formed-but-unwritable shape: the initial movie box carries the tags.
		c.exitCode, c.code = 3, "unsupported-fragmentation"
	case errors.Is(err, waxerr.ErrSourceChanged):
		c.exitCode, c.code, c.hint = 5, "source-changed",
			"the file changed since it was read; re-run to pick up the new contents"
	case errors.Is(err, waxerr.ErrInputTooLarge):
		// A user-configured --max-size cap, not corruption: a raw stream has no declared
		// size, so this is not invalid-data.
		c.exitCode, c.code = 7, "input-too-large"
	case errors.Is(err, waxerr.ErrInvalidData),
		errors.Is(err, waxerr.ErrSizeTooLarge),
		errors.Is(err, waxerr.ErrTooDeep),
		errors.Is(err, waxerr.ErrPictureTooLarge):
		c.exitCode, c.code = 4, "invalid-data"
	case isNotFoundPathError(err):
		pe, _ := err.(*fs.PathError) // guaranteed by isNotFoundPathError
		// Matches the per-file line dump/set/verify print, so human and --json agree.
		c.exitCode, c.code, c.message = 6, "not-found", pe.Path+": "+notFoundReason
	case isLocalIOError(err):
		c.exitCode, c.code = 6, "io"
	}
	return c
}

func isUsageError(err error) bool {
	_, ok := errors.AsType[*usageError](err)
	return ok
}

// errClassRank orders per-file error classes for a multi-file run's aggregate exit code,
// most-severe first: a broken file must outrank a wrong path, which must outrank a bad
// invocation. The numeric exit code deliberately does not follow this order, since
// numeric-max would let not-found (6) beat invalid-data (4). An unrecognized code ranks 0
// and never masks a known class. Keep in sync with README's precedence list;
// TestErrClassRankCoversEveryErrorClass pins that no code falls to 0 silently.
var errClassRank = map[string]int{
	"canceled":                  100, // exit 130: an interrupted run dominates
	"timeout":                   100, // exit 130
	"source-changed":            90,  // exit 5
	"invalid-data":              80,  // exit 4: a corrupt file
	"input-too-large":           75,  // exit 7: a streamed input over the user's --max-size cap (not corruption)
	"unsupported-format":        70,  // exit 3
	"unsupported-tag":           65,  // exit 3
	"unsupported-stream":        64,  // exit 3: a chained/multiplexed Ogg stream
	"unsupported-alignment":     63,  // exit 3: a non-page-aligned Ogg stream
	"unsupported-fragmentation": 62,  // exit 3: a fragmented MP4
	"io":                        60,  // exit 6
	"not-found":                 55,  // exit 6: a wrong path
	"usage":                     20,  // exit 2: a bad invocation
	"invalid-key":               20,  // exit 2
	"needs-file":                20,  // exit 2: a path-less SaveBack (library callers)
	"error":                     10,  // exit 1: the unclassified fallback
	"broken-pipe":               5,   // exit 0: a closed output pipe, below every real failure so one still wins
}

// worseError reports whether candidate outranks current by errClassRank, so a run's exit
// code reflects the most-severe failure rather than the first. Equal ranks keep the
// incumbent, which is harmless since they share an exit code.
func worseError(current, candidate error) bool {
	if current == nil {
		return true
	}
	return errClassRank[classifyError(candidate).code] > errClassRank[classifyError(current).code]
}

// isNotFoundPathError reports whether err is, at the top level, a not-exist *fs.PathError,
// the one shape the CLI restates as a clean "<path>: no such file or directory". The
// assertion is direct rather than errors.As on purpose: an error a caller already wrapped
// keeps its own message, and a *os.LinkError stays in the I/O class, since only a
// *fs.PathError yields the clean path-only message "not-found" promises.
func isNotFoundPathError(err error) bool {
	pe, ok := err.(*fs.PathError)
	return ok && os.IsNotExist(pe)
}

// isLocalIOError reports whether err is a local filesystem failure: the three shapes the
// read and write paths produce, all mapping to the "io" exit class.
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

// looksLikePathFlag reports whether an unknown-flag message's offending token looks like a
// path, where the leading-dash hint beats the generic --help pointer. A genuine flag typo
// keeps --help. The token is the last word of cobra's message, whose fixed prefix carries
// no path bytes.
func looksLikePathFlag(msg string) bool {
	return looksLikePath(msg[strings.LastIndexByte(msg, ' ')+1:])
}

// looksLikeBareWord reports whether s is a plain word rather than a path: what an unquoted
// value fragment looks like, the "Words" left over from `--set TITLE=Two Words`. It backs
// the quoting hint.
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
				// newlines and point at the command list. cmd stays empty so the hint falls
				// back to "waxlabel", since an unknown command should list the commands
				// rather than a subcommand's flags.
				ue.multiline, ue.wantsHint = true, true
			case "unknown flag", "unknown shorthand":
				// Backstop for a flag error that reaches here untyped; in practice cobra
				// routes flag-parse failures through FlagErrorFunc, which attaches
				// dashPathHint the same way. Only when the token looks like a path, so a
				// genuine typo is left to the help hint.
				if looksLikePathFlag(msg) {
					ue.hint = dashPathHint
				}
			}
			return ue
		}
	}
	return err
}
