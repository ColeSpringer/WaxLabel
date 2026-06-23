package main

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	wl "github.com/colespringer/waxlabel"
)

// stdinArg is the conventional path that means "read standard input". It is kept
// as the display name in output so a buffered-stdin temp path never leaks.
const stdinArg = "-"

// bufferStdin copies all of standard input to a temp file (a pipe has no ReaderAt
// or Size, which the parsers need) and returns its path plus a cleanup that
// removes it. stdin is consumed here, so a caller must invoke this at most once
// per run.
func bufferStdin(stdin io.Reader) (path string, cleanup func(), err error) {
	noop := func() {}
	tmp, err := os.CreateTemp("", "waxlabel-stdin-*")
	if err != nil {
		return "", noop, err
	}
	name := tmp.Name()
	remove := func() { _ = os.Remove(name) }
	if _, err := io.Copy(tmp, stdin); err != nil {
		_ = tmp.Close()
		remove()
		return "", noop, err
	}
	if err := tmp.Close(); err != nil {
		remove()
		return "", noop, err
	}
	return name, remove, nil
}

// readInputs prepares a read command's path arguments for parsing. Standard input
// ("-") is buffered to one shared temp file (a pipe can be read only once, so
// repeated "-" arguments all resolve to that single buffer). It returns realOf,
// which maps each original argument to the path to parse - the temp file for "-",
// the argument itself otherwise - and a cleanup that removes the temp file. The
// original argument stays the display name, so "-" never surfaces as a temp path.
func readInputs(stdin io.Reader, paths []string) (realOf func(string) string, cleanup func(), err error) {
	cleanup = func() {}
	stdinReal := ""
	if slices.Contains(paths, stdinArg) {
		real, cl, e := bufferStdin(stdin)
		if e != nil {
			return nil, cleanup, e
		}
		stdinReal, cleanup = real, cl
	}
	// stdinReal is non-empty exactly when a "-" was buffered, so it doubles as the
	// "did we buffer?" flag - no separate bool to keep in sync.
	realOf = func(p string) string {
		if p == stdinArg && stdinReal != "" {
			return stdinReal
		}
		return p
	}
	return realOf, cleanup, nil
}

// parseInput parses the file at realPath but reports it under origPath's display
// name, so a buffered-stdin temp path never leaks into the library's
// "could not identify" error (B1). realPath is the path actually read (the temp
// file for "-"); origPath is the user's argument ("-" or the real path), which
// displayName turns into "<stdin>" or a sanitized path. Routing every read
// command's ParseFile through this one helper keeps the source-name plumbing from
// being forgotten at a call site. extra carries any per-call parse options.
func parseInput(ctx context.Context, realPath, origPath string, extra ...wl.ParseOption) (*wl.Document, error) {
	return wl.ParseFile(ctx, realPath, append(extra, wl.WithSourceName(displayName(origPath)))...)
}

