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
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				return nil, usagef("%s is a directory; pass --recursive to walk it for audio files", p)
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
			out = append(out, p)
			continue
		}
		out = append(out, walkAudioFiles(p)...)
	}
	return out, nil
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
		if isAudioExtension(filepath.Ext(path)) {
			out = append(out, path)
		}
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
