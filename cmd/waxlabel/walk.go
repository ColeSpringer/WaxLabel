package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
	"github.com/spf13/cobra"
)

// stdinArg means "read standard input". Kept as the display name so buffered-stdin temp paths never appear in output.
const stdinArg = "-"

// bufferStdin copies stdin to a temp file (pipes lack ReaderAt/Size) and returns its path
// plus cleanup. Consumes stdin; call at most once per run. maxSize > 0 caps disk use. A read
// that blocks ends when ctx does.
func bufferStdin(ctx context.Context, stdin io.Reader, maxSize int64) (path string, cleanup func(), err error) {
	noop := func() {}
	dir := os.TempDir()
	tmp, err := os.CreateTemp(dir, "waxlabel-stdin-*")
	if err != nil {
		return "", noop, wl.NewTempCreateError(dir, err)
	}
	name := tmp.Name()
	// Registered before io.Copy so forced exit mid-copy still deletes the temp file.
	// Close before remove: Windows cannot delete open files. Quit mid-copy may leave
	// an in-flight write holding a handle, so remove can still fail.
	deregister := registerCleanup(func() { _ = tmp.Close(); _ = os.Remove(name) })
	cleanup = func() {
		deregister()
		_ = os.Remove(name)
	}
	// math.MaxInt64 would overflow maxSize+1 below; io.LimitReader would then read nothing.
	if maxSize == math.MaxInt64 {
		maxSize = 0
	}
	// maxSize+1: exact-length streams buffer; overflow is detected below. LimitReader(maxSize) would truncate and misparse.
	src := stdin
	if maxSize > 0 {
		src = io.LimitReader(stdin, maxSize+1)
	}
	written, err := copyUntil(ctx, tmp, src)
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return "", noop, spoolError(dir, name, err)
	}
	if maxSize > 0 && written > maxSize {
		_ = tmp.Close()
		cleanup()
		return "", noop, fmt.Errorf("%w: standard input exceeds %s", waxerr.ErrInputTooLarge, wl.HumanBytes(maxSize))
	}
	if err = tmp.Close(); err != nil {
		cleanup()
		return "", noop, spoolError(dir, name, err)
	}
	return name, cleanup, nil
}

// spoolError names dir in place of the temp file name in a failure on that file. Any other
// error (the stream's own) is returned as it is.
func spoolError(dir, name string, err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok && pe.Path == name {
		return fmt.Errorf("buffer standard input in %s: %w", dir, os.NewSyscallError(pe.Op, pe.Err))
	}
	return err
}

// copyUntil is io.Copy that answers to ctx: once ctx ends it returns ctx's error, and a read of
// src still blocked (a terminal, a pipe nobody writes to) is abandoned with its goroutine.
func copyUntil(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	type result struct {
		n   int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := io.Copy(dst, src)
		done <- result{n, err}
	}()
	select {
	case res := <-done:
		return res.n, res.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// readInputs buffers "-" to one temp file (pipes are read-once). Second "-" is a usage error.
// Returns realOf (arg to parse path) and cleanup, and keeps orig args for display names. A
// failure to buffer the stream is stdinErr: the "-" operand's own, not the run's.
func readInputs(ctx context.Context, stdin io.Reader, maxSize int64, paths []string) (realOf func(string) string, cleanup func(), stdinErr, err error) {
	cleanup = func() {}
	seenStdin := false
	for _, p := range paths {
		if p != stdinArg {
			continue
		}
		if seenStdin {
			return nil, cleanup, nil, usagef("standard input (%q) cannot be specified more than once", stdinArg)
		}
		seenStdin = true
	}
	stdinReal := ""
	if seenStdin {
		stdinReal, cleanup, stdinErr = bufferStdin(ctx, stdin, maxSize)
	}
	// Non-empty iff "-" was buffered; avoids a separate bool.
	realOf = func(p string) string {
		if p == stdinArg && stdinReal != "" {
			return stdinReal
		}
		return p
	}
	return realOf, cleanup, stdinErr, nil
}

// parseInput parses realPath but uses origPath in errors so temp stdin paths never leak.
// WithSourceName gets the raw orig path (not displayName): %q escapes once; pre-sanitizing double-escapes tabs.
// All read commands go through here.
func parseInput(ctx context.Context, realPath, origPath string, extra ...wl.ParseOption) (*wl.Document, error) {
	name := origPath
	if s := stdinDisplay(origPath); s != "" {
		name = s
	}
	return wl.ParseFile(ctx, realPath, append(extra, wl.WithSourceName(name))...)
}

// expandPaths with --recursive walks directories for known-audio extensions. Files and "-"
// pass through in order. Without --recursive, directories and named FIFO/device/socket stay
// in the list with errors in pathErrors, as does "-" with stdinErr. The caller checks
// pathErrors first: bad paths fail per-element and good inputs still run. FIFOs are recorded,
// not opened: os.Open on a FIFO blocks. Stat and walk failures are per-path; only
// invocation-level failures return err.
//
// skipped counts regular files with unknown extensions; leftovers counts stale temp files from
// interrupted writes. Both are text-mode notes and zero without --recursive.
func expandPaths(paths []string, recursive bool, stdinErr error) (expanded []string, skipped, leftovers int, pathErrors map[string]error, err error) {
	// Exit 2 before stat so empty operands cannot become ErrInvalidData and outrank not-found.
	if err := checkEmptyOperands(paths...); err != nil {
		return nil, 0, 0, nil, err
	}
	pathErrors = operandErrors(true, stdinErr, paths...)
	if !recursive {
		for p, e := range pathErrors {
			if errors.Is(e, errIsDirectory) {
				pathErrors[p] = usagef("is a directory; pass --recursive to walk it for audio files")
			}
		}
		return paths, 0, 0, pathErrors, nil
	}
	var out []string
	for _, p := range paths {
		if !errors.Is(pathErrors[p], errIsDirectory) {
			// A file, a missing path or a recorded FIFO: the per-file loop reports it.
			out = append(out, p)
			continue
		}
		// The directory's own entry stays behind: only the paths in out are looked up.
		files, sk, lo, werrs := walkAudioFiles(p)
		out = append(out, files...)
		for path, e := range werrs {
			pathErrors[path] = walkError{e}
			out = append(out, path)
		}
		slices.Sort(out[len(out)-len(files)-len(werrs):])
		skipped += sk
		leftovers += lo
	}
	return out, skipped, leftovers, pathErrors, nil
}

// walkError: path added by expandPaths due to walk failure, not user input.
// Reported like other per-path errors but excluded from arity checks (-o single-input):
// unreadable subdir beside one audio file is still a one-file run.
type walkError struct{ err error }

func (e walkError) Error() string { return e.err.Error() }
func (e walkError) Unwrap() error { return e.err }

// namedInputs: expandPaths output minus walkError paths.
func namedInputs(paths []string, pathErrors map[string]error) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, ok := errors.AsType[walkError](pathErrors[p]); ok {
			continue
		}
		out = append(out, p)
	}
	return out
}