// expandPaths expands directory arguments into the audio files they contain when
// recursive is set, walking each tree and keeping files whose extension matches a
// known codec (a cheap filter that skips unrelated files without parsing them).
// Ordinary files and the "-" stdin sentinel pass through unchanged and in order.
// A stat or walk failure on an argument leaves it in place for the per-file loop
// to classify, rather than aborting the whole run.
//
// Without recursive, a directory argument cannot be processed, so expandPaths
// returns a usage error (exit 2) naming --recursive instead of letting the
// directory fall through to the parser's ErrInvalidData (exit 4); this fixes both
// the exit class and discoverability in one place. A stat error on an argument
// (e.g. a nonexistent path) still passes through, so the per-file loop classifies
// it as not-found (exit 6) - only a confirmed directory is rejected here.
//
// On a recursive walk it also returns the count of regular files passed over for
// not matching a known audio extension, across every directory argument. The caller
// surfaces that count as a text-mode note. The non-recursive path walks nothing, so
// its count is always zero.
func expandPaths(paths []string, recursive bool) ([]string, int, error) {
	// An empty operand is a usage error (exit 2), caught before any stat/parse so it
	// does not reach the library's ErrInvalidData (exit 4) fallback and outrank a real
	// not-found in a multi-file run. Covers dump/verify/plan/set/lint.
	if err := checkEmptyOperands(paths...); err != nil {
		return nil, 0, err
	}
	if !recursive {
		for _, p := range paths {
			if p == stdinArg {
				continue
			}
			// One stat per arg, reused below: a directory has more specific guidance
			// (--recursive walks it), so reject it first with that message; otherwise
			// checkRegularFileInfo catches a FIFO/device/socket before the per-file parse
			// opens it (which, for a FIFO, would block). These commands stream stdin, so
			// the non-regular hint may suggest "-" (acceptsStdin true).
			info, statErr := os.Stat(p)
			if statErr == nil && info.IsDir() {
				return nil, 0, usagef("%s is a directory; pass --recursive to walk it for audio files", p)
			}
			if err := checkRegularFileInfo(p, info, statErr, true); err != nil {
				return nil, 0, err
			}
		}
		return paths, 0, nil
	}
	var out []string
	skipped := 0
	for _, p := range paths {
		if p == stdinArg {
			out = append(out, p)
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			// Not a directory (or unstattable): reject a directly-named FIFO/device/socket
			// here too (reusing the stat above), so it cannot wedge the batch and so the
			// recursive and non-recursive branches agree (both exit 2). A regular file or a
			// nonexistent path passes through to the per-file loop, which parses it or
			// classifies it as not-found.
			if cerr := checkRegularFileInfo(p, info, err, true); cerr != nil {
				return nil, 0, cerr
			}
			out = append(out, p)
			continue
		}
		files, sk := walkAudioFiles(p)
		out = append(out, files...)
		skipped += sk
	}
	return out, skipped, nil
}

// checkRegularFile rejects a path that exists but is not a regular file - a FIFO,
// device, socket, or directory - as a usage error (exit 2). It is the CLI choke
// point that turns the library's exit-4 backstop into a precise exit-2 message
// before any parse, and the same guard loadPictureFile applies to an --add-cover /
// --add-picture source. acceptsStdin tailors the FIFO/device/socket hint: a command
// that reads "-" from standard input points there, one that does not (copy) suggests
// a regular file instead.
// It distinguishes exists-and-non-regular (the usage error) from does-not-exist
// (returns nil, so the caller's own not-found path - exit 6 - still owns a typo'd
// path) and from a regular file (nil). A FIFO is the case that matters most: os.Open
// blocks on its read end, so it must be caught before the file is opened.
func checkRegularFile(path string, acceptsStdin bool) error {
	info, err := os.Stat(path)
	return checkRegularFileInfo(path, info, err, acceptsStdin)
}

// checkRegularFileInfo is checkRegularFile given an os.Stat result the caller already
// obtained, so a caller that stats the path for its own reasons (expandPaths, which
// also tests for a directory) need not stat it twice - which would also open a
// window for the path to change between the two stats. A non-nil statErr means the
// path does not exist (or is unstattable): it returns nil so the caller's own
// not-found path owns it. info is read only when statErr is nil. acceptsStdin tailors
// the FIFO/device/socket hint (see checkRegularFile).
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
	// FIFO, device, or socket. Point at the escape hatch that fits the command: the
	// stdin sentinel for a command that streams ("-"), or a plain file path for copy,
	// which rejects "-" - so its hint must not suggest one.
	if acceptsStdin {
		return usagef("%s is not a regular file; pipe a stream in with %q instead", path, stdinArg)
	}
	return usagef("%s is not a regular file; pass a regular file path instead", path)
}

// checkRegularInputs applies the checkRegularFile guard to each operand of a command
// that parses its inputs directly rather than through expandPaths - caps, diff, and
// copy, which take fixed operands and do not walk directories. Without it those
// commands would fall through to the library's exit-4 backstop for a FIFO/directory
// (still no hang, but a less precise class and message than the exit-2 dump/verify/
// plan/set/lint return for the same input). It checks the resolved path (so a "-"
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

