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
)

// stdinArg means "read standard input". Kept as the display name so buffered-stdin temp paths never appear in output.
const stdinArg = "-"

// bufferStdin copies stdin to a temp file (pipes lack ReaderAt/Size) and returns its path
// plus cleanup. Consumes stdin; call at most once per run. maxSize > 0 caps disk use.
func bufferStdin(stdin io.Reader, maxSize int64) (path string, cleanup func(), err error) {
	noop := func() {}
	tmp, err := os.CreateTemp("", "waxlabel-stdin-*")
	if err != nil {
		return "", noop, err
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
	written, err := io.Copy(tmp, src)
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return "", noop, err
	}
	if maxSize > 0 && written > maxSize {
		_ = tmp.Close()
		cleanup()
		return "", noop, fmt.Errorf("%w: standard input exceeds %s", waxerr.ErrInputTooLarge, wl.HumanBytes(maxSize))
	}
	if err = tmp.Close(); err != nil {
		cleanup()
		return "", noop, err
	}
	return name, cleanup, nil
}

// readInputs buffers "-" to one temp file (pipes are read-once). Second "-" is a usage error.
// Returns realOf (arg to parse path), cleanup, and keeps orig args for display names.
func readInputs(stdin io.Reader, maxSize int64, paths []string) (realOf func(string) string, cleanup func(), err error) {
	cleanup = func() {}
	seenStdin := false
	for _, p := range paths {
		if p != stdinArg {
			continue
		}
		if seenStdin {
			return nil, cleanup, usagef("standard input (%q) cannot be specified more than once", stdinArg)
		}
		seenStdin = true
	}
	stdinReal := ""
	if seenStdin {
		real, cl, e := bufferStdin(stdin, maxSize)
		if e != nil {
			return nil, cleanup, e
		}
		stdinReal, cleanup = real, cl
	}
	// Non-empty iff "-" was buffered; avoids a separate bool.
	realOf = func(p string) string {
		if p == stdinArg && stdinReal != "" {
			return stdinReal
		}
		return p
	}
	return realOf, cleanup, nil
}

// parseInput parses realPath but uses origPath in errors so temp stdin paths never leak.
// WithSourceName gets the raw orig path (not displayName): %q escapes once; pre-sanitizing double-escapes tabs.
// All read commands go through here.
func parseInput(ctx context.Context, realPath, origPath string, extra ...wl.ParseOption) (*wl.Document, error) {
	return wl.ParseFile(ctx, realPath, append(extra, wl.WithSourceName(jsonFileName(origPath)))...)
}

// expandPaths with --recursive walks directories for known-audio extensions. Files and "-"
// pass through in order. Stat/walk failures stay for the per-file loop.
//
// Without --recursive, directories and named FIFO/device/socket stay in the list with errors
// in pathErrors. Caller checks pathErrors first: bad paths fail per-element; good inputs still run.
// FIFOs must be recorded, not opened: os.Open on a FIFO blocks.
// Only invocation-level failures return err and abort.
//
// skipped: regular files with unknown extensions. leftovers: stale temp files from interrupted writes.
// Both surfaced as text-mode notes. Zero without --recursive.
func expandPaths(paths []string, recursive bool) (expanded []string, skipped, leftovers int, pathErrors map[string]error, err error) {
	// Exit 2 before stat so empty operands cannot become ErrInvalidData and outrank real not-found.
	// Only invocation-level abort here; rest is per-path.
	if err := checkEmptyOperands(paths...); err != nil {
		return nil, 0, 0, nil, err
	}
	pathErrors = map[string]error{}
	if !recursive {
		for _, p := range paths {
			if p == stdinArg {
				continue
			}
			// One stat, reused below. Directory check wins; else checkRegularFileInfo catches FIFOs before parse would block.
			// Per-path errors; batch continues.
			info, statErr := os.Stat(p)
			if statErr == nil && info.IsDir() {
				// Callers add the "waxlabel: <path>: " prefix.
				pathErrors[p] = usagef("is a directory; pass --recursive to walk it for audio files")
				continue
			}
			if cerr := checkRegularFileInfo(p, info, statErr, true); cerr != nil {
				pathErrors[p] = cerr
			}
		}
		return paths, 0, 0, pathErrors, nil
	}
	var out []string
	for _, p := range paths {
		if p == stdinArg {
			out = append(out, p)
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			// Record FIFO/device/socket per path; regular or missing paths go to the per-file loop.
			if cerr := checkRegularFileInfo(p, info, err, true); cerr != nil {
				pathErrors[p] = cerr
			}
			out = append(out, p)
			continue
		}
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
		var we walkError
		if errors.As(pathErrors[p], &we) {
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

// checkRegularFile: non-regular existing paths exit 2 with a precise message (before library exit 4).
// Missing paths return nil so caller owns not-found. acceptsStdin tailors the FIFO hint.
// FIFO matters: os.Open on read end blocks.
func checkRegularFile(path string, acceptsStdin bool) error {
	info, err := os.Stat(path)
	return checkRegularFileInfo(path, info, err, acceptsStdin)
}

// checkRegularFileInfo: same as checkRegularFile but uses caller's stat (no double-stat TOCTOU).
// Non-nil statErr returns nil; caller owns not-found.
func checkRegularFileInfo(path string, info fs.FileInfo, statErr error, acceptsStdin bool) error {
	if statErr != nil {
		return nil // missing or unstattable: caller classifies not-found
	}
	if info.Mode().IsRegular() {
		return nil
	}
	if info.IsDir() {
		return usagef("%s is a directory, not a file", path)
	}
	// FIFO, device, or socket.
	if acceptsStdin {
		return usagef("%s is not a regular file; pipe a stream in with %q instead", path, stdinArg)
	}
	return usagef("%s is not a regular file; pass a regular file path instead", path)
}

// checkRegularInputs: checkRegularFile on direct-operand commands (caps, diff, copy) that skip expandPaths.
// Without this, FIFOs hit library exit 4 instead of exit 2. Checks resolved path ("-" -> buffered temp).
// Missing paths pass through to parse. acceptsStdin: true for caps/diff; false for copy (rejects "-").
func checkRegularInputs(realOf func(string) string, acceptsStdin bool, args ...string) error {
	for _, a := range args {
		if a == stdinArg {
			continue
		}
		if err := checkRegularFile(realOf(a), acceptsStdin); err != nil {
			return err
		}
	}
	return nil
}

// checkEmptyOperands: empty path is exit 2. Shared by expandPaths and copy/diff.
// Prevents ErrInvalidData from outranking real not-found. "-" is valid.
func checkEmptyOperands(paths ...string) error {
	for _, p := range paths {
		if p == "" {
			return usagef("input filename is empty")
		}
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