// guardPathErrors returns recorded pre-flight errors before os.Open.
// Invariant: recorded FIFOs are never opened (read blocks).
// set checks pathErrors inline in its write loop; must be first in the loop body.
func guardPathErrors[T any](pathErrors map[string]error, compute func(context.Context, string) (T, error)) func(context.Context, string) (T, error) {
	return func(ctx context.Context, path string) (T, error) {
		if e := pathErrors[path]; e != nil {
			var zero T
			return zero, e
		}
		return compute(ctx, path)
	}
}

// errIsDirectory is the usage error (exit 2) of a directory named where a file is read.
var errIsDirectory = usagef("is a directory, not a file")

// nonRegularError says why an existing path that is not a regular file cannot be read, without
// naming it: the caller prints the path. Nil for a regular file. acceptsStdin tailors the hint.
func nonRegularError(info fs.FileInfo, acceptsStdin bool) error {
	switch {
	case info.Mode().IsRegular():
		return nil
	case info.IsDir():
		return errIsDirectory
	case acceptsStdin:
		return usagef("is not a regular file; pipe a stream in with %q instead", stdinArg)
	}
	return usagef("is not a regular file; pass a regular file path instead")
}

// checkRegularFile is a usage error naming path when it exists and is not a regular file, for
// a file a flag names. Missing paths return nil so the caller owns not-found.
func checkRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if nerr := nonRegularError(info, false); nerr != nil {
		return usagef("%s %s", path, nerr)
	}
	return nil
}

// operandErrors records each operand that cannot be read before any is opened, so it fails
// alone: a named path that is not a regular file (opening a FIFO would block) and "-" when
// buffering it failed with stdinErr. A missing path is left for the parse to report.
func operandErrors(acceptsStdin bool, stdinErr error, operands ...string) map[string]error {
	errs := map[string]error{}
	for _, p := range operands {
		if p == stdinArg {
			if stdinErr != nil {
				errs[p] = stdinErr
			}
			continue
		}
		if info, err := os.Stat(p); err == nil {
			if nerr := nonRegularError(info, acceptsStdin); nerr != nil {
				errs[p] = nerr
			}
		}
	}
	return errs
}

// operandError is the failure of one operand of a command that takes a fixed few. It classifies
// as its cause and reads "<operand>: <reason>", the per-file line without its prefix.
type operandError struct {
	path string
	err  error
}

func (e *operandError) Error() string { return jsonFileName(e.path) + ": " + perFileReason(e.err) }
func (e *operandError) Unwrap() error { return e.err }

