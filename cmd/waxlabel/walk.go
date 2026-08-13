package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	wl "github.com/colespringer/waxlabel"
	"github.com/colespringer/waxlabel/waxerr"
)

// stdinArg is the conventional path that means "read standard input". It is kept
// as the display name in output so a buffered-stdin temp path never leaks.
const stdinArg = "-"

// bufferStdin copies standard input to a temp file, since a pipe has no ReaderAt or Size,
// and returns its path plus a cleanup. It consumes stdin, so call it at most once per run.
// A positive maxSize stops an endless pipe from filling the disk.
func bufferStdin(stdin io.Reader, maxSize int64) (path string, cleanup func(), err error) {
	noop := func() {}
	tmp, err := os.CreateTemp("", "waxlabel-stdin-*")
	if err != nil {
		return "", noop, err
	}
	name := tmp.Name()
	// Registered before the io.Copy, so a forced exit mid-copy still deletes it. cleanup
	// both deregisters and removes, and runs on every exit path, so no entry is orphaned.
	//
	// Close before removing, since Windows cannot delete an open file. That covers the idle
	// handle, not every case: a quit landing mid-copy leaves an in-flight write holding a
	// reference, so the real CloseHandle is deferred and the remove can still fail.
	deregister := registerCleanup(func() { _ = tmp.Close(); _ = os.Remove(name) })
	cleanup = func() {
		deregister()
		_ = os.Remove(name)
	}
	// A bound at the int64 ceiling would overflow the maxSize+1 probe below to a negative
	// that io.LimitReader reads as "nothing", and nothing exceeds it anyway.
	if maxSize == math.MaxInt64 {
		maxSize = 0
	}
	// maxSize+1 so a stream of exactly maxSize still buffers while the first byte past it
	// is caught below; a plain LimitReader would truncate and misparse instead.
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

// readInputs prepares a read command's arguments for parsing. "-" is buffered to one temp
// file, since a pipe can be read only once; a second "-" would replay the same bytes and
// is a usage error. It returns realOf, mapping each argument to the path to parse, plus a
// cleanup. The original argument stays the display name, so "-" never shows a temp path.
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
	// Non-empty exactly when a "-" was buffered, so no separate bool to keep in sync.
	realOf = func(p string) string {
		if p == stdinArg && stdinReal != "" {
			return stdinReal
		}
		return p
	}
	return realOf, cleanup, nil
}

// parseInput parses realPath but reports it under origPath's name, so a buffered-stdin
// temp path never leaks into the library's "could not identify" error. The source name is
// the RAW path, not displayName: the library's %q already escapes control bytes once, so a
// pre-sanitized name would double-escape a tab. Every read command routes through here so
// the plumbing cannot be forgotten at a call site.
func parseInput(ctx context.Context, realPath, origPath string, extra ...wl.ParseOption) (*wl.Document, error) {
	return wl.ParseFile(ctx, realPath, append(extra, wl.WithSourceName(jsonFileName(origPath)))...)
}

