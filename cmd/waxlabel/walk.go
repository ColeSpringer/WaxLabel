package main

import (
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
func expandPaths(paths []string, recursive bool) ([]string, error) {
	if !recursive {
		for _, p := range paths {
			if p == stdinArg {
				continue
			}
			// One stat per arg, reused below: a directory has more specific guidance
			// (--recursive walks it), so reject it first with that message; otherwise
			// checkRegularFileInfo catches a FIFO/device/socket before the per-file parse
			// opens it (which, for a FIFO, would block).
			info, statErr := os.Stat(p)
			if statErr == nil && info.IsDir() {
				return nil, usagef("%s is a directory; pass --recursive to walk it for audio files", p)
			}
			if err := checkRegularFileInfo(p, info, statErr); err != nil {
				return nil, err
			}
		}
		return paths, nil
	}
	var out []string
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
			if cerr := checkRegularFileInfo(p, info, err); cerr != nil {
				return nil, cerr
			}
			out = append(out, p)
			continue
		}
		out = append(out, walkAudioFiles(p)...)
	}
	return out, nil
}

// checkRegularFile rejects a path that exists but is not a regular file - a FIFO,
// device, socket, or directory - as a usage error (exit 2). It is the CLI choke
// point that turns the library's exit-4 backstop into a precise exit-2 message
// before any parse, and the same guard loadCovers applies to an --add-cover source.
// It distinguishes exists-and-non-regular (the usage error) from does-not-exist
// (returns nil, so the caller's own not-found path - exit 6 - still owns a typo'd
// path) and from a regular file (nil). A FIFO is the case that matters most: os.Open
// blocks on its read end, so it must be caught before the file is opened.
func checkRegularFile(path string) error {
	info, err := os.Stat(path)
	return checkRegularFileInfo(path, info, err)
}

// checkRegularFileInfo is checkRegularFile given an os.Stat result the caller already
// obtained, so a caller that stats the path for its own reasons (expandPaths, which
// also tests for a directory) need not stat it twice - which would also open a
// window for the path to change between the two stats. A non-nil statErr means the
// path does not exist (or is unstattable): it returns nil so the caller's own
// not-found path owns it. info is read only when statErr is nil.
func checkRegularFileInfo(path string, info fs.FileInfo, statErr error) error {
	if statErr != nil {
		return nil // does not exist (or unstattable): let the not-found path classify it
	}
	if info.Mode().IsRegular() {
		return nil
	}
	if info.IsDir() {
		return usagef("%s is a directory, not a file", path)
	}
	// FIFO, device, or socket: point at the escape hatch that does work for a stream.
	return usagef("%s is not a regular file; pipe a stream in with %q instead", path, stdinArg)
}

// checkRegularInputs applies the checkRegularFile guard to each operand of a command
// that parses its inputs directly rather than through expandPaths - caps, diff, and
// copy, which take fixed operands and do not walk directories. Without it those
// commands would fall through to the library's exit-4 backstop for a FIFO/directory
// (still no hang, but a less precise class and message than the exit-2 dump/verify/
// plan/set/lint return for the same input). It checks the resolved path (so a "-"
// maps to the buffered-stdin temp, a regular file, and passes) and lets a
// nonexistent path through to the parse's own not-found.
func checkRegularInputs(realOf func(string) string, args ...string) error {
	for _, a := range args {
		if a == stdinArg {
			continue
		}
		if err := checkRegularFile(realOf(a)); err != nil {
			return err
		}
	}
	return nil
}

// walkAudioFiles returns the audio files under root, recursively and in sorted
// order, selected by matching each file's extension against the known codec
// extensions. A walk error on an entry is skipped (the entry is simply omitted)
// so one unreadable file does not fail the whole tree; a matching-extension file
// that is malformed still surfaces its parse error later, in the per-file loop.
func walkAudioFiles(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !isAudioExtension(filepath.Ext(path)) {
			return nil
		}
		switch {
		case d.Type().IsRegular():
			out = append(out, path)
		case d.Type()&fs.ModeSymlink != 0:
			// WalkDir does not follow symlinks, so resolve the target with os.Stat. Include
			// a link to a regular file (README: "symlinks are followed") and a dangling link
			// too - os.Stat fails fast on it (it cannot block, unlike a FIFO), so passing it
			// through lets the per-file loop report the broken entry as not-found rather than
			// dropping it silently from a library scan. Skip only a link to a non-regular
			// target (a FIFO/socket/dir), which would be a non-audio or batch-wedging entry.
			if info, err := os.Stat(path); err != nil || info.Mode().IsRegular() {
				out = append(out, path)
			}
		}
		// A bare non-regular entry (a FIFO/socket/device file with an audio extension) is
		// neither case above, so it is skipped like a non-audio file - it cannot wedge the
		// batch, and it is not a file to parse.
		return nil
	})
	slices.Sort(out)
	return out
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