// parseOperands parses the operands of a command that takes a fixed few, under ctx, which a
// closed output pipe does not cancel. Every failure is reported, not only the first: a line
// each in text, and the worst as the error returned, for the exit code and the JSON envelope.
func parseOperands(cmd *cobra.Command, ctx context.Context, realOf func(string) string, pathErrors map[string]error, operands ...string) ([]*wl.Document, error) {
	docs := make([]*wl.Document, len(operands))
	var worst error
	for i, p := range operands {
		err := pathErrors[p]
		if err == nil {
			docs[i], err = parseInput(ctx, realOf(p), p)
		}
		if err == nil {
			continue
		}
		if worseError(worst, err) {
			worst = &operandError{path: p, err: err}
		}
		if !jsonMode(cmd) {
			perFileError(cmd.ErrOrStderr(), p, err)
		}
		// Interrupted: the operands left would only read canceled too.
		if ctx.Err() != nil {
			break
		}
	}
	if worst != nil && !jsonMode(cmd) {
		return nil, alreadyRendered(worst)
	}
	return docs, worst
}

// checkEmptyOperands: empty path is exit 2. Shared by expandPaths and copy/diff.
// Prevents ErrInvalidData from outranking not-found. "-" is valid.
func checkEmptyOperands(paths ...string) error {
	if slices.Contains(paths, "") {
		return usagef("input filename is empty")
	}
	return nil
}

// isWalkCandidate: regular file or symlink to one (dangling links included for not-found reporting).
// Not FIFO/socket/device. Shared by inclusion and skip counting. WalkDir does not follow links;
// os.Stat resolves symlinks and fails fast; cannot block like opening a FIFO.
func isWalkCandidate(path string, d fs.DirEntry) bool {
	switch {
	case d.Type().IsRegular():
		return true
	case d.Type()&fs.ModeSymlink != 0:
		// Stat failure: dangling link, kept. Exclude only symlinks to non-regular files.
		info, err := os.Stat(path)
		return err != nil || info.Mode().IsRegular()
	default:
		return false
	}
}

// walkAudioFiles: sorted audio paths under root, skip/leftover counts, walk errors by user path.
// Bad audio files still fail in the per-file parse loop. Counts feed skip/leftover notes; errors become io entries.
func walkAudioFiles(root string) (files []string, skipped, leftovers int, errs map[string]error) {
	// WalkDir lstats root and never follows links; symlinked directory args need root resolution
	// with paths rebased to the user's argument. Interior dir symlinks stay skipped (no cycle risk).
	walkRoot, linked := resolvedWalkRoot(root)
	var out []string
	errs = map[string]error{}
	_ = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable directory or unreadable root. WalkDir skipped it; record for per-file io reporting.
			errs[rebaseWalkPath(root, walkRoot, linked, path)] = err
			return nil
		}
		// Skip hidden directories (.git, .cache). Hidden root arg is still walked; only interior dirs pruned.
		if d.IsDir() {
			if path != walkRoot && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		// Hidden files are not skipped-count. Exception: stale leftover temps (same age gate as clean default).
		if strings.HasPrefix(d.Name(), ".") {
			if wl.IsTempFileName(d.Name()) && staleLeftover(d) {
				leftovers++
			}
			return nil
		}
		if !isWalkCandidate(path, d) {
			return nil
		}
		if isAudioExtension(filepath.Ext(path)) {
			out = append(out, rebaseWalkPath(root, walkRoot, linked, path))
		} else {
			// Non-audio regular files (cover.jpg, etc.) counted as skipped.
			skipped++
		}
		return nil
	})
	slices.Sort(out)
	return out, skipped, leftovers, errs
}

// staleLeftover: temp-named regular file older than cleanAgeGate (same rule as clean without --all).
// Newer files may still be in use; non-regular files excluded.
func staleLeftover(d fs.DirEntry) bool {
	info, err := d.Info()
	return err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) >= cleanAgeGate
}

// resolvedWalkRoot: directory to walk for recursive root. Symlink-to-dir root is EvalSymlink'd
// (linked=true; caller rebases paths). Plain dir, non-dir link, or unreadable link: walk as-is.
// Only root resolved; interior links handled by isWalkCandidate.
func resolvedWalkRoot(root string) (walkRoot string, linked bool) {
	li, err := os.Lstat(root)
	if err != nil || li.Mode()&fs.ModeSymlink == 0 {
		return root, false
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return root, false
	}
	if ri, err := os.Stat(resolved); err != nil || !ri.IsDir() {
		return root, false
	}
	return resolved, true
}

// rebaseWalkPath: map resolved walk path back under user's root arg. Unlinked root or Rel failure: unchanged.
func rebaseWalkPath(root, walkRoot string, linked bool, path string) string {
	if !linked {
		return path
	}
	rel, err := filepath.Rel(walkRoot, path)
	if err != nil {
		return path
	}
	return filepath.Join(root, rel)
}

// audioExtensions: all codec extensions from wl.Formats(); tracks new formats automatically.
var audioExtensions = func() map[string]bool {
	m := make(map[string]bool)
	for _, f := range wl.Formats() {
		for _, ext := range wl.ExtensionsFor(f) {
			m[ext] = true
		}
	}
	return m
}()

// isAudioExtension: ext (with dot) is a known codec extension.
func isAudioExtension(ext string) bool {
	return audioExtensions[strings.ToLower(ext)]
}