// expandPaths expands directory arguments into their audio files when recursive is set,
// keeping files whose extension matches a known codec. Ordinary files and "-" pass through
// in order. A stat or walk failure stays in place for the per-file loop to classify.
//
// A directory without --recursive, or a directly-named FIFO/device/socket, stays in the
// list with its error recorded in pathErrors. The caller checks that map first, so the bad
// path surfaces as one per-element error while good inputs still process. Recording the
// FIFO rather than opening it is load-bearing: a per-file os.Open on one would block. Only
// an invocation-level failure returns err and aborts the run.
//
// skipped counts regular files passed over for not matching a known audio extension, which
// the caller surfaces as a text-mode note. Always zero without --recursive.
func expandPaths(paths []string, recursive bool) (expanded []string, skipped int, pathErrors map[string]error, err error) {
	// Exit 2 before any stat, so it cannot fall through to ErrInvalidData and outrank a
	// real not-found. The one invocation-level abort; everything below is per path.
	if err := checkEmptyOperands(paths...); err != nil {
		return nil, 0, nil, err
	}
	pathErrors = map[string]error{}
	if !recursive {
		for _, p := range paths {
			if p == stdinArg {
				continue
			}
			// One stat, reused below. A directory has more specific guidance, so it wins;
			// otherwise checkRegularFileInfo catches a FIFO before the parse opens it and
			// blocks. Recorded per path so the rest of the batch still runs.
			info, statErr := os.Stat(p)
			if statErr == nil && info.IsDir() {
				// No path in the detail: callers add the "waxlabel: <path>: " prefix.
				pathErrors[p] = usagef("is a directory; pass --recursive to walk it for audio files")
				continue
			}
			if cerr := checkRegularFileInfo(p, info, statErr, true); cerr != nil {
				pathErrors[p] = cerr
			}
		}
		return paths, 0, pathErrors, nil
	}
	var out []string
	for _, p := range paths {
		if p == stdinArg {
			out = append(out, p)
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			// Record a directly-named FIFO per path rather than wedging the batch, so both
			// branches agree. A regular or nonexistent path passes to the per-file loop.
			if cerr := checkRegularFileInfo(p, info, err, true); cerr != nil {
				pathErrors[p] = cerr
			}
			out = append(out, p)
			continue
		}
		files, sk := walkAudioFiles(p)
		out = append(out, files...)
		skipped += sk
	}
	return out, skipped, pathErrors, nil
}

// guardPathErrors wraps a per-file compute so a path carrying a recorded pre-flight error
// returns it as the literal first step, before any os.Open. Centralizing that is what
// guarantees the load-bearing invariant: a recorded FIFO is never opened, since its read
// would block. Only a command with a bespoke write loop, as set has, checks pathErrors
// inline instead, and must do so as the first statement of the loop body.
func guardPathErrors[T any](pathErrors map[string]error, compute func(context.Context, string) (T, error)) func(context.Context, string) (T, error) {
	return func(ctx context.Context, path string) (T, error) {
		if e := pathErrors[path]; e != nil {
			var zero T
			return zero, e
		}
		return compute(ctx, path)
	}
}

// checkRegularFile rejects a path that exists but is not a regular file as exit 2, the
// CLI choke point that turns the library's exit-4 backstop into a precise message before
// any parse. A nonexistent path returns nil, so the caller's not-found still owns a typo.
// acceptsStdin tailors the hint: a command reading "-" points there, copy does not.
//
// A FIFO is the case that matters: os.Open blocks on its read end.
func checkRegularFile(path string, acceptsStdin bool) error {
	info, err := os.Stat(path)
	return checkRegularFileInfo(path, info, err, acceptsStdin)
}

// checkRegularFileInfo is checkRegularFile given a stat the caller already has, so it need
// not stat twice and open a window for the path to change in between. A non-nil statErr
// returns nil, leaving the caller's not-found to own it; info is read only when it is nil.
func checkRegularFileInfo(path string, info fs.FileInfo, statErr error, acceptsStdin bool) error {
	if statErr != nil {
		return nil // does not exist (or unstattable): let the not-found path classify it
	}
	if info.Mode().IsRegular() {
		return nil
	}
	if info.IsDir() {
		return usagef("%s is a directory, not a file", path)
	}
	// FIFO, device, or socket: point at the escape hatch that fits the command.
	if acceptsStdin {
		return usagef("%s is not a regular file; pipe a stream in with %q instead", path, stdinArg)
	}
	return usagef("%s is not a regular file; pass a regular file path instead", path)
}

// checkRegularInputs applies the checkRegularFile guard to each operand of a command that
// parses its inputs directly rather than through expandPaths: caps, diff, and copy, which
// take fixed operands and do not walk. Without it they would fall through to the library's
// exit-4 backstop for a FIFO, a less precise class and message than the exit 2 the other
// commands give for the same input. It checks the resolved path (so a "-"
// maps to the buffered-stdin temp, a regular file, and passes) and lets a
// nonexistent path through to the parse's own not-found. acceptsStdin tailors the
// non-regular-file hint: caps/diff stream stdin and pass true; copy rejects "-" and
// passes false, so its hint does not suggest a "-" it would refuse.
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

