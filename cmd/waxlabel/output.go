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

// schemaVersion tags JSON output for shape detection. Pinned at 1 until the
// command JSON shape changes incompatibly.
const schemaVersion = 1

// subformatOf returns JSON "subformat": exact container subtype when known, else
// the codec family used as "format". dump and caps share it so plain formats
// report subformat == format.
func subformatOf(container, format string) string {
	if container != "" {
		return container
	}
	return format
}

// writeJSON writes indented JSON. Unwraps sanitizingWriter: Encoder escapes C0 but
// emits DEL/C1 raw; sanitizing those yields invalid JSON. All JSON paths use this
// unwrap.
func writeJSON(w io.Writer, v any) error {
	if sw, ok := w.(*sanitizingWriter); ok {
		w = sw.Raw()
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// nonNil returns s or an empty slice so JSON marshals [] not null. append-built
// fields use []T{} for the same effect.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// sanitizingWriter is the human-output boundary: dispatch wraps stdout/stderr once so
// a future renderer cannot leak terminal controls. [tag.SanitizeText] is idempotent
// and composes with per-field escapes.
//
// Keeps '\n' for line separation; newline forgery is per-field [tag.SanitizeLine].
// JSON uses [sanitizingWriter.Raw].
//
// Not concurrent-safe: Write mutates buf without locking. Cobra serializes one
// command's renders.
type sanitizingWriter struct {
	w io.Writer
	// buf holds trailing incomplete UTF-8 between Writes so a split rune is not
	// escaped as a lone invalid lead byte.
	buf []byte
}

func newSanitizingWriter(w io.Writer) *sanitizingWriter { return &sanitizingWriter{w: w} }

// Raw returns the underlying writer for exact JSON bytes.
func (s *sanitizingWriter) Raw() io.Writer { return s.w }

// Write sanitizes p, holding back trailing incomplete UTF-8. Reports len(p) consumed;
// held tail is buffered, not rejected. Underlying error commits nothing (0 consumed),
// preserving prior tail for retry.
func (s *sanitizingWriter) Write(p []byte) (int, error) {
	data := p
	if len(s.buf) > 0 {
		data = append(s.buf, p...)
	}
	hold := incompleteSuffix(data)
	// string(...) copies prefix now so s.buf reslice below cannot alias it.
	clean := tag.SanitizeText(string(data[:len(data)-hold]))
	if _, err := io.WriteString(s.w, clean); err != nil {
		// Nothing committed: keep prior tail, report 0 consumed.
		return 0, err
	}
	// Committed: replace held tail with this write's incomplete remainder.
	s.buf = append(s.buf[:0], data[len(data)-hold:]...)
	return len(p), nil
}

// Close flushes held partial UTF-8. Incomplete bytes are escaped as invalid;
// dispatch calls before returning exit code.
func (s *sanitizingWriter) Close() error {
	if len(s.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(s.w, tag.SanitizeText(string(s.buf)))
	s.buf = nil
	return err
}

// incompleteSuffix is the byte length of a trailing incomplete multi-byte rune.
// Returns 0 on rune boundary or invalid final byte (escape now, do not hold).
func incompleteSuffix(p []byte) int {
	if len(p) == 0 {
		return 0
	}
	// Step back over continuation bytes to the lead byte.
	start := len(p) - 1
	for start > 0 && len(p)-start < utf8.UTFMax && p[start]&0xC0 == 0x80 {
		start--
	}
	if utf8.FullRune(p[start:]) {
		return 0
	}
	return len(p) - start
}

// jsonMode reads --json from parsed flags inside RunE. Persistent flag shared with root.
func jsonMode(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

// wantsJSON scans raw args for --json so dispatch routes terminal errors when cobra
// aborted before flag bind. Mirrors pflag bool forms.
func wantsJSON(args []string) bool {
	v := false
	for _, a := range args {
		switch {
		case a == "--":
			return v // after -- is positional
		case a == "--json":
			v = true
		case strings.HasPrefix(a, "--json="):
			// pflag keeps prior value when bool parse fails.
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

// jsonErrBody is code, message, and hint for terminal and per-file JSON errors.
// Hint matches classifiedError.hint so human and JSON cannot drift.
type jsonErrBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// jsonErrorEntry is the shared per-file error shape for list --json output. Only
// schemaVersion, file, and error: command-specific fields would leak as zero values
// and look like partial success. Each result struct has a matching Error field for
// mixed-array decoding.
type jsonErrorEntry struct {
	SchemaVersion int         `json:"schemaVersion"`
	File          string      `json:"file"`
	Error         jsonErrBody `json:"error"`
}

// errorEntry builds the shared per-file error element from err (not a pre-built
// classification) so JSON and human lines stay aligned. Message uses perFileReason:
// classifyError's wording suits the terminal envelope; here it would duplicate file
// or keep Go's syscall verb.
func errorEntry(path string, err error) jsonErrorEntry {
	c := classifyError(err)
	return jsonErrorEntry{
		SchemaVersion: schemaVersion,
		File:          jsonFileName(path),
		Error:         jsonErrBody{Code: c.code, Message: perFileReason(err), Hint: c.hint},
	}
}

// roundMs converts duration to JSON milliseconds (rounded, not floored).
func roundMs(d time.Duration) int64 { return int64(d.Round(time.Millisecond) / time.Millisecond) }

// humanDuration formats duration as H:MM:SS or M:SS. Sub-minute values show seconds
// so short clips are not flattened to 0:00.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "0:00"
	}
	if d < time.Minute {
		// Round to centiseconds first: 59.999s would print "60.00s" not "1:00".
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

// perFile runs dump/verify/plan/caps per path. Keeps most-severe error class, not first.
// Per-file errors go to stderr or JSON array. Blank line between text records unless
// noSeparator (one line per file for sort/uniq). One failure per array element keeps
// aggregate exit order-independent; unknown-key guard aborts up front.
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
			// Closed output pipe cancelled context: stop silently, do not print
			// "canceled" per remaining file. isPipeClose gates so a real file error
			// racing the close is still recorded.
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
			// Usually closed pipe; per-file worstErr outranks it. Returning werr would drop
			// accumulated exit 4 to broken-pipe.
			if worstErr != nil {
				return alreadyRendered(worstErr)
			}
			return werr
		}
	}
	return alreadyRendered(worstErr)
}

// emitJSONList writes items as a JSON array (even one path) so list --json is uniform.
// Empty result is []. diff and copy are single-object commands.
func emitJSONList(w io.Writer, items []any) error {
	if items == nil {
		items = []any{}
	}
	return writeJSON(w, items)
}

// listCommandAnnotation marks list --json commands. dispatch wraps pre-flight errors as
// a one-element array so `dump --json | jq '.[]'` works on early failure. Set at
// construction; no hand-maintained list to forget.
const listCommandAnnotation = "waxlabel/emitsJSONList"

// markListCommand tags cmd as list (see listCommandAnnotation) and returns it.
func markListCommand(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[listCommandAnnotation] = "true"
	return cmd
}

// emitsJSONList reports whether args resolve to a list --json command via cobra Find
// (handles "--", flags before subcommand, --format). caps is list but returns one
// object under --format. Unknown command resolves to root (object).
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

// hasFlag reports whether --name appears in args. Stops at "--" so positional
// lookalikes are ignored.
func hasFlag(args []string, name string) bool {
	long := "--" + name
	for _, a := range args {
		if a == "--" {
			break // after -- is positional
		}
		if a == long || strings.HasPrefix(a, long+"=") {
			return true
		}
	}
	return false
}

// noteNoFiles prints when --recursive walk matched nothing (typo or no audio).
// Text only; JSON still emits [].
func noteNoFiles(w io.Writer, paths []string, asJSON bool) {
	if asJSON || len(paths) != 0 {
		return
	}
	// "note:" not "waxlabel:" so exit-0 advisory does not read as failure.
	fmt.Fprintln(w, "note: no audio files found")
}

// noteSkipped reports files skipped by extension on --recursive walk. Pairs with
// noteNoFiles when nothing matched.
func noteSkipped(w io.Writer, skipped int, asJSON bool) {
	if asJSON || skipped == 0 {
		return
	}
	fmt.Fprintf(w, "note: %d file(s) skipped (not recognized by extension)\n", skipped)
}

// noteLeftovers reports temp files from interrupted writes (walker hides dotfiles).
// Off under --json.
func noteLeftovers(w io.Writer, n int, asJSON bool) {
	if n == 0 || asJSON {
		return
	}
	fmt.Fprintf(w, "note: %d leftover temp file(s) from an interrupted write; run 'waxlabel clean --recursive DIR' to list or remove them\n", n)
}

// usageError is bad-args failure (exit 2). Extra fields only at cobra dead-ends:
// cmd for help hint, wantsHint for "run --help", multiline for trusted cobra text,
// hint overrides wantsHint. usagef leaves all zero (self-documenting).
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

// checkArgText maps writable-text rejection to exit 2 (bad invocation, not corrupt
// file). Reason from WritableTextReason, same as library backstop. argv has no NUL;
// --synced-lyrics-file content can.
func checkArgText(value, what string) error {
	if reason := wl.WritableTextReason(value); reason != "" {
		return usagef("%s %s", what, reason)
	}
	return nil
}

// alreadyRenderedError marks failure already written. dispatch uses cause for exit
// code only.
type alreadyRenderedError struct{ cause error }

func (e *alreadyRenderedError) Error() string { return e.cause.Error() }
func (e *alreadyRenderedError) Unwrap() error { return e.cause }

// alreadyRendered wraps cause so dispatch skips re-render. nil cause returns nil.
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

// renderError writes terminal error as JSON or human line from one classification.
// emitList wraps JSON in one-element array for list pre-flight failures (`jq '.[]'`).
func renderError(w io.Writer, jsonMode, emitList bool, err error) {
	if err == nil {
		return
	}
	c := classifyError(err)
	// Silent: reader closed pipe; stdout may be gone. exitCodeFor still returns 0.
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
	// Single-line may embed file paths: SanitizeLine escapes \n/\t (line-forgery).
	// Multiline is trusted cobra text: SanitizeText keeps newlines, escapes controls;
	// else suggestion block shows literal \x0a.
	sanitize := tag.SanitizeLine
	if c.multiline {
		sanitize = tag.SanitizeText
	}
	fmt.Fprintf(w, "waxlabel: %s\n", sanitize(c.message))
	if c.hint != "" {
		fmt.Fprintf(w, "  hint: %s\n", tag.SanitizeLine(c.hint))
	}
}

// cleanMessage strips leading "waxlabel: " to avoid doubled prefix. Library sentinels
// omit it; direct errors may not.
func cleanMessage(msg string) string { return strings.TrimPrefix(msg, "waxlabel: ") }

// perFileError writes "waxlabel: <path>: <reason>" via displayName/perFileReason.
func perFileError(w io.Writer, path string, err error) {
	fmt.Fprintf(w, "waxlabel: %s: %s\n", displayName(path), perFileReason(err))
}

// perFileReason renders per-file error when path is already printed: bare *fs.PathError
// drops path (entry's own or internal temp). Wrapped PathError keeps both (caller context).
//
// Only canceled/timeout/not-found normalize to classifyError wording. Permission text
// stays platform-specific; machine code is the cross-platform contract.
func perFileReason(err error) string {
	// Order matches classifyError. broken-pipe never reaches here: perFile breaks on it.
	switch {
	case errors.Is(err, context.Canceled):
		return canceledReason
	case errors.Is(err, context.DeadlineExceeded):
		return timeoutReason
	}
	// walkError wraps walk *fs.PathError for arity rules; unwrap so PathError below drops
	// path caller prints itself.
	var we walkError
	if errors.As(err, &we) {
		err = we.Unwrap()
	}
	if pe, ok := err.(*fs.PathError); ok {
		if errors.Is(pe.Err, fs.ErrNotExist) {
			return notFoundReason
		}
		return pe.Err.Error()
	}
	return cleanMessage(err.Error())
}

// CLI-owned reason strings shared by per-file text and --json (one wording per failure).
const (
	notFoundReason = "no such file or directory" // POSIX phrasing (shipped)
	canceledReason = "canceled"
	timeoutReason  = "operation timed out"
)

// writeFailed is true when Execute failed without commit. Post-commit error after
// committed write is not failure (bytes landed). warnPostCommit names the step. set,
// copy, lint --fix branch here.
func writeFailed(res wl.SaveResult, err error) bool { return err != nil && !res.Committed }

// warnPostCommit notes committed write with failed post-commit step (see writeFailed).
// path is file written (set -o: output, not input). No-op on nil err or --json
// (jsonPostWrite there).
func warnPostCommit(w io.Writer, asJSON bool, path string, err error) {
	if err == nil || asJSON {
		return
	}
	fmt.Fprintf(w, "waxlabel: %s: written, but a step after the write failed: %s\n",
		displayName(path), postWriteReason(err))
}

// jsonPostWrite carries post-commit failure in write payloads (warning, not error;
// committed stays true). Not in report warnings (library plan warnings).
type jsonPostWrite struct {
	PostWriteWarning string `json:"postWriteWarning,omitempty"`
}

func (j *jsonPostWrite) setPostWrite(err error) {
	if err != nil {
		j.PostWriteWarning = postWriteReason(err)
	}
}

// postWriteReason renders post-commit failure for stderr/JSON. Not perFileReason:
// post-commit steps name directories, not the printed file path.
func postWriteReason(err error) string { return cleanMessage(err.Error()) }

// classifiedError is terminal error presentation. multiline: trusted cobra text keeps
// newlines in human render.
type classifiedError struct {
	exitCode  int
	code      string
	message   string
	hint      string
	multiline bool
}

// errBrokenPipe is SIGPIPE cancel cause (closed pipe vs interrupt both yield
// context.Canceled). classifyError: silent exit 0 (`... | head` convention).
var errBrokenPipe = errors.New("broken output pipe")

// isPipeClose is context cancel or broken-pipe errno from write racing reader close.
// perFile pairs with errBrokenPipe cause so coincident real file errors still record.
func isPipeClose(err error) bool {
	return errors.Is(err, context.Canceled) || isBrokenPipe(err)
}

// classifyError maps terminal error to exit code and machine code. Library sentinels
// beat filesystem fallback. Keep in sync with README exit-code list.
func classifyError(err error) classifiedError {
	if err == nil {
		return classifiedError{}
	}
	c := classifiedError{message: cleanMessage(err.Error()), exitCode: 1, code: "error"}
	switch {
	case errors.Is(err, errBrokenPipe):
		// Closed pipe (`... | head`): exit 0, no message. First: most specific.
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
				// Explicit hint (leading-dash "--" guidance) beats generic --help.
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
		// Path-less SaveBack: supported format, usage not unsupported-format. CLI always
		// has path; library callers only.
		c.exitCode, c.code = 2, "needs-file"
	case errors.Is(err, waxerr.ErrUnsupportedFormat):
		c.exitCode, c.code = 3, "unsupported-format"
	case errors.Is(err, waxerr.ErrUnsupportedTag):
		c.exitCode, c.code = 3, "unsupported-tag"
	case errors.Is(err, waxerr.ErrChainedStream):
		// Own code, not unsupported-tag: merged reasons lose signal.
		c.exitCode, c.code = 3, "unsupported-stream"
	case errors.Is(err, waxerr.ErrUnalignedStream):
		// Readable but not safely rewritable: refusal, not corrupt file.
		c.exitCode, c.code = 3, "unsupported-alignment"
	case errors.Is(err, waxerr.ErrFragmented):
		// Well-formed but unwritable: initial movie box carries tags.
		c.exitCode, c.code = 3, "unsupported-fragmentation"
	case errors.Is(err, waxerr.ErrPictureTooLarge):
		// Supplied cover too large for destination block/atom/frame. File reads fine;
		// write refusal not corrupt file. Above invalid-data (switch order).
		c.exitCode, c.code = 3, "picture-too-large"
	case errors.Is(err, waxerr.ErrSourceChanged):
		c.exitCode, c.code, c.hint = 5, "source-changed",
			"the file changed since it was read; re-run to pick up the new contents"
	case errors.Is(err, waxerr.ErrInputTooLarge):
		// User --max-size cap, not corruption (raw stream has no declared size).
		c.exitCode, c.code = 7, "input-too-large"
	case errors.Is(err, waxerr.ErrInvalidData),
		errors.Is(err, waxerr.ErrSizeTooLarge),
		errors.Is(err, waxerr.ErrTooDeep):
		c.exitCode, c.code = 4, "invalid-data"
	case isNotFoundPathError(err):
		pe, _ := err.(*fs.PathError) // guaranteed by isNotFoundPathError
		// Matches per-file line dump/set/verify print (human/JSON agree).
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

// errClassRank orders per-file classes for aggregate exit (most-severe wins). Exit
// codes do not follow this order (numeric-max would let not-found 6 beat invalid-data 4).
// Unknown code ranks 0. Keep in sync with README precedence.
//
// picture-too-large last among exit-3 classes (same exit code; rank picks reported
// message; unwritable file beats cover-too-large-only).
var errClassRank = map[string]int{
	"canceled":                  100, // exit 130
	"timeout":                   100, // exit 130
	"source-changed":            90,  // exit 5
	"invalid-data":              80,  // exit 4
	"input-too-large":           75,  // exit 7 (--max-size, not corruption)
	"unsupported-format":        70,  // exit 3
	"unsupported-tag":           65,  // exit 3
	"unsupported-stream":        64,  // exit 3: chained Ogg
	"unsupported-alignment":     63,  // exit 3: non-page-aligned Ogg
	"unsupported-fragmentation": 62,  // exit 3: fragmented MP4
	"picture-too-large":         61,  // exit 3: cover too large for destination
	"io":                        60,  // exit 6
	"not-found":                 55,  // exit 6
	"usage":                     20,  // exit 2
	"invalid-key":               20,  // exit 2
	"needs-file":                20,  // exit 2: path-less SaveBack
	"error":                     10,  // exit 1
	"broken-pipe":               5,   // exit 0: below real failures
}

// worseError reports whether candidate outranks current by errClassRank. Equal rank
// keeps incumbent (same exit code).
func worseError(current, candidate error) bool {
	if current == nil {
		return true
	}
	return errClassRank[classifyError(candidate).code] > errClassRank[classifyError(current).code]
}

// isNotFoundPathError is top-level not-exist *fs.PathError (CLI: "<path>: no such file
// or directory"). Direct assert, not errors.As: wrapped errors keep own message.
func isNotFoundPathError(err error) bool {
	pe, ok := err.(*fs.PathError)
	return ok && os.IsNotExist(pe)
}

// isLocalIOError is local filesystem failure (PathError, LinkError, SyscallError -> "io").
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

// dashPathHint for leading-dash path mistaken as flag: use "--". Conditional wording
// avoids misleading when wrong. Shared by FlagErrorFunc and normalizeExecuteError.
const dashPathHint = "if this was a file path beginning with '-', put '--' before it (e.g. waxlabel dump -- -track.flac)"

// looksLikePath: path separator or audio extension (not "log.level=debug"). Shared by
// looksLikePathFlag and looksLikeBareWord.
func looksLikePath(s string) bool {
	return strings.ContainsAny(s, "/\\") || isAudioExtension(filepath.Ext(s))
}

// looksLikePathFlag: unknown-flag token looks like path -> dashPathHint not --help.
// Token is last word of cobra message (prefix has no path bytes).
func looksLikePathFlag(msg string) bool {
	return looksLikePath(msg[strings.LastIndexByte(msg, ' ')+1:])
}

// looksLikeBareWord: plain word not path (e.g. unquoted `--set TITLE=Two Words` fragment).
func looksLikeBareWord(s string) bool {
	return !looksLikePath(s)
}

// normalizeExecuteError converts cobra unknown-command/flag errors to usageError (exit 2).
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
				// Cobra "Did you mean?" block: keep newlines. cmd empty -> hint "waxlabel".
				ue.multiline, ue.wantsHint = true, true
			case "unknown flag", "unknown shorthand":
				// Backstop; flag errors usually go through FlagErrorFunc with dashPathHint.
				// Genuine typo keeps --help hint.
				if looksLikePathFlag(msg) {
					ue.hint = dashPathHint
				}
			}
			return ue
		}
	}
	return err
}