// checkEmptyOperands rejects an empty-string path operand as a usage error (exit 2),
// single-sourcing the check and its message across every command that validates
// operands at the CLI boundary: expandPaths (dump/verify/plan/set/lint) and the
// direct-operand copy/diff (which do not walk, so they call this themselves). Catching
// it here keeps an empty name from reaching the library's ErrInvalidData (exit 4)
// fallback and outranking a real not-found in a multi-file run. "-" (the stdin
// sentinel) is a real operand and is left for the command's own stdin handling.
func checkEmptyOperands(paths ...string) error {
	for _, p := range paths {
		if p == "" {
			return usagef("input filename is empty")
		}
	}
	return nil
}

// isWalkCandidate reports whether a non-directory walk entry is a file the recursive
// walk treats as a candidate: a regular file, or a symlink that resolves to a regular
// file - or a dangling one, passed through so the per-file loop reports it as not-found
// rather than dropping it silently from a library scan. A FIFO/socket/device, or a
// symlink to one, is not a candidate: it cannot wedge the batch and is not a file to
// parse. It is the single predicate shared by the inclusion of audio-extension entries
// and the skipped-count of the rest (walkAudioFiles), so the two cannot drift on which
// entries count as files. WalkDir does not follow symlinks, so a symlink target is
// resolved with os.Stat, which fails fast on a dangling link (it cannot block, unlike a
// FIFO).
func isWalkCandidate(path string, d fs.DirEntry) bool {
	switch {
	case d.Type().IsRegular():
		return true
	case d.Type()&fs.ModeSymlink != 0:
		info, err := os.Stat(path)
		return err != nil || info.Mode().IsRegular()
	default:
		return false
	}
}

// walkAudioFiles returns the audio files under root, recursively and in sorted
// order, selected by matching each candidate file's extension against the known codec
// extensions, along with a count of candidate files passed over for not matching a
// known extension. A walk error on an entry is skipped (the entry is omitted) so one
// unreadable file does not fail the whole tree; a matching-extension file that is
// malformed still surfaces its parse error later, in the per-file loop. The skipped
// count drives the run's "N file(s) skipped" note, so a directory of unrecognized
// files is not a silent near-no-op. Inclusion and the skipped-count share
// isWalkCandidate, so they cannot disagree on what is a file.
func walkAudioFiles(root string) ([]string, int) {
	// WalkDir lstats its root, so a symlinked-directory argument yields a symlink node
	// it refuses to descend (WalkDir never follows links). Resolve the named root link
	// once and walk the real directory, then map every match back under the user's
	// original argument so display and I/O keep the path they passed. Only the root is
	// resolved: interior directory symlinks stay skipped (isWalkCandidate follows
	// symlinks only to regular files), so following the named root cannot reintroduce
	// traversal-cycle risk.
	walkRoot, linked := resolvedWalkRoot(root)
	var out []string
	skipped := 0
	_ = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isWalkCandidate(path, d) {
			return nil
		}
		if isAudioExtension(filepath.Ext(path)) {
			out = append(out, rebaseWalkPath(root, walkRoot, linked, path))
		} else {
			// A candidate file passed over for its extension (a cover.jpg, a notes.txt, a
			// symlinked image) - counted so a directory of unrecognized files is not a
			// silent near-no-op.
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
// resolved - interior links are left to isWalkCandidate, avoiding cycle risk.
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
// original root argument, so a symlinked-directory walk lists and reads files under
// the name the user passed rather than the link's target. When the root was not a
// resolved link (linked false) the path is already correct and returned unchanged; a
// Rel failure (paths on different volumes - not possible for a walk descendant) also
// falls back to the path as found.
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

// audioExtensions is the set of file extensions any implemented codec claims,
// gathered once from the library's format list so the walker's filter tracks the
// codecs automatically as formats are added.
var audioExtensions = func() map[string]bool {
	m := make(map[string]bool)
	for _, f := range wl.Formats() {
		for _, ext := range wl.ExtensionsFor(f) {
			m[ext] = true
		}
	}
	return m
}()

// isAudioExtension reports whether ext (with its leading dot) is claimed by a
// known codec.
func isAudioExtension(ext string) bool {
	return audioExtensions[strings.ToLower(ext)]
}