// checkEmptyOperands rejects an empty path operand as exit 2, shared by expandPaths and
// the direct-operand copy/diff, which do not walk. Catching it here keeps an empty name
// from reaching ErrInvalidData and outranking a real not-found. "-" is a real operand.
func checkEmptyOperands(paths ...string) error {
	for _, p := range paths {
		if p == "" {
			return usagef("input filename is empty")
		}
	}
	return nil
}

// isWalkCandidate reports whether a non-directory walk entry is a file worth considering:
// a regular file, or a symlink to one. A dangling link counts too, so the per-file loop
// reports it as not-found rather than dropping it silently. A FIFO/socket/device does not.
// Shared by the inclusion and skipped-count paths, so the two cannot disagree on what a
// file is. WalkDir does not follow links, so os.Stat resolves them; it fails fast on a
// dangling link and cannot block the way opening a FIFO would.
func isWalkCandidate(path string, d fs.DirEntry) bool {
	switch {
	case d.Type().IsRegular():
		return true
	case d.Type()&fs.ModeSymlink != 0:
		// Stat failing means a dangling link, kept on purpose; only a link to a
		// non-regular file is excluded.
		info, err := os.Stat(path)
		return err != nil || info.Mode().IsRegular()
	default:
		return false
	}
}

// walkAudioFiles returns the audio files under root, sorted, selected by extension, plus a
// count of candidates passed over. An entry's walk error is skipped so one unreadable file
// does not fail the tree; a malformed file with a matching extension still surfaces its
// parse error in the per-file loop. The count drives the "N file(s) skipped" note.
func walkAudioFiles(root string) ([]string, int) {
	// WalkDir lstats its root and never follows links, so a symlinked-directory argument
	// would yield a node it refuses to descend. Resolve the root once and map matches back
	// under the user's argument. Only the root: interior directory symlinks stay skipped,
	// so this cannot reintroduce traversal-cycle risk.
	walkRoot, linked := resolvedWalkRoot(root)
	var out []string
	skipped := 0
	_ = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// Prune a hidden directory and its subtree: .git and .cache are not media trees.
		// An explicitly-named hidden root is still walked, so only interior ones go.
		if d.IsDir() {
			if path != walkRoot && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		// Not counted as skipped either: deliberately hidden, not unrecognized media.
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !isWalkCandidate(path, d) {
			return nil
		}
		if isAudioExtension(filepath.Ext(path)) {
			out = append(out, rebaseWalkPath(root, walkRoot, linked, path))
		} else {
			// A cover.jpg or notes.txt, counted so a directory of unrecognized files is
			// not a silent near-no-op.
			skipped++
		}
		return nil
	})
	slices.Sort(out)
	return out, skipped
}

// resolvedWalkRoot returns the real directory to walk for a recursive root argument.
// When root is itself a symlink to a directory, WalkDir would refuse to descend it
// (it never follows links), so the link is resolved with EvalSymlinks and linked is
// true (the caller maps matches back under root); a plain directory, a non-directory
// link, or an unreadable link is walked as-is (linked false). Only the named root is
// resolved; interior links are left to isWalkCandidate, avoiding cycle risk.
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

// rebaseWalkPath maps a path found under the resolved walk root back under the user's
// original argument, so a symlinked-directory walk reports the name they passed. An
// unresolved root, or a Rel failure, returns the path as found.
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

// audioExtensions is every extension a codec claims, gathered from the library's format
// list so the walker's filter tracks new formats automatically.
var audioExtensions = func() map[string]bool {
	m := make(map[string]bool)
	for _, f := range wl.Formats() {
		for _, ext := range wl.ExtensionsFor(f) {
			m[ext] = true
		}
	}
	return m
}()

// isAudioExtension reports whether ext, with its leading dot, is claimed by a codec.
func isAudioExtension(ext string) bool {
	return audioExtensions[strings.ToLower(ext)]
}
